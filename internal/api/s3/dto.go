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
}

type ListAllMyBucketsResult struct {
	XMLName xml.Name      `xml:"ListAllMyBucketsResult"`
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
