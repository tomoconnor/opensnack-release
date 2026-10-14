// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"encoding/xml"
	"time"
)

// --- ListBuckets Result ---

type BucketEntry struct {
	XMLName      xml.Name  `xml:"Bucket"`
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
	BucketRegion string    `xml:"BucketRegion,omitempty"`
}

type ListAllMyBucketsResult struct {
	XMLName xml.Name      `xml:"ListAllMyBucketsResult"`
	Xmlns   string        `xml:"xmlns,attr,omitempty"`
	Owner   *Owner        `xml:"Owner,omitempty"` // optional; LocalStack does not always send it
	Buckets []BucketEntry `xml:"Buckets>Bucket"`
}

type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

// --- CreateBucket (XML mode) ---

type CreateBucketResult struct {
	XMLName  xml.Name `xml:"CreateBucketConfiguration"`
	Location string   `xml:"Location"`
}

// --- GetBucketLocation Response ---

type LocationConstraint struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Region  string   `xml:",chardata"`
}

// --- ListObjects / ListObjectsV2 ---

// s3XMLNS is the namespace AWS stamps on every S3 REST response body.
const s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// ObjectEntry is one <Contents> element of a bucket listing.
type ObjectEntry struct {
	XMLName      xml.Name `xml:"Contents"`
	Key          string   `xml:"Key"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
	Size         int64    `xml:"Size"`
	StorageClass string   `xml:"StorageClass"`
}

// CommonPrefix rolls up every key sharing a prefix up to the delimiter, which is
// how `aws s3 ls` renders directories.
type CommonPrefix struct {
	XMLName xml.Name `xml:"CommonPrefixes"`
	Prefix  string   `xml:"Prefix"`
}

// ListBucketResult is the ListObjectsV2 (list-type=2) response.
type ListBucketResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	MaxKeys               int            `xml:"MaxKeys"`
	KeyCount              int            `xml:"KeyCount"`
	IsTruncated           bool           `xml:"IsTruncated"`
	EncodingType          string         `xml:"EncodingType,omitempty"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	Contents              []ObjectEntry  `xml:"Contents"`
	CommonPrefixes        []CommonPrefix `xml:"CommonPrefixes"`
}

// ListBucketResultV1 is the original ListObjects response: the same body, but
// paged by Marker instead of a continuation token and without KeyCount.
type ListBucketResultV1 struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	MaxKeys        int            `xml:"MaxKeys"`
	IsTruncated    bool           `xml:"IsTruncated"`
	EncodingType   string         `xml:"EncodingType,omitempty"`
	Marker         string         `xml:"Marker"`
	NextMarker     string         `xml:"NextMarker,omitempty"`
	Contents       []ObjectEntry  `xml:"Contents"`
	CommonPrefixes []CommonPrefix `xml:"CommonPrefixes"`
}
