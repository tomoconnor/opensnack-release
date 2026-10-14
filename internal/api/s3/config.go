// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"opensnack/internal/util"
)

//
// Bucket sub-resource configurations: tagging, encryption, public access
// block, ownership controls, CORS, website, and the read-only defaults the
// provider reads on every aws_s3_bucket refresh.
//

type Tag struct {
	Key   string `xml:"Key" json:"key"`
	Value string `xml:"Value" json:"value"`
}

type Tagging struct {
	XMLName struct{} `xml:"Tagging" json:"-"`
	Xmlns   string   `xml:"xmlns,attr,omitempty" json:"-"`
	TagSet  []Tag    `xml:"TagSet>Tag"`
}

type ServerSideEncryptionConfiguration struct {
	XMLName struct{}            `xml:"ServerSideEncryptionConfiguration" json:"-"`
	Xmlns   string              `xml:"xmlns,attr,omitempty" json:"-"`
	Rules   []ServerSideEncRule `xml:"Rule" json:"rules"`
}

type ServerSideEncRule struct {
	Default          *SSEDefault `xml:"ApplyServerSideEncryptionByDefault" json:"default,omitempty"`
	BucketKeyEnabled bool        `xml:"BucketKeyEnabled" json:"bucket_key_enabled"`
}

type SSEDefault struct {
	SSEAlgorithm   string `xml:"SSEAlgorithm" json:"sse_algorithm"`
	KMSMasterKeyID string `xml:"KMSMasterKeyID,omitempty" json:"kms_master_key_id,omitempty"`
}

type PublicAccessBlockConfiguration struct {
	XMLName               struct{} `xml:"PublicAccessBlockConfiguration" json:"-"`
	Xmlns                 string   `xml:"xmlns,attr,omitempty" json:"-"`
	BlockPublicAcls       bool     `xml:"BlockPublicAcls" json:"block_public_acls"`
	IgnorePublicAcls      bool     `xml:"IgnorePublicAcls" json:"ignore_public_acls"`
	BlockPublicPolicy     bool     `xml:"BlockPublicPolicy" json:"block_public_policy"`
	RestrictPublicBuckets bool     `xml:"RestrictPublicBuckets" json:"restrict_public_buckets"`
}

type OwnershipControls struct {
	XMLName struct{} `xml:"OwnershipControls" json:"-"`
	Xmlns   string   `xml:"xmlns,attr,omitempty" json:"-"`
	Rules   []struct {
		ObjectOwnership string `xml:"ObjectOwnership" json:"object_ownership"`
	} `xml:"Rule" json:"rules"`
}

type CORSConfiguration struct {
	XMLName struct{}   `xml:"CORSConfiguration" json:"-"`
	Xmlns   string     `xml:"xmlns,attr,omitempty" json:"-"`
	Rules   []CORSRule `xml:"CORSRule" json:"rules"`
}

type CORSRule struct {
	ID             string   `xml:"ID,omitempty" json:"id,omitempty"`
	AllowedHeaders []string `xml:"AllowedHeader" json:"allowed_headers,omitempty"`
	AllowedMethods []string `xml:"AllowedMethod" json:"allowed_methods"`
	AllowedOrigins []string `xml:"AllowedOrigin" json:"allowed_origins"`
	ExposeHeaders  []string `xml:"ExposeHeader" json:"expose_headers,omitempty"`
	MaxAgeSeconds  *int32   `xml:"MaxAgeSeconds,omitempty" json:"max_age_seconds,omitempty"`
}

type WebsiteConfiguration struct {
	XMLName       struct{} `xml:"WebsiteConfiguration" json:"-"`
	Xmlns         string   `xml:"xmlns,attr,omitempty" json:"-"`
	ErrorDocument *struct {
		Key string `xml:"Key" json:"key"`
	} `xml:"ErrorDocument,omitempty" json:"error_document,omitempty"`
	IndexDocument *struct {
		Suffix string `xml:"Suffix" json:"suffix"`
	} `xml:"IndexDocument,omitempty" json:"index_document,omitempty"`
	RedirectAllRequestsTo *struct {
		HostName string `xml:"HostName" json:"host_name"`
		Protocol string `xml:"Protocol,omitempty" json:"protocol,omitempty"`
	} `xml:"RedirectAllRequestsTo,omitempty" json:"redirect_all_requests_to,omitempty"`
	RoutingRules []RoutingRule `xml:"RoutingRules>RoutingRule,omitempty" json:"routing_rules,omitempty"`
}

type RoutingRule struct {
	Condition *struct {
		HttpErrorCodeReturnedEquals *string `xml:"HttpErrorCodeReturnedEquals" json:"http_error_code_returned_equals,omitempty"`
		KeyPrefixEquals             *string `xml:"KeyPrefixEquals" json:"key_prefix_equals,omitempty"`
	} `xml:"Condition,omitempty" json:"condition,omitempty"`
	Redirect struct {
		HostName             *string `xml:"HostName" json:"host_name,omitempty"`
		HttpRedirectCode     *string `xml:"HttpRedirectCode" json:"http_redirect_code,omitempty"`
		Protocol             *string `xml:"Protocol" json:"protocol,omitempty"`
		ReplaceKeyPrefixWith *string `xml:"ReplaceKeyPrefixWith" json:"replace_key_prefix_with,omitempty"`
		ReplaceKeyWith       *string `xml:"ReplaceKeyWith" json:"replace_key_with,omitempty"`
	} `xml:"Redirect" json:"redirect"`
}

// readXML decodes the request body into v, answering MalformedXML itself.
func readXML(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil || xml.Unmarshal(body, v) != nil {
		malformedXML(w)
		return false
	}
	return true
}

func notConfigured(w http.ResponseWriter, bucket, code, msg string) {
	writeS3Error(w, http.StatusNotFound, code, msg, bucket)
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

//
// TAGGING  (PUT/GET/DELETE /:bucket?tagging)
//

func (h *Handler) PutBucketTagging(w http.ResponseWriter, r *http.Request) {
	var in Tagging
	if !readXML(w, r, &in) {
		return
	}
	if code, msg, ok := validateTags(in.TagSet, 50); !ok {
		writeS3Error(w, http.StatusBadRequest, code, msg, "")
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.TagSet = sortedTags(in.TagSet); return true }) {
		noContent(w)
	}
}

func (h *Handler) GetBucketTagging(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if len(meta.TagSet) == 0 {
		notConfigured(w, meta.Name, "NoSuchTagSet", "The TagSet does not exist")
		return
	}
	writeXMLOK(w, Tagging{Xmlns: s3XMLNS, TagSet: sortedTags(meta.TagSet)})
}

func (h *Handler) DeleteBucketTagging(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.TagSet = nil; return true }) {
		noContent(w)
	}
}

//
// ENCRYPTION  (PUT/GET/DELETE /:bucket?encryption)
//
// Every general purpose bucket is encrypted: with no configuration of its
// own, AWS reports (and applies) SSE-S3, so GetBucketEncryption never says
// "not found".
//

var validSSE = map[string]bool{"AES256": true, "aws:kms": true, "aws:kms:dsse": true}

func defaultEncryption() *ServerSideEncryptionConfiguration {
	return &ServerSideEncryptionConfiguration{
		Rules: []ServerSideEncRule{{Default: &SSEDefault{SSEAlgorithm: "AES256"}}},
	}
}

func (m *bucketMeta) encryption() *ServerSideEncryptionConfiguration {
	if m.Encryption != nil && len(m.Encryption.Rules) > 0 {
		return m.Encryption
	}
	return defaultEncryption()
}

func (h *Handler) PutBucketEncryption(w http.ResponseWriter, r *http.Request) {
	var in ServerSideEncryptionConfiguration
	if !readXML(w, r, &in) {
		return
	}
	if len(in.Rules) != 1 || in.Rules[0].Default == nil {
		malformedXML(w)
		return
	}
	d := in.Rules[0].Default
	if !validSSE[d.SSEAlgorithm] {
		invalidArgument(w, "The encryption method specified is not supported")
		return
	}
	if d.KMSMasterKeyID != "" && !strings.HasPrefix(d.SSEAlgorithm, "aws:kms") {
		invalidArgument(w, "a KMSMasterKeyID is not applicable if the default sse algorithm is not aws:kms or aws:kms:dsse")
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Encryption = &in; return true }) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetBucketEncryption(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	out := *meta.encryption()
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketEncryption(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Encryption = nil; return true }) {
		noContent(w)
	}
}

// applyDefaultEncryption fills in the SSE headers an object gets from its
// bucket when the request names none.
func applyDefaultEncryption(headers map[string]string, bucket *bucketMeta, r *http.Request) {
	const algo = "X-Amz-Server-Side-Encryption"
	const keyID = "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"
	const bucketKey = "X-Amz-Server-Side-Encryption-Bucket-Key-Enabled"
	if headers[algo] == "" {
		rule := bucket.encryption().Rules[0]
		headers[algo] = rule.Default.SSEAlgorithm
		if rule.Default.KMSMasterKeyID != "" {
			headers[keyID] = rule.Default.KMSMasterKeyID
		}
		if rule.BucketKeyEnabled && strings.HasPrefix(rule.Default.SSEAlgorithm, "aws:kms") {
			headers[bucketKey] = "true"
		}
	}
	if strings.HasPrefix(headers[algo], "aws:kms") {
		if headers[keyID] == "" {
			// The AWS managed key S3 uses when no key is named.
			headers[keyID] = "arn:aws:kms:" + bucket.region() + ":" + util.AccountID(r) + ":alias/aws/s3"
		} else if !strings.HasPrefix(headers[keyID], "arn:") {
			// Objects report the full key ARN even when given a bare key ID.
			id := headers[keyID]
			if !strings.HasPrefix(id, "alias/") {
				id = "key/" + id
			}
			headers[keyID] = "arn:aws:kms:" + bucket.region() + ":" + util.AccountID(r) + ":" + id
		}
	} else {
		delete(headers, keyID)
		delete(headers, bucketKey)
	}
}

//
// PUBLIC ACCESS BLOCK  (PUT/GET/DELETE /:bucket?publicAccessBlock)
//

func (h *Handler) PutPublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	var in PublicAccessBlockConfiguration
	if !readXML(w, r, &in) {
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.PublicAccessBlock = &in; return true }) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetPublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.PublicAccessBlock == nil {
		notConfigured(w, meta.Name, "NoSuchPublicAccessBlockConfiguration",
			"The public access block configuration was not found")
		return
	}
	out := *meta.PublicAccessBlock
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeletePublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.PublicAccessBlock = nil; return true }) {
		noContent(w)
	}
}

//
// OWNERSHIP CONTROLS  (PUT/GET/DELETE /:bucket?ownershipControls)
//

var validOwnership = map[string]bool{"BucketOwnerPreferred": true, "ObjectWriter": true, "BucketOwnerEnforced": true}

func (h *Handler) PutBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	var in OwnershipControls
	if !readXML(w, r, &in) {
		return
	}
	if len(in.Rules) != 1 || !validOwnership[in.Rules[0].ObjectOwnership] {
		malformedXML(w)
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Ownership = &in; return true }) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.Ownership == nil {
		notConfigured(w, meta.Name, "OwnershipControlsNotFoundError",
			"The bucket ownership controls were not found")
		return
	}
	out := *meta.Ownership
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Ownership = nil; return true }) {
		noContent(w)
	}
}

//
// CORS  (PUT/GET/DELETE /:bucket?cors)
//

var validCORSMethods = map[string]bool{"GET": true, "PUT": true, "POST": true, "DELETE": true, "HEAD": true}

func (h *Handler) PutBucketCors(w http.ResponseWriter, r *http.Request) {
	var in CORSConfiguration
	if !readXML(w, r, &in) {
		return
	}
	if len(in.Rules) == 0 || len(in.Rules) > 100 {
		malformedXML(w)
		return
	}
	for _, rule := range in.Rules {
		if len(rule.AllowedMethods) == 0 || len(rule.AllowedOrigins) == 0 {
			malformedXML(w)
			return
		}
		for _, m := range rule.AllowedMethods {
			if !validCORSMethods[m] {
				invalidArgument(w, "Found unsupported HTTP method in CORS config. Unsupported method is "+m)
				return
			}
		}
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.CORS = &in; return true }) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetBucketCors(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.CORS == nil {
		notConfigured(w, meta.Name, "NoSuchCORSConfiguration", "The CORS configuration does not exist")
		return
	}
	out := *meta.CORS
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketCors(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.CORS = nil; return true }) {
		noContent(w)
	}
}

//
// WEBSITE  (PUT/GET/DELETE /:bucket?website)
//

func (h *Handler) PutBucketWebsite(w http.ResponseWriter, r *http.Request) {
	var in WebsiteConfiguration
	if !readXML(w, r, &in) {
		return
	}
	switch {
	case in.RedirectAllRequestsTo != nil:
		if in.IndexDocument != nil || in.ErrorDocument != nil || len(in.RoutingRules) > 0 {
			invalidArgument(w, "RedirectAllRequestsTo cannot be provided in conjunction with other Routing/Redirect configurations.")
			return
		}
	case in.IndexDocument == nil || in.IndexDocument.Suffix == "":
		invalidArgument(w, "A value for IndexDocument Suffix must be provided if RedirectAllRequestsTo is empty")
		return
	case strings.Contains(in.IndexDocument.Suffix, "/"):
		invalidArgument(w, "The IndexDocument Suffix is not well formed")
		return
	}
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Website = &in; return true }) {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) GetBucketWebsite(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.Website == nil {
		notConfigured(w, meta.Name, "NoSuchWebsiteConfiguration",
			"The specified bucket does not have a website configuration")
		return
	}
	out := *meta.Website
	out.Xmlns = s3XMLNS
	writeXMLOK(w, out)
}

func (h *Handler) DeleteBucketWebsite(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Website = nil; return true }) {
		noContent(w)
	}
}

//
// POLICY STATUS, DELETE POLICY
//

func (h *Handler) GetBucketPolicyStatus(w http.ResponseWriter, r *http.Request) {
	_, meta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	if meta.Policy == "" {
		notConfigured(w, meta.Name, "NoSuchBucketPolicy", "The bucket policy does not exist")
		return
	}
	public := policyIsPublic(meta.Policy)
	if pab := meta.PublicAccessBlock; pab != nil && pab.RestrictPublicBuckets {
		public = false
	}
	writeXMLOK(w, struct {
		XMLName  struct{} `xml:"PolicyStatus"`
		Xmlns    string   `xml:"xmlns,attr"`
		IsPublic bool     `xml:"IsPublic"`
	}{Xmlns: s3XMLNS, IsPublic: public})
}

// policyIsPublic is a rough version of AWS's analysis: an Allow statement for
// the "*" principal with no conditions grants public access.
func policyIsPublic(policy string) bool {
	var doc struct {
		Statement json.RawMessage
	}
	if json.Unmarshal([]byte(policy), &doc) != nil {
		return false
	}
	var stmts []map[string]any
	if json.Unmarshal(doc.Statement, &stmts) != nil {
		var one map[string]any
		if json.Unmarshal(doc.Statement, &one) != nil {
			return false
		}
		stmts = []map[string]any{one}
	}
	for _, s := range stmts {
		if s["Effect"] != "Allow" || s["Condition"] != nil {
			continue
		}
		switch p := s["Principal"].(type) {
		case string:
			if p == "*" {
				return true
			}
		case map[string]any:
			if aws, _ := p["AWS"].(string); aws == "*" {
				return true
			}
		}
	}
	return false
}

func (h *Handler) DeleteBucketPolicy(w http.ResponseWriter, r *http.Request) {
	if h.editBucket(w, r, func(m *bucketMeta) bool { m.Policy = ""; return true }) {
		noContent(w)
	}
}

//
// READ-ONLY DEFAULTS
//
// aws_s3_bucket reads these on every refresh. Opensnack doesn't support
// configuring them, so each reports what an untouched AWS bucket does.
//

func (h *Handler) GetBucketAccelerateConfiguration(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.bucketOr404(w, r); ok {
		writeXMLOK(w, struct {
			XMLName struct{} `xml:"AccelerateConfiguration"`
			Xmlns   string   `xml:"xmlns,attr"`
		}{Xmlns: s3XMLNS})
	}
}

func (h *Handler) GetBucketRequestPayment(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.bucketOr404(w, r); ok {
		writeXMLOK(w, struct {
			XMLName struct{} `xml:"RequestPaymentConfiguration"`
			Xmlns   string   `xml:"xmlns,attr"`
			Payer   string   `xml:"Payer"`
		}{Xmlns: s3XMLNS, Payer: "BucketOwner"})
	}
}

func (h *Handler) GetBucketLogging(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.bucketOr404(w, r); ok {
		writeXMLOK(w, struct {
			XMLName struct{} `xml:"BucketLoggingStatus"`
			Xmlns   string   `xml:"xmlns,attr"`
		}{Xmlns: s3XMLNS})
	}
}

func (h *Handler) GetBucketReplication(w http.ResponseWriter, r *http.Request) {
	if _, meta, ok := h.bucketOr404(w, r); ok {
		notConfigured(w, meta.Name, "ReplicationConfigurationNotFoundError",
			"The replication configuration was not found")
	}
}

func (h *Handler) GetObjectLockConfiguration(w http.ResponseWriter, r *http.Request) {
	if _, meta, ok := h.bucketOr404(w, r); ok {
		notConfigured(w, meta.Name, "ObjectLockConfigurationNotFoundError",
			"Object Lock configuration does not exist for this bucket")
	}
}
