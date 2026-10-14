// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/util"
)

func (h *Handler) distributions(w http.ResponseWriter, r *http.Request, ns string, segs []string) error {
	switch {
	case len(segs) == 0 && r.Method == http.MethodGet:
		return h.listDistributions(w, ns)
	case len(segs) == 0 && r.Method == http.MethodPost:
		return h.createDistribution(w, r, ns)
	}
	if len(segs) == 0 {
		return errf(http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
	}
	rec, err := h.load(ns, "distribution", segs[0])
	if err != nil {
		return errf(http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
	}
	switch {
	case len(segs) == 1 && r.Method == http.MethodGet:
		writeDoc(w, http.StatusOK, h.distributionDoc(rec), rec.ETag)
		return nil
	case len(segs) == 1 && r.Method == http.MethodDelete:
		return h.deleteDistribution(w, r, ns, rec)
	case len(segs) == 2 && segs[1] == "config" && r.Method == http.MethodGet:
		writeDoc(w, http.StatusOK, rec.config(), rec.ETag)
		return nil
	case len(segs) == 2 && segs[1] == "config" && r.Method == http.MethodPut:
		return h.updateDistribution(w, r, ns, rec)
	case len(segs) >= 2 && segs[1] == "invalidation":
		return h.invalidations(w, r, ns, rec, segs[2:])
	}
	return errf(http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
}

func (h *Handler) status(rec *record) string {
	if t, err := time.Parse(time.RFC3339Nano, rec.DeployedAt); err == nil && h.now().Before(t) {
		return "InProgress"
	}
	return "Deployed"
}

func (h *Handler) touch(rec *record) {
	rec.ETag = newETag()
	rec.Modified = h.timestamp()
	rec.DeployedAt = h.now().Add(DeployDelay).UTC().Format(time.RFC3339Nano)
}

func (h *Handler) distributionDoc(rec *record) *node {
	return elem("Distribution",
		leaf("Id", rec.ID),
		leaf("ARN", rec.ARN),
		leaf("Status", h.status(rec)),
		leaf("LastModifiedTime", rec.Modified),
		leaf("InProgressInvalidationBatches", "0"),
		leaf("DomainName", rec.DomainName),
		elem("ActiveTrustedSigners", leaf("Enabled", "false"), leaf("Quantity", "0")),
		elem("ActiveTrustedKeyGroups", leaf("Enabled", "false"), leaf("Quantity", "0")),
		rec.config(),
	)
}

func (h *Handler) listDistributions(w http.ResponseWriter, ns string) error {
	recs := h.all(ns, "distribution")
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	items := elem("Items")
	for _, rec := range recs {
		cfg := rec.config()
		s := elem("DistributionSummary",
			leaf("Id", rec.ID), leaf("ARN", rec.ARN), leaf("ETag", rec.ETag), leaf("Status", h.status(rec)),
			leaf("LastModifiedTime", rec.Modified), leaf("DomainName", rec.DomainName))
		for _, name := range []string{"Aliases", "Origins", "OriginGroups", "DefaultCacheBehavior", "CacheBehaviors",
			"CustomErrorResponses", "Comment", "PriceClass", "Enabled", "ViewerCertificate", "Restrictions",
			"WebACLId", "HttpVersion", "IsIPV6Enabled", "Staging"} {
			if c := cfg.child(name); c != nil {
				s.Children = append(s.Children, c)
			}
		}
		items.Children = append(items.Children, s)
	}
	doc := elem("DistributionList", leaf("Marker", ""), leaf("MaxItems", "100"), leaf("IsTruncated", "false"),
		leaf("Quantity", itoa(len(recs))), items)
	writeDoc(w, http.StatusOK, doc, "")
	return nil
}

func (h *Handler) createDistribution(w http.ResponseWriter, r *http.Request, ns string) error {
	doc, err := readDoc(r)
	if err != nil {
		return err
	}
	cfg, tags := doc, map[string]string{}
	if _, ok := r.URL.Query()["WithTags"]; ok {
		cfg = doc.child("DistributionConfig")
		if cfg == nil {
			return malformed(errMissing("DistributionConfig"))
		}
		if t := doc.child("Tags"); t != nil {
			tags = parseTags(t)
		}
	}
	if cfg.Name != "DistributionConfig" {
		return malformed(errMissing("DistributionConfig"))
	}
	if err := h.validateDistribution(ns, cfg); err != nil {
		return err
	}
	ref := cfg.text("CallerReference")
	for _, other := range h.all(ns, "distribution") {
		if other.config().text("CallerReference") == ref {
			return errf(http.StatusConflict, "DistributionAlreadyExists",
				"The caller reference that you are using to create a distribution is associated with another distribution. Already exists: %s", other.ID)
		}
	}
	normalizeDistribution(cfg)

	id := h.uniqueDistributionID()
	rec := &record{
		ID:         id,
		ARN:        "arn:aws:cloudfront::" + util.AccountID(r) + ":distribution/" + id,
		DomainName: strings.ToLower(id) + ".cloudfront.net",
		Config:     cfg.String(),
		Tags:       tags,
		AccountID:  util.AccountID(r),
	}
	h.touch(rec)
	if err := h.save(ns, "distribution", rec, true); err != nil {
		return err
	}
	w.Header().Set("Location", "https://cloudfront.amazonaws.com"+apiVersion+"distribution/"+id)
	writeDoc(w, http.StatusCreated, h.distributionDoc(rec), rec.ETag)
	return nil
}

// uniqueDistributionID avoids IDs other tenants hold: the data plane finds
// distributions by ID alone.
func (h *Handler) uniqueDistributionID() string {
	for {
		id := randomID("E", 13)
		if !h.distributionExists(id) {
			return id
		}
	}
}

func (h *Handler) updateDistribution(w http.ResponseWriter, r *http.Request, ns string, rec *record) error {
	if err := checkIfMatch(r, rec.ETag); err != nil {
		return err
	}
	cfg, err := readDoc(r)
	if err != nil {
		return err
	}
	if cfg.Name != "DistributionConfig" {
		return malformed(errMissing("DistributionConfig"))
	}
	if err := h.validateDistribution(ns, cfg); err != nil {
		return err
	}
	if cfg.text("CallerReference") != rec.config().text("CallerReference") {
		return errf(http.StatusBadRequest, "IllegalUpdate", "The CallerReference of a distribution can't be changed.")
	}
	normalizeDistribution(cfg)
	rec.Config = cfg.String()
	h.touch(rec)
	if err := h.save(ns, "distribution", rec, false); err != nil {
		return err
	}
	writeDoc(w, http.StatusOK, h.distributionDoc(rec), rec.ETag)
	return nil
}

func (h *Handler) deleteDistribution(w http.ResponseWriter, r *http.Request, ns string, rec *record) error {
	if err := checkIfMatch(r, rec.ETag); err != nil {
		return err
	}
	if rec.config().bool("Enabled") || h.status(rec) != "Deployed" {
		return errf(http.StatusConflict, "DistributionNotDisabled",
			"The distribution you are trying to delete has not been disabled.")
	}
	for _, inv := range h.all(ns, "invalidation") {
		if inv.Distribution == rec.ID {
			h.Store.Delete(inv.ID, service, "invalidation", ns)
		}
	}
	if err := h.Store.Delete(rec.ID, service, "distribution", ns); err != nil {
		return err
	}
	h.cache.drop(rec.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "missing element " + string(e) }

// validateDistribution makes the checks AWS makes that a user is likely
// to trip over: dangling origin, policy and OAC references, and behaviours
// with neither a cache policy nor legacy forwarded values.
func (h *Handler) validateDistribution(ns string, cfg *node) error {
	if cfg.text("CallerReference") == "" {
		return invalidArg("The parameter CallerReference is required.")
	}
	if cfg.child("Enabled") == nil {
		return invalidArg("The parameter Enabled is required.")
	}
	origins := cfg.items("Origins")
	if len(origins) == 0 {
		return invalidArg("The parameter Origins must contain at least one origin.")
	}
	targets := map[string]bool{}
	for _, o := range origins {
		if o.text("Id") == "" || o.text("DomainName") == "" {
			return invalidArg("Each origin needs an Id and a DomainName.")
		}
		if targets[o.text("Id")] {
			return invalidArg("The parameter origin Id %s is used by more than one origin.", o.text("Id"))
		}
		targets[o.text("Id")] = true
		if oac := o.text("OriginAccessControlId"); oac != "" {
			if _, err := h.load(ns, "oac", oac); err != nil {
				return errf(http.StatusNotFound, "NoSuchOriginAccessControl", "The origin access control %s does not exist.", oac)
			}
		}
	}
	for _, g := range cfg.items("OriginGroups") {
		targets[g.text("Id")] = true
	}
	behaviours := []*node{cfg.child("DefaultCacheBehavior")}
	if behaviours[0] == nil {
		return invalidArg("The parameter DefaultCacheBehavior is required.")
	}
	behaviours = append(behaviours, cfg.items("CacheBehaviors")...)
	for _, b := range behaviours {
		if !targets[b.text("TargetOriginId")] {
			return errf(http.StatusNotFound, "NoSuchOrigin", "One or more of your origins or origin groups do not exist.")
		}
		cp := b.text("CachePolicyId")
		if cp == "" && b.child("ForwardedValues") == nil {
			return invalidArg("The parameter ForwardedValues is required.")
		}
		if cp != "" && h.findPolicy(ns, cachePolicyKind, cp) == nil {
			return errf(http.StatusNotFound, "NoSuchCachePolicy", "The cache policy does not exist.")
		}
		if orp := b.text("OriginRequestPolicyId"); orp != "" && h.findPolicy(ns, originRequestPolicyKind, orp) == nil {
			return errf(http.StatusNotFound, "NoSuchOriginRequestPolicy", "The origin request policy does not exist.")
		}
	}
	return nil
}

// normalizeDistribution adds what AWS fills in when a client leaves it out.
func normalizeDistribution(cfg *node) {
	for _, name := range []string{"Aliases", "OriginGroups", "CacheBehaviors", "CustomErrorResponses"} {
		cfg.ensure(list(name))
	}
	cfg.ensure(leaf("DefaultRootObject", ""))
	cfg.ensure(leaf("Comment", ""))
	cfg.ensure(elem("Logging", leaf("Enabled", "false"), leaf("IncludeCookies", "false"), leaf("Bucket", ""), leaf("Prefix", "")))
	cfg.ensure(leaf("PriceClass", "PriceClass_All"))
	vc := cfg.ensure(elem("ViewerCertificate", leaf("CloudFrontDefaultCertificate", "true")))
	if vc.bool("CloudFrontDefaultCertificate") {
		vc.ensure(leaf("SSLSupportMethod", "vip"))
		vc.ensure(leaf("MinimumProtocolVersion", "TLSv1"))
		vc.ensure(leaf("CertificateSource", "cloudfront"))
	}
	cfg.ensure(elem("Restrictions", elem("GeoRestriction", leaf("RestrictionType", "none"), leaf("Quantity", "0"))))
	cfg.ensure(leaf("WebACLId", ""))
	cfg.ensure(leaf("HttpVersion", "http2"))
	cfg.ensure(leaf("IsIPV6Enabled", "true"))
	cfg.ensure(leaf("ContinuousDeploymentPolicyId", ""))
	cfg.ensure(leaf("Staging", "false"))

	for _, o := range cfg.items("Origins") {
		o.ensure(leaf("OriginPath", ""))
		o.ensure(list("CustomHeaders"))
		o.ensure(leaf("ConnectionAttempts", "3"))
		o.ensure(leaf("ConnectionTimeout", "10"))
		o.ensure(elem("OriginShield", leaf("Enabled", "false")))
		if c := o.child("CustomOriginConfig"); c != nil {
			c.ensure(leaf("OriginReadTimeout", "30"))
			c.ensure(leaf("OriginKeepaliveTimeout", "5"))
		} else if o.child("S3OriginConfig") == nil && o.child("VpcOriginConfig") == nil {
			o.Children = append(o.Children, elem("S3OriginConfig", leaf("OriginAccessIdentity", "")))
		}
	}
	behaviours := append([]*node{cfg.child("DefaultCacheBehavior")}, cfg.items("CacheBehaviors")...)
	for _, b := range behaviours {
		b.ensure(elem("TrustedSigners", leaf("Enabled", "false"), leaf("Quantity", "0")))
		b.ensure(elem("TrustedKeyGroups", leaf("Enabled", "false"), leaf("Quantity", "0")))
		am := b.ensure(elem("AllowedMethods", leaf("Quantity", "2"), elem("Items", leaf("Method", "HEAD"), leaf("Method", "GET"))))
		am.ensure(elem("CachedMethods", leaf("Quantity", "2"), elem("Items", leaf("Method", "HEAD"), leaf("Method", "GET"))))
		b.ensure(leaf("SmoothStreaming", "false"))
		b.ensure(leaf("Compress", "false"))
		b.ensure(list("LambdaFunctionAssociations"))
		b.ensure(list("FunctionAssociations"))
		b.ensure(leaf("FieldLevelEncryptionId", ""))
	}
}

// distributionExists looks across tenants.
func (h *Handler) distributionExists(id string) bool {
	_, _, err := h.lookupDistribution(id)
	return err == nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
