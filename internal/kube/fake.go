// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kube

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Pods for tests. Pods it creates stay Pending until
// the test moves them on (or OnCreate does).
type Fake struct {
	mu   sync.Mutex
	pods map[string]*Pod
	// OnCreate, when set, runs after each create (outside the lock), so a
	// test can start a pretend runtime for the pod.
	OnCreate func(p *Pod)
	// CreateErr, when set, fails creates.
	CreateErr error
	// Deleted lists the names of deleted pods, in order.
	Deleted []string
	seq     int
}

func NewFake() *Fake { return &Fake{pods: map[string]*Pod{}} }

func (f *Fake) Create(_ context.Context, pod *Pod) (*Pod, error) {
	f.mu.Lock()
	if f.CreateErr != nil {
		f.mu.Unlock()
		return nil, f.CreateErr
	}
	if _, ok := f.pods[pod.Metadata.Name]; ok {
		f.mu.Unlock()
		return nil, &APIError{Code: http.StatusConflict, Reason: "AlreadyExists", Message: "pod exists"}
	}
	p := *pod
	f.seq++
	now := time.Now()
	p.Metadata.CreationTimestamp = &now
	p.Metadata.UID = fmt.Sprintf("uid-%d", f.seq)
	p.Status = PodStatus{Phase: PodPending, PodIP: fmt.Sprintf("10.42.0.%d", f.seq%250+1)}
	f.pods[p.Metadata.Name] = &p
	out := p
	hook := f.OnCreate
	f.mu.Unlock()
	if hook != nil {
		hook(&out)
	}
	return &out, nil
}

func (f *Fake) Get(_ context.Context, name string) (*Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pods[name]
	if !ok {
		return nil, &APIError{Code: http.StatusNotFound, Reason: "NotFound", Message: "pods \"" + name + "\" not found"}
	}
	out := *p
	return &out, nil
}

func (f *Fake) List(_ context.Context, selector string) ([]Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Pod
	for _, p := range f.pods {
		if matches(p.Metadata.Labels, selector) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}

func (f *Fake) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pods[name]; ok {
		delete(f.pods, name)
		f.Deleted = append(f.Deleted, name)
	}
	return nil
}

// Put adds a pod as if something else had created it.
func (f *Fake) Put(p Pod) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[p.Metadata.Name] = &p
}

// Update changes a pod in place (its status, typically).
func (f *Fake) Update(name string, change func(p *Pod)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.pods[name]; ok {
		change(p)
	}
}

// Names lists the pods that exist.
func (f *Fake) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.pods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// matches evaluates an equality-only label selector.
func matches(labels map[string]string, selector string) bool {
	if selector == "" {
		return true
	}
	for _, term := range strings.Split(selector, ",") {
		k, v, ok := strings.Cut(term, "=")
		if !ok {
			if _, has := labels[term]; !has {
				return false
			}
			continue
		}
		if labels[k] != v {
			return false
		}
	}
	return true
}
