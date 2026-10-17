// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"opensnack/internal/util"
)

//
// REPLICATION  (PUT/GET/DELETE /:bucket?replication)
//
// The configuration is stored as sent. New objects and, where a rule asks
// for it, delete markers are copied to the destination bucket as they are
// written, keeping their version IDs; the source version reports
// x-amz-replication-status COMPLETED (or FAILED) and the copy REPLICA.
// Existing objects, version deletes and changes to a replica are not
// replicated, as in S3. All buckets share one namespace, so a destination
// in another region (or account ID) is just another bucket here.
//

type ReplicationConfiguration struct {
	XMLName struct{}          `xml:"ReplicationConfiguration" json:"-"`
	Xmlns   string            `xml:"xmlns,attr,omitempty" json:"-"`
	Role    string            `xml:"Role" json:"role"`
	Rules   []ReplicationRule `xml:"Rule" json:"rules"`
}

type ReplicationRule struct {
	ID                        string                   `xml:"ID,omitempty" json:"id,omitempty"`
	Priority                  *int32                   `xml:"Priority" json:"priority,omitempty"`
	Prefix                    *string                  `xml:"Prefix" json:"prefix,omitempty"` // the V1 rule-level filter
	Filter                    *ReplicationFilter       `xml:"Filter" json:"filter,omitempty"`
	Status                    string                   `xml:"Status" json:"status"`
	SourceSelectionCriteria   *SourceSelectionCriteria `xml:"SourceSelectionCriteria" json:"source_selection_criteria,omitempty"`
	ExistingObjectReplication *replicationStatus       `xml:"ExistingObjectReplication" json:"existing_object_replication,omitempty"`
	Destination               ReplicationDestination   `xml:"Destination" json:"destination"`
	DeleteMarkerReplication   *replicationStatus       `xml:"DeleteMarkerReplication" json:"delete_marker_replication,omitempty"`
}

type replicationStatus struct {
	Status string `xml:"Status" json:"status"`
}

type ReplicationFilter struct {
	Prefix *string `xml:"Prefix" json:"prefix,omitempty"`
	Tag    *Tag    `xml:"Tag" json:"tag,omitempty"`
	And    *struct {
		Prefix *string `xml:"Prefix" json:"prefix,omitempty"`
		Tags   []Tag   `xml:"Tag" json:"tags,omitempty"`
	} `xml:"And" json:"and,omitempty"`
}

type SourceSelectionCriteria struct {
	SseKmsEncryptedObjects *replicationStatus `xml:"SseKmsEncryptedObjects" json:"sse_kms_encrypted_objects,omitempty"`
	ReplicaModifications   *replicationStatus `xml:"ReplicaModifications" json:"replica_modifications,omitempty"`
}

type ReplicationDestination struct {
	Bucket                   string  `xml:"Bucket" json:"bucket"`
	Account                  *string `xml:"Account" json:"account,omitempty"`
	StorageClass             *string `xml:"StorageClass" json:"storage_class,omitempty"`
	AccessControlTranslation *struct {
		Owner string `xml:"Owner" json:"owner"`
	} `xml:"AccessControlTranslation" json:"access_control_translation,omitempty"`
	EncryptionConfiguration *struct {
		ReplicaKmsKeyID *string `xml:"ReplicaKmsKeyID" json:"replica_kms_key_id,omitempty"`
	} `xml:"EncryptionConfiguration" json:"encryption_configuration,omitempty"`
	ReplicationTime *struct {
		Status string `xml:"Status" json:"status"`
		Time   *struct {
			Minutes *int32 `xml:"Minutes" json:"minutes,omitempty"`
		} `xml:"Time" json:"time,omitempty"`
	} `xml:"ReplicationTime" json:"replication_time,omitempty"`
	Metrics *struct {
		Status         string `xml:"Status" json:"status"`
		EventThreshold *struct {
			Minutes *int32 `xml:"Minutes" json:"minutes,omitempty"`
		} `xml:"EventThreshold" json:"event_threshold,omitempty"`
	} `xml:"Metrics" json:"metrics,omitempty"`
}

const replicationStatusHeader = "X-Amz-Replication-Status"

func validStatus(s *replicationStatus) bool {
	return s == nil || s.Status == "Enabled" || s.Status == "Disabled"
}

// destinationBucket is the bucket name in a destination ARN.
func destinationBucket(arn string) string {
	name, ok := strings.CutPrefix(arn, "arn:aws:s3:::")
	if !ok || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return name
}

func (h *Handler) PutBucketReplication(w http.ResponseWriter, r *http.Request) {
	var in ReplicationConfiguration
	if !readXML(w, r, &in) {
		return
	}
	if in.Role == "" || len(in.Rules) == 0 || len(in.Rules) > 1000 {
		malformedXML(w)
		return
	}
	ids, priorities := map[string]bool{}, map[int32]bool{}
	for i := range in.Rules {
		rule := &in.Rules[i]
		if rule.ID == "" {
			rule.ID = randomRuleID()
		}
		msg := ""
		switch {
		case len(rule.ID) > 255:
			msg = "ID length should not exceed allowed limit of 255"
		case ids[rule.ID]:
			msg = "Rule Id must be unique"
		case rule.Status != "Enabled" && rule.Status != "Disabled":
			msg = "Invalid status: " + rule.Status
		case rule.Prefix != nil && rule.Filter != nil:
			msg = "Filter and Prefix cannot be used together in a replication rule"
		case rule.Filter != nil && rule.DeleteMarkerReplication == nil:
			msg = "DeleteMarkerReplication must be specified for this version of Cross Region Replication configuration schema."
		case rule.Filter != nil && rule.Priority != nil && priorities[*rule.Priority]:
			msg = "Found duplicate priority " + strconv.Itoa(int(*rule.Priority)) + " when priorities should be unique for all rules."
		case !validStatus(rule.DeleteMarkerReplication) || !validStatus(rule.ExistingObjectReplication):
			msg = "Invalid status in replication rule " + rule.ID
		case destinationBucket(rule.Destination.Bucket) == "":
			msg = "Invalid bucket ARN: " + rule.Destination.Bucket
		}
		if f := rule.Filter; msg == "" && f != nil {
			set := 0
			for _, b := range []bool{f.Prefix != nil, f.Tag != nil, f.And != nil} {
				if b {
					set++
				}
			}
			if set > 1 {
				msg = "Filter can only have one of Prefix, Tag or And"
			}
		}
		if msg != "" {
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest", msg, "")
			return
		}
		ids[rule.ID] = true
		if rule.Priority != nil {
			priorities[*rule.Priority] = true
		}
	}

	srcName, _ := extractBucketKey(r.URL.Path)
	gns := util.GlobalNamespace(r)
	for _, rule := range in.Rules {
		dest := destinationBucket(rule.Destination.Bucket)
		_, dmeta, err := h.loadBucket(gns, dest)
		switch {
		case err != nil:
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "Destination bucket must exist.", "")
			return
		case dmeta.Versioning != "Enabled":
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "Destination bucket must have versioning enabled.", "")
			return
		case dest == srcName:
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "Destination bucket cannot be the same as the source bucket.", "")
			return
		}
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool {
		if m.Versioning != "Enabled" {
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest",
				"Versioning must be 'Enabled' on the bucket to apply a replication configuration", "")
			return false
		}
		m.Replication = &in
		return true
	}) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetBucketReplication(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.Replication == nil {
		notConfigured(w, meta.Name, "ReplicationConfigurationNotFoundError",
			"The replication configuration was not found")
		return
	}
	out := *meta.Replication
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketReplication(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Replication = nil; return true }) {
		noContent(w)
	}
}

// matches reports whether an object with this key and these tags is in the
// rule's scope.
func (rule *ReplicationRule) matches(key string, tags []Tag) bool {
	has := func(t Tag) bool {
		for _, o := range tags {
			if o == t {
				return true
			}
		}
		return false
	}
	switch f := rule.Filter; {
	case rule.Prefix != nil:
		return strings.HasPrefix(key, *rule.Prefix)
	case f == nil:
		return true
	case f.Prefix != nil:
		return strings.HasPrefix(key, *f.Prefix)
	case f.Tag != nil:
		return has(*f.Tag)
	case f.And != nil:
		if f.And.Prefix != nil && !strings.HasPrefix(key, *f.And.Prefix) {
			return false
		}
		for _, t := range f.And.Tags {
			if !has(t) {
				return false
			}
		}
	}
	return true
}

// tagFiltered reports whether the rule selects objects by tag, which rules
// that replicate delete markers can't.
func (rule *ReplicationRule) tagFiltered() bool {
	f := rule.Filter
	return f != nil && (f.Tag != nil || (f.And != nil && len(f.And.Tags) > 0))
}

// replicationRule is the enabled rule that applies to an object, the one
// with the highest priority when several match.
func (m *bucketMeta) replicationRule(key string, tags []Tag) *ReplicationRule {
	if m.Replication == nil {
		return nil
	}
	var match []*ReplicationRule
	for i := range m.Replication.Rules {
		rule := &m.Replication.Rules[i]
		if rule.Status == "Enabled" && rule.matches(key, tags) {
			match = append(match, rule)
		}
	}
	if len(match) == 0 {
		return nil
	}
	sort.SliceStable(match, func(i, j int) bool { return priority(match[i]) > priority(match[j]) })
	return match[0]
}

func priority(rule *ReplicationRule) int32 {
	if rule.Priority == nil {
		return 0
	}
	return *rule.Priority
}

// prepareReplication marks an object about to be written to a bucket with
// its replication status, returning the rule to replicate it under.
func (h *Handler) prepareReplication(ns string, bucket *bucketMeta, m *objectMeta) *ReplicationRule {
	rule := bucket.replicationRule(m.Key, m.Tags)
	if rule == nil {
		return nil
	}
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	_, dest, err := h.loadBucket(ns, destinationBucket(rule.Destination.Bucket))
	if err != nil || dest.Versioning != "Enabled" {
		m.Headers[replicationStatusHeader] = "FAILED"
		return nil
	}
	m.Headers[replicationStatusHeader] = "COMPLETED"
	return rule
}

// replicateObject copies a version just written to the rule's destination,
// with the same version ID.
func (h *Handler) replicateObject(ns string, rule *ReplicationRule, src *objectMeta) {
	if rule == nil {
		return
	}
	_, dest, err := h.loadBucket(ns, destinationBucket(rule.Destination.Bucket))
	if err != nil {
		return
	}
	body, err := os.ReadFile(blobPath(ns, src.Blob))
	if err != nil {
		return
	}
	blob, err := writeBlob(ns, body)
	if err != nil {
		return
	}
	cp := *src
	cp.Bucket, cp.Blob = dest.Name, blob
	cp.Headers = map[string]string{}
	for k, v := range src.Headers {
		cp.Headers[k] = v
	}
	cp.Headers[replicationStatusHeader] = "REPLICA"
	if sc := rule.Destination.StorageClass; sc != nil && *sc != "" && *sc != "STANDARD" {
		cp.Headers["X-Amz-Storage-Class"] = *sc
	}
	if err := h.storeObject(ns, dest.Versioning, &cp, false); err != nil {
		removeBlob(ns, blob)
	}
}

// replicateDeleteMarker adds the delete marker just written for key to the
// destination of the rule covering it, when the rule replicates markers.
func (h *Handler) replicateDeleteMarker(ns string, bucket *bucketMeta, key string, res deleteResult) {
	if !res.DeleteMarker {
		return
	}
	rule := bucket.replicationRule(key, nil)
	if rule == nil || rule.tagFiltered() || rule.DeleteMarkerReplication == nil ||
		rule.DeleteMarkerReplication.Status != "Enabled" {
		return
	}
	_, dest, err := h.loadBucket(ns, destinationBucket(rule.Destination.Bucket))
	if err != nil {
		return
	}
	_, _ = h.deleteKey(ns, dest.Versioning, dest.Name, key, res.VersionID)
}
