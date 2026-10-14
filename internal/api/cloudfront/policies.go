// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"net/http"
	"sort"
)

// policyKind describes the config-only resources that share one shape:
// origin access controls, cache policies and origin request policies.
type policyKind struct {
	typ       string // store type
	config    string // config element, e.g. CachePolicyConfig
	wrapper   string // response element, e.g. CachePolicy
	listElem  string
	summary   string
	newID     func() string
	modified  bool // the wrapper carries LastModifiedTime
	typed     bool // list entries are {Type, wrapper} (managed or custom)
	updateCfg bool // updates go to /{id}/config rather than /{id}
	notFound  string
	exists    string
	inUse     string
	// refField is the element that refers to it from origins (OAC) or
	// cache behaviours (policies).
	refField string
	managed  []managedPolicy
	validate func(cfg *node) error
}

var (
	oacKind = &policyKind{
		typ: "oac", config: "OriginAccessControlConfig", wrapper: "OriginAccessControl",
		listElem: "OriginAccessControlList", summary: "OriginAccessControlSummary",
		newID: func() string { return randomID("E", 13) }, updateCfg: true,
		notFound: "NoSuchOriginAccessControl", exists: "OriginAccessControlAlreadyExists",
		inUse: "OriginAccessControlInUse", refField: "OriginAccessControlId",
		validate: validateOAC,
	}
	cachePolicyKind = &policyKind{
		typ: "cachepolicy", config: "CachePolicyConfig", wrapper: "CachePolicy",
		listElem: "CachePolicyList", summary: "CachePolicySummary",
		newID: uuid, modified: true, typed: true,
		notFound: "NoSuchCachePolicy", exists: "CachePolicyAlreadyExists",
		inUse: "CachePolicyInUse", refField: "CachePolicyId",
		managed: managedCachePolicies, validate: validateCachePolicy,
	}
	originRequestPolicyKind = &policyKind{
		typ: "originrequestpolicy", config: "OriginRequestPolicyConfig", wrapper: "OriginRequestPolicy",
		listElem: "OriginRequestPolicyList", summary: "OriginRequestPolicySummary",
		newID: uuid, modified: true, typed: true,
		notFound: "NoSuchOriginRequestPolicy", exists: "OriginRequestPolicyAlreadyExists",
		inUse: "OriginRequestPolicyInUse", refField: "OriginRequestPolicyId",
		managed: managedOriginRequestPolicies,
	}
)

func (h *Handler) policies(w http.ResponseWriter, r *http.Request, ns string, k *policyKind, segs []string) error {
	switch {
	case len(segs) == 0 && r.Method == http.MethodGet:
		return h.listPolicies(w, r, ns, k)
	case len(segs) == 0 && r.Method == http.MethodPost:
		return h.createPolicy(w, r, ns, k)
	case len(segs) == 0:
		return errf(http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
	}
	rec := h.findPolicy(ns, k, segs[0])
	if rec == nil {
		return errf(http.StatusNotFound, k.notFound, "The specified %s does not exist.", k.wrapper)
	}
	managed := rec.AccountID == "managed"
	cfgPath := len(segs) == 2 && segs[1] == "config"
	switch {
	case len(segs) == 1 && r.Method == http.MethodGet:
		writeDoc(w, http.StatusOK, k.doc(rec), rec.ETag)
		return nil
	case cfgPath && r.Method == http.MethodGet:
		writeDoc(w, http.StatusOK, rec.config(), rec.ETag)
		return nil
	case r.Method == http.MethodPut && (cfgPath == k.updateCfg) && len(segs) <= 2:
		if managed {
			return errf(http.StatusBadRequest, "IllegalUpdate", "Managed policies can't be updated.")
		}
		return h.updatePolicy(w, r, ns, k, rec)
	case len(segs) == 1 && r.Method == http.MethodDelete:
		if managed {
			return errf(http.StatusBadRequest, "IllegalDelete", "Managed policies can't be deleted.")
		}
		return h.deletePolicy(w, r, ns, k, rec)
	}
	return errf(http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
}

func (k *policyKind) doc(rec *record) *node {
	n := elem(k.wrapper, leaf("Id", rec.ID))
	if k.modified {
		n.Children = append(n.Children, leaf("LastModifiedTime", rec.Modified))
	}
	n.Children = append(n.Children, rec.config())
	return n
}

// findPolicy returns a tenant's policy or an AWS managed one.
func (h *Handler) findPolicy(ns string, k *policyKind, id string) *record {
	for _, m := range k.managed {
		if m.id == id {
			return m.record()
		}
	}
	rec, err := h.load(ns, k.typ, id)
	if err != nil {
		return nil
	}
	return rec
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request, ns string, k *policyKind) error {
	filter := r.URL.Query().Get("Type")
	var recs []*record
	if filter == "" || filter == "managed" {
		for _, m := range k.managed {
			recs = append(recs, m.record())
		}
	}
	if filter == "" || filter == "custom" {
		custom := h.all(ns, k.typ)
		sort.Slice(custom, func(i, j int) bool { return custom[i].ID < custom[j].ID })
		recs = append(recs, custom...)
	}
	items := elem("Items")
	for _, rec := range recs {
		if k.typed {
			typ := "custom"
			if rec.AccountID == "managed" {
				typ = "managed"
			}
			items.Children = append(items.Children, elem(k.summary, leaf("Type", typ), k.doc(rec)))
			continue
		}
		s := elem(k.summary, leaf("Id", rec.ID))
		s.Children = append(s.Children, rec.config().Children...)
		items.Children = append(items.Children, s)
	}
	doc := elem(k.listElem)
	if !k.typed {
		doc.Children = append(doc.Children, leaf("Marker", ""), leaf("IsTruncated", "false"))
	}
	doc.Children = append(doc.Children, leaf("MaxItems", "100"), leaf("Quantity", itoa(len(recs))))
	if len(recs) > 0 {
		doc.Children = append(doc.Children, items)
	}
	writeDoc(w, http.StatusOK, doc, "")
	return nil
}

func (h *Handler) checkName(ns string, k *policyKind, cfg *node, self string) error {
	name := cfg.text("Name")
	if name == "" {
		return invalidArg("The parameter Name is required.")
	}
	for _, m := range k.managed {
		if m.record().config().text("Name") == name {
			return errf(http.StatusConflict, k.exists, "A %s with this name already exists.", k.wrapper)
		}
	}
	for _, other := range h.all(ns, k.typ) {
		if other.ID != self && other.config().text("Name") == name {
			return errf(http.StatusConflict, k.exists, "A %s with this name already exists.", k.wrapper)
		}
	}
	return nil
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request, ns string, k *policyKind) error {
	cfg, err := readDoc(r)
	if err != nil {
		return err
	}
	if cfg.Name != k.config {
		return malformed(errMissing(k.config))
	}
	if err := h.checkName(ns, k, cfg, ""); err != nil {
		return err
	}
	if k.validate != nil {
		if err := k.validate(cfg); err != nil {
			return err
		}
	}
	rec := &record{ID: k.newID(), Config: cfg.String(), ETag: newETag(), Modified: h.timestamp()}
	if err := h.save(ns, k.typ, rec, true); err != nil {
		return err
	}
	path := map[string]string{"oac": "origin-access-control", "cachepolicy": "cache-policy", "originrequestpolicy": "origin-request-policy"}[k.typ]
	w.Header().Set("Location", "https://cloudfront.amazonaws.com"+apiVersion+path+"/"+rec.ID)
	writeDoc(w, http.StatusCreated, k.doc(rec), rec.ETag)
	return nil
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request, ns string, k *policyKind, rec *record) error {
	if err := checkIfMatch(r, rec.ETag); err != nil {
		return err
	}
	cfg, err := readDoc(r)
	if err != nil {
		return err
	}
	if cfg.Name != k.config {
		return malformed(errMissing(k.config))
	}
	if err := h.checkName(ns, k, cfg, rec.ID); err != nil {
		return err
	}
	if k.validate != nil {
		if err := k.validate(cfg); err != nil {
			return err
		}
	}
	rec.Config, rec.ETag, rec.Modified = cfg.String(), newETag(), h.timestamp()
	if err := h.save(ns, k.typ, rec, false); err != nil {
		return err
	}
	writeDoc(w, http.StatusOK, k.doc(rec), rec.ETag)
	return nil
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request, ns string, k *policyKind, rec *record) error {
	if err := checkIfMatch(r, rec.ETag); err != nil {
		return err
	}
	for _, d := range h.all(ns, "distribution") {
		cfg := d.config()
		refs := cfg.items("Origins")
		if k.refField != "OriginAccessControlId" {
			refs = append([]*node{cfg.child("DefaultCacheBehavior")}, cfg.items("CacheBehaviors")...)
		}
		for _, n := range refs {
			if n.text(k.refField) == rec.ID {
				return errf(http.StatusConflict, k.inUse, "The %s is in use by distribution %s.", k.wrapper, d.ID)
			}
		}
	}
	if err := h.Store.Delete(rec.ID, service, k.typ, ns); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func validateOAC(cfg *node) error {
	if p := cfg.text("SigningProtocol"); p != "sigv4" {
		return invalidArg("The parameter SigningProtocol must be sigv4.")
	}
	switch cfg.text("SigningBehavior") {
	case "never", "always", "no-override":
	default:
		return invalidArg("The parameter SigningBehavior must be never, always or no-override.")
	}
	switch cfg.text("OriginAccessControlOriginType") {
	case "s3", "mediastore", "lambda", "mediapackagev2":
	default:
		return invalidArg("The parameter OriginAccessControlOriginType is not valid.")
	}
	return nil
}

func validateCachePolicy(cfg *node) error {
	min, _ := cfg.int64("MinTTL")
	def, hasDef := cfg.int64("DefaultTTL")
	max, hasMax := cfg.int64("MaxTTL")
	if hasDef && def < min || hasMax && hasDef && max < def {
		return invalidArg("The TTLs must satisfy MinTTL <= DefaultTTL <= MaxTTL.")
	}
	return nil
}
