// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxCachedObject is the largest response the edge cache keeps.
	maxCachedObject = 512 << 10
	// maxCachePerDistribution bounds the memory one distribution can use.
	maxCachePerDistribution = 32 << 20
)

type cached struct {
	status  int
	header  http.Header
	body    []byte
	stored  time.Time
	expires time.Time
}

// cache is the in-memory edge cache, one bucket per distribution. It lives
// in this process only, like a single edge location.
type cache struct {
	mu    sync.Mutex
	dists map[string]*distCache
	now   func() time.Time
}

type distCache struct {
	entries map[string]*cached
	size    int
}

func newCache() *cache {
	return &cache{dists: map[string]*distCache{}, now: time.Now}
}

func (c *cache) get(dist, key string) *cached {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.dists[dist]
	if d == nil {
		return nil
	}
	e := d.entries[key]
	if e == nil {
		return nil
	}
	if !c.now().Before(e.expires) {
		d.size -= len(e.body)
		delete(d.entries, key)
		return nil
	}
	return e
}

func (c *cache) put(dist, key string, e *cached) {
	if len(e.body) > maxCachedObject {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.dists[dist]
	if d == nil {
		d = &distCache{entries: map[string]*cached{}}
		c.dists[dist] = d
	}
	if old := d.entries[key]; old != nil {
		d.size -= len(old.body)
	}
	// Over budget: expired entries go first, then anything.
	now := c.now()
	for _, expiredOnly := range []bool{true, false} {
		for k, x := range d.entries {
			if d.size+len(e.body) <= maxCachePerDistribution {
				break
			}
			if !expiredOnly || !now.Before(x.expires) {
				d.size -= len(x.body)
				delete(d.entries, k)
			}
		}
	}
	d.entries[key] = e
	d.size += len(e.body)
}

// invalidate drops the entries whose path matches one of paths; a trailing
// "*" matches any suffix, as in CloudFront.
func (c *cache) invalidate(dist string, paths []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.dists[dist]
	if d == nil {
		return
	}
	for key, e := range d.entries {
		path, _, _ := strings.Cut(key, "?")
		for _, p := range paths {
			if prefix, wild := strings.CutSuffix(p, "*"); wild && strings.HasPrefix(path, prefix) || path == p {
				d.size -= len(e.body)
				delete(d.entries, key)
				break
			}
		}
	}
}

func (c *cache) drop(dist string) {
	c.mu.Lock()
	delete(c.dists, dist)
	c.mu.Unlock()
}

// len is the number of live entries (for tests).
func (c *cache) len(dist string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.dists[dist]; d != nil {
		return len(d.entries)
	}
	return 0
}
