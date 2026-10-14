// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import (
	"hash/fnv"
	"net/http"
	"sync"

	"opensnack/internal/util"
)

// Bucket configuration and object versions are read-modify-write on store
// rows, and the provider configures a bucket's sub-resources in parallel,
// so each bucket (for its configuration) and each key (for its versions) is
// serialised. Locks are striped and never nested.
var stripes [64]sync.Mutex

func lockFor(parts ...string) func() {
	h := fnv.New32a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	m := &stripes[h.Sum32()%uint32(len(stripes))]
	m.Lock()
	return m.Unlock
}

func lockBucket(ns, bucket string) func() { return lockFor("bucket", ns, bucket) }

func lockKey(ns, bucket, key string) func() { return lockFor("key", ns, bucket, key) }

// lockedBucket wraps a handler that edits the bucket row without editBucket.
func lockedBucket(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucket, _ := extractBucketKey(r.URL.Path)
		defer lockBucket(util.GlobalNamespace(r), bucket)()
		fn(w, r)
	}
}
