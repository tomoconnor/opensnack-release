// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"net/http"
	"sort"
	"strings"
)

// Invalidations complete as soon as they are created: the cache is local,
// so clearing it is immediate.
func (h *Handler) invalidations(w http.ResponseWriter, r *http.Request, ns string, dist *record, segs []string) error {
	switch {
	case len(segs) == 0 && r.Method == http.MethodPost:
		return h.createInvalidation(w, r, ns, dist)
	case len(segs) == 0 && r.Method == http.MethodGet:
		var recs []*record
		for _, rec := range h.all(ns, "invalidation") {
			if rec.Distribution == dist.ID {
				recs = append(recs, rec)
			}
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].Modified > recs[j].Modified })
		items := elem("Items")
		for _, rec := range recs {
			items.Children = append(items.Children, elem("InvalidationSummary",
				leaf("Id", rec.ID), leaf("CreateTime", rec.Modified), leaf("Status", "Completed")))
		}
		doc := elem("InvalidationList", leaf("Marker", ""), leaf("MaxItems", "100"),
			leaf("IsTruncated", "false"), leaf("Quantity", itoa(len(recs))))
		if len(recs) > 0 {
			doc.Children = append(doc.Children, items)
		}
		writeDoc(w, http.StatusOK, doc, "")
		return nil
	case len(segs) == 1 && r.Method == http.MethodGet:
		rec, err := h.load(ns, "invalidation", segs[0])
		if err != nil || rec.Distribution != dist.ID {
			return errf(http.StatusNotFound, "NoSuchInvalidation", "The specified invalidation does not exist.")
		}
		writeDoc(w, http.StatusOK, invalidationDoc(rec), "")
		return nil
	}
	return errf(http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
}

func invalidationDoc(rec *record) *node {
	return elem("Invalidation", leaf("Id", rec.ID), leaf("Status", "Completed"),
		leaf("CreateTime", rec.Modified), rec.config())
}

func (h *Handler) createInvalidation(w http.ResponseWriter, r *http.Request, ns string, dist *record) error {
	batch, err := readDoc(r)
	if err != nil {
		return err
	}
	if batch.Name != "InvalidationBatch" || batch.text("CallerReference") == "" {
		return malformed(errMissing("InvalidationBatch/CallerReference"))
	}
	var paths []string
	for _, p := range batch.items("Paths") {
		paths = append(paths, strings.TrimSpace(p.Text))
	}
	if len(paths) == 0 {
		return invalidArg("The parameter Paths must contain at least one path.")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			return invalidArg("Your request contains one or more invalid invalidation paths: %s", p)
		}
		if i := strings.Index(p, "*"); i >= 0 && i != len(p)-1 {
			return invalidArg("Your request contains one or more invalid invalidation paths: %s", p)
		}
	}
	// The same caller reference returns the original invalidation.
	for _, rec := range h.all(ns, "invalidation") {
		if rec.Distribution == dist.ID && rec.config().text("CallerReference") == batch.text("CallerReference") {
			writeDoc(w, http.StatusCreated, invalidationDoc(rec), "")
			return nil
		}
	}
	rec := &record{ID: randomID("I", 13), Distribution: dist.ID, Config: batch.String(), Modified: h.timestamp()}
	if err := h.save(ns, "invalidation", rec, true); err != nil {
		return err
	}
	h.cache.invalidate(dist.ID, paths)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com"+apiVersion+"distribution/"+dist.ID+"/invalidation/"+rec.ID)
	writeDoc(w, http.StatusCreated, invalidationDoc(rec), "")
	return nil
}
