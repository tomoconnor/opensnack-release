// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

//
// VERSION STORAGE
//
// The current version of a key, when it is an object, is the "object" row
// (ID "<bucket>/<key>") that listings, the inspection API and unversioned
// buckets use. Every other version, and every delete marker (current or
// not), is an "objver" row with ID "<bucket>/<key>?versionId=<id>". Each
// version keeps its own blob, so a noncurrent version stays readable.
//
// Versions are ordered by Seq, which only ever grows for a key; the object
// row, when there is one, always holds the highest Seq.
//
// An object written while the bucket was unversioned has an empty VersionID
// and is the "null" version, which is also the ID suspended versioning
// writes.
//

const versionType = "objver"

// errPrecondition is a lost create-only race.
var errPrecondition = errors.New("precondition failed")

func versionRowID(bucket, key, vid string) string {
	return bucket + "/" + key + "?versionId=" + vid
}

func newVersionID() string {
	// Same alphabet and length as AWS version IDs.
	return strings.ToUpper(util.RandomHex(4)) + util.RandomHex(12)
}

// vid is the version ID as clients see it.
func (m *objectMeta) vid() string {
	if m.VersionID == "" {
		return "null"
	}
	return m.VersionID
}

func (m *objectMeta) seq() int64 {
	if m.Seq != 0 {
		return m.Seq
	}
	return m.modTime().UnixNano()
}

type version struct {
	objectMeta
	current bool // stored in the "object" row
}

func decodeMeta(res resource.Resource) (objectMeta, bool) {
	var m objectMeta
	if err := json.Unmarshal(res.Attributes, &m); err != nil {
		return m, false
	}
	return m, true
}

// keyVersions returns every version of a key, newest first.
func (h *Handler) keyVersions(ns, bucket, key string) ([]version, error) {
	var out []version
	if res, err := h.Store.Get(bucket+"/"+key, "s3", "object", ns); err == nil {
		if m, ok := decodeMeta(*res); ok {
			m.Bucket, m.Key = bucket, key
			out = append(out, version{m, true})
		}
	}
	rows, err := h.Store.List("s3", versionType, ns)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if m, ok := decodeMeta(row); ok && m.Bucket == bucket && m.Key == key {
			out = append(out, version{m, false})
		}
	}
	sortVersions(out)
	return out, nil
}

// bucketVersions returns every version in a bucket under prefix, by key and
// then newest first, the order ListObjectVersions uses.
func (h *Handler) bucketVersions(ns, bucket, prefix string) ([]version, error) {
	var out []version
	for _, typ := range []string{"object", versionType} {
		rows, err := h.Store.List("s3", typ, ns)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if !strings.HasPrefix(row.ID, bucket+"/") {
				continue
			}
			m, ok := decodeMeta(row)
			if !ok {
				continue
			}
			if typ == "object" {
				m.Bucket, m.Key = bucket, strings.TrimPrefix(row.ID, bucket+"/")
			}
			if m.Bucket != bucket || !strings.HasPrefix(m.Key, prefix) {
				continue
			}
			out = append(out, version{m, typ == "object"})
		}
	}
	sortVersions(out)
	return out, nil
}

func sortVersions(vs []version) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Key != vs[j].Key {
			return vs[i].Key < vs[j].Key
		}
		if vs[i].current != vs[j].current {
			return vs[i].current
		}
		return vs[i].seq() > vs[j].seq()
	})
}

func nextSeq(vs []version) int64 {
	seq := time.Now().UnixNano()
	for _, v := range vs {
		if s := v.seq(); s >= seq {
			seq = s + 1
		}
	}
	return seq
}

func (h *Handler) writeVersionRow(ns string, m objectMeta) error {
	buf, _ := json.Marshal(m)
	res := &resource.Resource{
		ID: versionRowID(m.Bucket, m.Key, m.vid()), Namespace: ns,
		Service: "s3", Type: versionType, Attributes: buf,
	}
	if err := h.Store.Create(res); err != nil {
		return h.Store.Update(res)
	}
	return nil
}

func (h *Handler) writeCurrentRow(ns string, m objectMeta, exists, createOnly bool) error {
	buf, _ := json.Marshal(m)
	res := &resource.Resource{
		ID: m.Bucket + "/" + m.Key, Namespace: ns,
		Service: "s3", Type: "object", Attributes: buf,
	}
	if exists {
		return h.Store.Update(res)
	}
	if err := h.Store.Create(res); err != nil {
		if createOnly {
			return errPrecondition
		}
		return err
	}
	return nil
}

// dropVersion removes one version's row and bytes.
func (h *Handler) dropVersion(ns string, v version) {
	if v.current {
		_ = h.Store.Delete(v.Bucket+"/"+v.Key, "s3", "object", ns)
	} else {
		_ = h.Store.Delete(versionRowID(v.Bucket, v.Key, v.vid()), "s3", versionType, ns)
	}
	removeBlob(ns, v.Blob)
}

// demote moves the current object row into a version row.
func (h *Handler) demote(ns string, v version) error {
	v.VersionID = v.vid()
	if err := h.writeVersionRow(ns, v.objectMeta); err != nil {
		return err
	}
	return h.Store.Delete(v.Bucket+"/"+v.Key, "s3", "object", ns)
}

// storeObject makes m the current version of its key under the bucket's
// versioning mode, setting m.VersionID and m.Seq. With createOnly, losing
// the race to create the key returns errPrecondition.
func (h *Handler) storeObject(ns, mode string, m *objectMeta, createOnly bool) error {
	defer lockKey(ns, m.Bucket, m.Key)()
	vs, err := h.keyVersions(ns, m.Bucket, m.Key)
	if err != nil {
		return err
	}
	m.Seq = nextSeq(vs)
	switch mode {
	case "Enabled":
		m.VersionID = newVersionID()
	case "Suspended":
		m.VersionID = "null"
	default:
		m.VersionID = ""
	}

	var cur *version
	if len(vs) > 0 && vs[0].current {
		cur = &vs[0]
	}

	var replaced []version // versions whose bytes go once m is in place
	exists := cur != nil
	if cur != nil {
		if mode == "" || (mode == "Suspended" && cur.vid() == "null") {
			replaced = append(replaced, *cur)
		} else {
			if err := h.demote(ns, *cur); err != nil {
				return err
			}
			exists = false
		}
	}
	if mode == "Suspended" {
		for _, v := range vs {
			if !v.current && v.vid() == "null" {
				_ = h.Store.Delete(versionRowID(v.Bucket, v.Key, "null"), "s3", versionType, ns)
				replaced = append(replaced, v)
			}
		}
	}

	if err := h.writeCurrentRow(ns, *m, exists, createOnly); err != nil {
		return err
	}
	for _, v := range replaced {
		removeBlob(ns, v.Blob)
	}
	return nil
}

// deleteResult is what a delete did, for the response headers.
type deleteResult struct {
	VersionID    string
	DeleteMarker bool
}

// deleteKey is DeleteObject without a version ID: it removes the object in
// an unversioned bucket and adds a delete marker in a versioned one.
func (h *Handler) deleteKey(ns, mode, bucket, key string) (deleteResult, error) {
	defer lockKey(ns, bucket, key)()
	vs, err := h.keyVersions(ns, bucket, key)
	if err != nil {
		return deleteResult{}, err
	}
	if mode == "" {
		if len(vs) > 0 && vs[0].current {
			h.dropVersion(ns, vs[0])
		}
		return deleteResult{}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	marker := objectMeta{
		Bucket: bucket, Key: key, DeleteMarker: true,
		Seq: nextSeq(vs), LastModified: now, CreatedAt: now,
		VersionID: "null",
	}
	if mode == "Enabled" {
		marker.VersionID = newVersionID()
	}
	for _, v := range vs {
		switch {
		case mode == "Suspended" && v.vid() == "null":
			h.dropVersion(ns, v)
		case v.current:
			if err := h.demote(ns, v); err != nil {
				return deleteResult{}, err
			}
		}
	}
	if err := h.writeVersionRow(ns, marker); err != nil {
		return deleteResult{}, err
	}
	return deleteResult{VersionID: marker.VersionID, DeleteMarker: true}, nil
}

// deleteVersion permanently removes one version. Deleting a version that
// does not exist is not an error.
func (h *Handler) deleteVersion(ns, bucket, key, vid string) (deleteResult, error) {
	defer lockKey(ns, bucket, key)()
	vs, err := h.keyVersions(ns, bucket, key)
	if err != nil {
		return deleteResult{}, err
	}
	res := deleteResult{VersionID: vid}
	for _, v := range vs {
		if v.vid() != vid {
			continue
		}
		h.dropVersion(ns, v)
		res.DeleteMarker = v.DeleteMarker
		if v.current || len(vs) > 1 {
			return res, h.settle(ns, bucket, key)
		}
		return res, nil
	}
	return res, nil
}

// settle promotes the newest remaining version to the object row when it is
// an object and nothing is current.
func (h *Handler) settle(ns, bucket, key string) error {
	vs, err := h.keyVersions(ns, bucket, key)
	if err != nil || len(vs) == 0 {
		return err
	}
	top := vs[0]
	if top.current || top.DeleteMarker {
		return nil
	}
	if err := h.writeCurrentRow(ns, top.objectMeta, false, false); err != nil {
		return err
	}
	return h.Store.Delete(versionRowID(bucket, key, top.vid()), "s3", versionType, ns)
}

// findVersion looks up a key's version by ID; an empty ID means the latest
// version, which may be a delete marker.
func (h *Handler) findVersion(ns, bucket, key, vid string) (*version, error) {
	vs, err := h.keyVersions(ns, bucket, key)
	if err != nil {
		return nil, err
	}
	for i, v := range vs {
		if vid == "" || v.vid() == vid {
			return &vs[i], nil
		}
	}
	return nil, nil
}

// saveVersion writes back a version's metadata in place (object tagging).
func (h *Handler) saveVersion(ns string, v *version) error {
	if v.current {
		return h.writeCurrentRow(ns, v.objectMeta, true, false)
	}
	return h.writeVersionRow(ns, v.objectMeta)
}

//
// LIST OBJECT VERSIONS
// GET /:bucket?versions
//

type versionEntry struct {
	XMLName      xml.Name
	Key          string `xml:"Key"`
	VersionID    string `xml:"VersionId"`
	IsLatest     bool   `xml:"IsLatest"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag,omitempty"`
	Size         *int64 `xml:"Size,omitempty"`
	StorageClass string `xml:"StorageClass,omitempty"`
	Owner        *Owner `xml:"Owner,omitempty"`
}

type ListVersionsResult struct {
	XMLName             struct{} `xml:"ListVersionsResult"`
	Xmlns               string   `xml:"xmlns,attr"`
	Name                string   `xml:"Name"`
	Prefix              string   `xml:"Prefix"`
	KeyMarker           string   `xml:"KeyMarker"`
	VersionIDMarker     string   `xml:"VersionIdMarker"`
	NextKeyMarker       string   `xml:"NextKeyMarker,omitempty"`
	NextVersionIDMarker string   `xml:"NextVersionIdMarker,omitempty"`
	MaxKeys             int      `xml:"MaxKeys"`
	Delimiter           string   `xml:"Delimiter,omitempty"`
	IsTruncated         bool     `xml:"IsTruncated"`
	EncodingType        string   `xml:"EncodingType,omitempty"`
	Entries             []versionEntry
	CommonPrefixes      []CommonPrefix `xml:"CommonPrefixes"`
}

func (h *Handler) ListObjectVersions(w http.ResponseWriter, r *http.Request) {
	_, bmeta, ok := h.bucketOr404(w, r)
	if !ok {
		return
	}
	ns := util.GlobalNamespace(r)
	q := r.URL.Query()
	prefix, delimiter := q.Get("prefix"), q.Get("delimiter")
	keyMarker, vidMarker := q.Get("key-marker"), q.Get("version-id-marker")
	maxKeys := parseMaxKeys(q.Get("max-keys"))
	encode := func(s string) string { return s }
	if q.Get("encoding-type") == "url" {
		encode = urlEncodeKey
	}

	vs, err := h.bucketVersions(ns, bmeta.Name, prefix)
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}

	resp := ListVersionsResult{
		Xmlns: s3XMLNS, Name: bmeta.Name, Prefix: encode(prefix),
		KeyMarker: encode(keyMarker), VersionIDMarker: vidMarker,
		MaxKeys: maxKeys, Delimiter: encode(delimiter), EncodingType: q.Get("encoding-type"),
	}
	owner := &Owner{ID: util.AccountID(r), DisplayName: "opensnack"}

	// Skip to the marker: past every version of keys before it, and with a
	// version-id-marker, past that version of the marker key too.
	start := 0
	if keyMarker != "" {
		for start < len(vs) && vs[start].Key < keyMarker {
			start++
		}
		if vidMarker == "" {
			for start < len(vs) && vs[start].Key == keyMarker {
				start++
			}
		} else {
			for i := start; i < len(vs) && vs[i].Key == keyMarker; i++ {
				if vs[i].vid() == vidMarker {
					start = i + 1
					break
				}
			}
		}
	}

	seen := map[string]bool{}
	count := 0
	for i := start; i < len(vs); i++ {
		v := vs[i]
		group := ""
		if delimiter != "" {
			if j := strings.Index(v.Key[len(prefix):], delimiter); j >= 0 {
				group = v.Key[:len(prefix)+j+len(delimiter)]
			}
		}
		if group != "" && seen[group] {
			continue
		}
		if count >= maxKeys {
			resp.IsTruncated = true
			break
		}
		count++
		if group != "" {
			seen[group] = true
			resp.CommonPrefixes = append(resp.CommonPrefixes, CommonPrefix{Prefix: encode(group)})
			resp.NextKeyMarker, resp.NextVersionIDMarker = group, ""
			continue
		}
		e := versionEntry{
			Key: encode(v.Key), VersionID: v.vid(),
			IsLatest:     i == 0 || vs[i-1].Key != v.Key,
			LastModified: v.modTime().Format("2006-01-02T15:04:05.000Z"),
			Owner:        owner,
		}
		if v.DeleteMarker {
			e.XMLName = xml.Name{Local: "DeleteMarker"}
		} else {
			size := v.Size
			e.XMLName = xml.Name{Local: "Version"}
			e.ETag, e.Size, e.StorageClass = v.ETag, &size, "STANDARD"
		}
		resp.Entries = append(resp.Entries, e)
		resp.NextKeyMarker, resp.NextVersionIDMarker = encode(v.Key), v.vid()
	}
	if !resp.IsTruncated {
		resp.NextKeyMarker, resp.NextVersionIDMarker = "", ""
	}
	writeXMLOK(w, resp)
}
