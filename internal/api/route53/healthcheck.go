// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"sort"

	"opensnack/internal/util"

	"github.com/google/uuid"
)

type healthCheck struct {
	ID              string            `json:"id"`
	CallerReference string            `json:"caller_reference"`
	Config          HealthCheckConfig `json:"config"`
	Version         int64             `json:"version"`
	Tags            map[string]string `json:"tags,omitempty"`
}

func (hc *healthCheck) out() HealthCheck {
	return HealthCheck{ID: hc.ID, CallerReference: hc.CallerReference, HealthCheckConfig: hc.Config, HealthCheckVersion: hc.Version}
}

var healthCheckTypes = map[string]bool{
	"HTTP": true, "HTTPS": true, "HTTP_STR_MATCH": true, "HTTPS_STR_MATCH": true,
	"TCP": true, "CALCULATED": true, "CLOUDWATCH_METRIC": true, "RECOVERY_CONTROL": true,
}

func writeNoSuchHealthCheck(w http.ResponseWriter, id string) {
	writeError(w, http.StatusNotFound, "NoSuchHealthCheck", "No health check exists with the specified ID "+id)
}

func checkHealthCheckConfig(c *HealthCheckConfig) string {
	if !healthCheckTypes[c.Type] {
		return "Invalid health check type: " + c.Type
	}
	switch c.Type {
	case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
		if c.IPAddress == "" && c.FullyQualifiedDomainName == "" {
			return "Invalid parameter : An IP address or a fully qualified domain name is required for " + c.Type + " health checks"
		}
		if c.Type == "TCP" && c.Port == nil {
			return "Invalid parameter : Port is required for TCP health checks"
		}
	}
	if (c.Type == "HTTP_STR_MATCH" || c.Type == "HTTPS_STR_MATCH") && c.SearchString == "" {
		return "Invalid parameter : SearchString is required for " + c.Type + " health checks"
	}
	if c.FailureThreshold != nil && (*c.FailureThreshold < 1 || *c.FailureThreshold > 10) {
		return "Invalid parameter : FailureThreshold must be between 1 and 10"
	}
	if c.RequestInterval != nil && *c.RequestInterval != 10 && *c.RequestInterval != 30 {
		return "Invalid parameter : RequestInterval must be 10 or 30"
	}
	if n := len(c.Regions); n > 0 && n < 3 {
		return "Invalid parameter : At least 3 regions are required"
	}
	return ""
}

func (h *Handler) CreateHealthCheck(w http.ResponseWriter, r *http.Request) {
	ns := util.GlobalNamespace(r)
	var req CreateHealthCheckRequest
	if err := decodeBody(r, &req); err != nil || req.CallerReference == "" {
		writeInvalidInput(w, "CallerReference and HealthCheckConfig are required")
		return
	}
	c := req.HealthCheckConfig
	if msg := checkHealthCheckConfig(&c); msg != "" {
		writeInvalidInput(w, msg)
		return
	}
	// The defaults Route 53 fills in and returns.
	switch c.Type {
	case "CALCULATED", "CLOUDWATCH_METRIC", "RECOVERY_CONTROL":
	default:
		if c.FailureThreshold == nil {
			v := int64(3)
			c.FailureThreshold = &v
		}
		if c.EnableSNI == nil {
			v := c.Type == "HTTPS" || c.Type == "HTTPS_STR_MATCH"
			c.EnableSNI = &v
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	all, _ := h.Store.List(service, typeHealthCheck, ns)
	for _, res := range all {
		var other healthCheck
		if json.Unmarshal(res.Attributes, &other) == nil && other.CallerReference == req.CallerReference {
			writeError(w, http.StatusConflict, "HealthCheckAlreadyExists", "A health check with the specified caller reference "+req.CallerReference+" already exists.")
			return
		}
	}
	hc := healthCheck{ID: uuid.NewString(), CallerReference: req.CallerReference, Config: c, Version: 1}
	if err := h.save(ns, typeHealthCheck, hc.ID, hc, true); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	w.Header().Set("Location", util.BaseURL(r)+"/"+APIVersion+"/healthcheck/"+hc.ID)
	writeXML(w, http.StatusCreated, HealthCheckResponse{XMLName: xml.Name{Local: "CreateHealthCheckResponse"}, Xmlns: xmlns, HealthCheck: hc.out()})
}

func (h *Handler) GetHealthCheck(w http.ResponseWriter, r *http.Request, id string) {
	var hc healthCheck
	if !h.load(util.GlobalNamespace(r), typeHealthCheck, id, &hc) {
		writeNoSuchHealthCheck(w, id)
		return
	}
	writeXML(w, http.StatusOK, HealthCheckResponse{XMLName: xml.Name{Local: "GetHealthCheckResponse"}, Xmlns: xmlns, HealthCheck: hc.out()})
}

func (h *Handler) UpdateHealthCheck(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	var req UpdateHealthCheckRequest
	if err := decodeBody(r, &req); err != nil {
		writeInvalidInput(w, "Could not parse UpdateHealthCheckRequest: "+err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var hc healthCheck
	if !h.load(ns, typeHealthCheck, id, &hc) {
		writeNoSuchHealthCheck(w, id)
		return
	}
	if req.HealthCheckVersion != nil && *req.HealthCheckVersion != hc.Version {
		writeError(w, http.StatusConflict, "HealthCheckVersionMismatch", "The value of HealthCheckVersion in the request doesn't match the value of HealthCheckVersion in the health check.")
		return
	}
	c := hc.Config
	setStr := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setStr(&c.IPAddress, req.IPAddress)
	setStr(&c.ResourcePath, req.ResourcePath)
	setStr(&c.FullyQualifiedDomainName, req.FullyQualifiedDomainName)
	setStr(&c.SearchString, req.SearchString)
	setStr(&c.InsufficientDataHealthStatus, req.InsufficientDataHealthStatus)
	if req.Port != nil {
		c.Port = req.Port
	}
	if req.FailureThreshold != nil {
		c.FailureThreshold = req.FailureThreshold
	}
	if req.Inverted != nil {
		c.Inverted = req.Inverted
	}
	if req.Disabled != nil {
		c.Disabled = req.Disabled
	}
	if req.HealthThreshold != nil {
		c.HealthThreshold = req.HealthThreshold
	}
	if req.EnableSNI != nil {
		c.EnableSNI = req.EnableSNI
	}
	if req.ChildHealthChecks != nil {
		c.ChildHealthChecks = req.ChildHealthChecks
	}
	if req.Regions != nil {
		c.Regions = req.Regions
	}
	if req.AlarmIdentifier != nil {
		c.AlarmIdentifier = req.AlarmIdentifier
	}
	for _, e := range req.ResetElements {
		switch e {
		case "FullyQualifiedDomainName":
			c.FullyQualifiedDomainName = ""
		case "Regions":
			c.Regions = nil
		case "ResourcePath":
			c.ResourcePath = ""
		case "ChildHealthChecks":
			c.ChildHealthChecks = nil
		default:
			writeInvalidInput(w, "Invalid ResettableElementName: "+e)
			return
		}
	}
	if msg := checkHealthCheckConfig(&c); msg != "" {
		writeInvalidInput(w, msg)
		return
	}
	hc.Config = c
	hc.Version++
	if err := h.save(ns, typeHealthCheck, hc.ID, hc, false); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, HealthCheckResponse{XMLName: xml.Name{Local: "UpdateHealthCheckResponse"}, Xmlns: xmlns, HealthCheck: hc.out()})
}

func (h *Handler) DeleteHealthCheck(w http.ResponseWriter, r *http.Request, id string) {
	ns := util.GlobalNamespace(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	var hc healthCheck
	if !h.load(ns, typeHealthCheck, id, &hc) {
		writeNoSuchHealthCheck(w, id)
		return
	}
	all, _ := h.Store.List(service, typeRecord, ns)
	for _, res := range all {
		var rr ResourceRecordSet
		if json.Unmarshal(res.Attributes, &rr) == nil && rr.HealthCheckID == id {
			writeError(w, http.StatusBadRequest, "HealthCheckInUse", "The health check with ID "+id+" is still referenced from the record set "+rr.Name)
			return
		}
	}
	if err := h.Store.Delete(id, service, typeHealthCheck, ns); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	writeXML(w, http.StatusOK, EmptyResponse{XMLName: xml.Name{Local: "DeleteHealthCheckResponse"}, Xmlns: xmlns})
}

func (h *Handler) listHealthChecks(ns string) []healthCheck {
	all, _ := h.Store.List(service, typeHealthCheck, ns)
	out := make([]healthCheck, 0, len(all))
	for _, res := range all {
		var hc healthCheck
		if json.Unmarshal(res.Attributes, &hc) == nil {
			out = append(out, hc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (h *Handler) ListHealthChecks(w http.ResponseWriter, r *http.Request) {
	limit, ok := maxItems(r, 1000)
	if !ok {
		writeInvalidInput(w, "maxitems must be a positive integer")
		return
	}
	marker := r.URL.Query().Get("marker")
	out := ListHealthChecksResponse{Xmlns: xmlns, Marker: marker, MaxItems: limit, HealthChecks: []HealthCheck{}}
	for _, hc := range h.listHealthChecks(util.GlobalNamespace(r)) {
		if hc.ID < marker {
			continue
		}
		if len(out.HealthChecks) == limit {
			out.IsTruncated, out.NextMarker = true, hc.ID
			break
		}
		out.HealthChecks = append(out.HealthChecks, hc.out())
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) GetHealthCheckCount(w http.ResponseWriter, r *http.Request) {
	writeXML(w, http.StatusOK, GetHealthCheckCountResponse{Xmlns: xmlns, HealthCheckCount: len(h.listHealthChecks(util.GlobalNamespace(r)))})
}
