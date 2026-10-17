// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"
	"time"
)

//
// LIFECYCLE  (PUT/GET/DELETE /:bucket?lifecycle)
//
// The configuration is stored as sent and returned unchanged; the provider
// waits after a PUT until GET returns exactly the rules it wrote. Rules are
// not acted on: objects don't expire or change storage class.
//
// The JSON names match the older handler's {"rules":[{"id","status",
// "expiration":{"days"}}]}, so buckets saved by it still load.
//

type LifecycleConfiguration struct {
	XMLName struct{}        `xml:"LifecycleConfiguration" json:"-"`
	Xmlns   string          `xml:"xmlns,attr,omitempty" json:"-"`
	Rules   []LifecycleRule `xml:"Rule" json:"rules"`
	// TransitionDefaultMinimumSize travels in the
	// x-amz-transition-default-minimum-object-size header, not the body.
	TransitionDefaultMinimumSize string `xml:"-" json:"transition_default_minimum_size,omitempty"`
}

type LifecycleRule struct {
	ID     string           `xml:"ID,omitempty" json:"id,omitempty"`
	Status string           `xml:"Status" json:"status"`
	Prefix *string          `xml:"Prefix" json:"prefix,omitempty"` // the deprecated rule-level filter
	Filter *LifecycleFilter `xml:"Filter" json:"filter,omitempty"`

	Expiration                     *LifecycleExpiration            `xml:"Expiration" json:"expiration,omitempty"`
	Transitions                    []LifecycleTransition           `xml:"Transition" json:"transitions,omitempty"`
	NoncurrentVersionExpiration    *NoncurrentVersionExpiration    `xml:"NoncurrentVersionExpiration" json:"noncurrent_version_expiration,omitempty"`
	NoncurrentVersionTransitions   []NoncurrentVersionTransition   `xml:"NoncurrentVersionTransition" json:"noncurrent_version_transitions,omitempty"`
	AbortIncompleteMultipartUpload *AbortIncompleteMultipartUpload `xml:"AbortIncompleteMultipartUpload" json:"abort_incomplete_multipart_upload,omitempty"`
}

type LifecycleFilter struct {
	Prefix                *string       `xml:"Prefix" json:"prefix,omitempty"`
	Tag                   *Tag          `xml:"Tag" json:"tag,omitempty"`
	ObjectSizeGreaterThan *int64        `xml:"ObjectSizeGreaterThan" json:"object_size_greater_than,omitempty"`
	ObjectSizeLessThan    *int64        `xml:"ObjectSizeLessThan" json:"object_size_less_than,omitempty"`
	And                   *LifecycleAnd `xml:"And" json:"and,omitempty"`
}

type LifecycleAnd struct {
	Prefix                *string `xml:"Prefix" json:"prefix,omitempty"`
	Tags                  []Tag   `xml:"Tag" json:"tags,omitempty"`
	ObjectSizeGreaterThan *int64  `xml:"ObjectSizeGreaterThan" json:"object_size_greater_than,omitempty"`
	ObjectSizeLessThan    *int64  `xml:"ObjectSizeLessThan" json:"object_size_less_than,omitempty"`
}

type LifecycleExpiration struct {
	Date                      *string `xml:"Date" json:"date,omitempty"`
	Days                      *int32  `xml:"Days" json:"days,omitempty"`
	ExpiredObjectDeleteMarker *bool   `xml:"ExpiredObjectDeleteMarker" json:"expired_object_delete_marker,omitempty"`
}

type LifecycleTransition struct {
	Date         *string `xml:"Date" json:"date,omitempty"`
	Days         *int32  `xml:"Days" json:"days,omitempty"`
	StorageClass string  `xml:"StorageClass" json:"storage_class"`
}

type NoncurrentVersionExpiration struct {
	NoncurrentDays          *int32 `xml:"NoncurrentDays" json:"noncurrent_days,omitempty"`
	NewerNoncurrentVersions *int32 `xml:"NewerNoncurrentVersions" json:"newer_noncurrent_versions,omitempty"`
}

type NoncurrentVersionTransition struct {
	NoncurrentDays          *int32 `xml:"NoncurrentDays" json:"noncurrent_days,omitempty"`
	NewerNoncurrentVersions *int32 `xml:"NewerNoncurrentVersions" json:"newer_noncurrent_versions,omitempty"`
	StorageClass            string `xml:"StorageClass" json:"storage_class"`
}

type AbortIncompleteMultipartUpload struct {
	DaysAfterInitiation *int32 `xml:"DaysAfterInitiation" json:"days_after_initiation,omitempty"`
}

const defaultTransitionMinimumSize = "all_storage_classes_128K"

var validTransitionSizes = map[string]bool{
	"all_storage_classes_128K": true, "varies_by_storage_class": true,
}

var validTransitionClasses = map[string]bool{
	"GLACIER": true, "STANDARD_IA": true, "ONEZONE_IA": true, "INTELLIGENT_TIERING": true,
	"DEEP_ARCHIVE": true, "GLACIER_IR": true,
}

func (h *Handler) PutBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	var in LifecycleConfiguration
	if !readXML(w, r, &in) {
		return
	}
	in.TransitionDefaultMinimumSize = r.Header.Get("X-Amz-Transition-Default-Minimum-Object-Size")
	if in.TransitionDefaultMinimumSize == "" {
		in.TransitionDefaultMinimumSize = defaultTransitionMinimumSize
	}
	if !validTransitionSizes[in.TransitionDefaultMinimumSize] {
		invalidArgument(w, "Invalid value for x-amz-transition-default-minimum-object-size")
		return
	}
	if len(in.Rules) == 0 || len(in.Rules) > 1000 {
		malformedXML(w)
		return
	}
	ids := map[string]bool{}
	for i := range in.Rules {
		rule := &in.Rules[i]
		if rule.ID == "" {
			rule.ID = randomRuleID()
		}
		if msg := validateLifecycleRule(rule); msg != "" {
			invalidArgument(w, msg)
			return
		}
		if ids[rule.ID] {
			invalidArgument(w, "Rule ID must be unique. Found same ID for more than one rule")
			return
		}
		ids[rule.ID] = true
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Lifecycle = &in; return true }) {
		w.Header().Set("X-Amz-Transition-Default-Minimum-Object-Size", in.TransitionDefaultMinimumSize)
		w.WriteHeader(http.StatusOK)
	}
}

// validateLifecycleRule returns S3's message for a rule it would refuse, or "".
func validateLifecycleRule(rule *LifecycleRule) string {
	switch {
	case len(rule.ID) > 255:
		return "ID length should not exceed allowed limit of 255"
	case rule.Status != "Enabled" && rule.Status != "Disabled":
		return "Invalid status: " + rule.Status
	case rule.Prefix != nil && rule.Filter != nil:
		return "Filter and Prefix cannot be used together in a Lifecycle rule"
	case rule.Expiration == nil && len(rule.Transitions) == 0 && rule.NoncurrentVersionExpiration == nil &&
		len(rule.NoncurrentVersionTransitions) == 0 && rule.AbortIncompleteMultipartUpload == nil:
		return "At least one action needs to be specified in a rule"
	}
	if f := rule.Filter; f != nil {
		set := 0
		for _, b := range []bool{f.Prefix != nil, f.Tag != nil, f.ObjectSizeGreaterThan != nil,
			f.ObjectSizeLessThan != nil, f.And != nil} {
			if b {
				set++
			}
		}
		if set > 1 {
			return "Filter can only have one of Prefix, Tag, ObjectSizeGreaterThan, ObjectSizeLessThan or And"
		}
	}
	if e := rule.Expiration; e != nil {
		set := 0
		for _, b := range []bool{e.Date != nil, e.Days != nil, e.ExpiredObjectDeleteMarker != nil} {
			if b {
				set++
			}
		}
		switch {
		case set != 1:
			return "Expiration must specify exactly one of Date, Days or ExpiredObjectDeleteMarker"
		case e.Days != nil && *e.Days <= 0:
			return "'Days' for Expiration action must be a positive integer"
		case e.Date != nil && !midnightUTC(*e.Date):
			return "'Date' must be at midnight GMT"
		}
	}
	for _, t := range rule.Transitions {
		switch {
		case !validTransitionClasses[t.StorageClass]:
			return "The storage class you specified is not valid"
		case (t.Days == nil) == (t.Date == nil):
			return "Transition must specify exactly one of Date or Days"
		case t.Days != nil && *t.Days < 0:
			return "'Days' for Transition action must be a non-negative integer"
		case t.Date != nil && !midnightUTC(*t.Date):
			return "'Date' must be at midnight GMT"
		}
	}
	if n := rule.NoncurrentVersionExpiration; n != nil && (n.NoncurrentDays == nil || *n.NoncurrentDays <= 0) {
		return "'NoncurrentDays' for NoncurrentVersionExpiration action must be a positive integer"
	}
	for _, t := range rule.NoncurrentVersionTransitions {
		switch {
		case !validTransitionClasses[t.StorageClass]:
			return "The storage class you specified is not valid"
		case t.NoncurrentDays == nil || *t.NoncurrentDays < 0:
			return "'NoncurrentDays' for NoncurrentVersionTransition action must be a non-negative integer"
		}
	}
	if a := rule.AbortIncompleteMultipartUpload; a != nil && (a.DaysAfterInitiation == nil || *a.DaysAfterInitiation <= 0) {
		return "'DaysAfterInitiation' for AbortIncompleteMultipartUpload action must be a positive integer"
	}
	return ""
}

func midnightUTC(s string) bool {
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && t.UTC().Equal(t.UTC().Truncate(24*time.Hour))
}

// randomRuleID is the kind of ID S3 gives a rule sent without one.
func randomRuleID() string {
	b := make([]byte, 30)
	_, _ = rand.Read(b)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

func (h *Handler) GetBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.Lifecycle == nil || len(meta.Lifecycle.Rules) == 0 {
		notConfigured(w, meta.Name, "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist")
		return
	}
	out := *meta.Lifecycle
	out.Xmlns = s3XMLNS
	size := out.TransitionDefaultMinimumSize
	if size == "" {
		size = defaultTransitionMinimumSize
	}
	w.Header().Set("X-Amz-Transition-Default-Minimum-Object-Size", size)
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketLifecycle(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Lifecycle = nil; return true }) {
		noContent(w)
	}
}
