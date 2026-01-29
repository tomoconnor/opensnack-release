// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	APIVersion     = "2013-04-01"
	route53Account = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build Route53 Hosted Zone ID
func hostedZoneID(name string) string {
	// Route53 hosted zone IDs are random strings like Z1234567890ABC
	// For simplicity, we'll generate a deterministic ID based on the name
	return "Z" + util.DeterministicHex("hzone", 24)
}

// Build Route53 Change ID
func changeID() string {
	// Route53 change IDs are random strings like C1234567890ABC
	return "C" + util.DeterministicHex("hzone", 24)
}

// Dispatch handles Route53 REST API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	// Route53 REST API format: /route53/2013-04-01/hostedzone
	// or /route53/2013-04-01/hostedzone/{id}
	// or /route53/2013-04-01/hostedzone/{id}/rrset
	// or /route53/2013-04-01/change/{id}
	// or /route53/2013-04-01/tags/hostedzone/{id}

	if strings.Contains(path, "/tags/") {
		// GET /tags/hostedzone/{id} → ListTagsForResource
		if method == "GET" {
			h.ListTagsForResource(w, r)
			return
		}
	}

	if strings.Contains(path, "/change/") {
		// GET /change/{id} → GetChange
		if method == "GET" {
			h.GetChange(w, r)
			return
		}
	}

	if strings.Contains(path, "/hostedzone") {
		if strings.HasSuffix(path, "/hostedzone") || strings.HasSuffix(path, "/hostedzone/") {
			// POST /hostedzone → CreateHostedZone
			if method == "POST" {
				h.CreateHostedZone(w, r)
				return
			}
			// GET /hostedzone → ListHostedZones
			if method == "GET" {
				h.ListHostedZones(w, r)
				return
			}
		} else if strings.Contains(path, "/hostedzone/") && strings.Contains(path, "/rrset") {
			// POST /hostedzone/{id}/rrset → ChangeResourceRecordSets
			if method == "POST" {
				h.ChangeResourceRecordSets(w, r)
				return
			}
			// GET /hostedzone/{id}/rrset → ListResourceRecordSets
			if method == "GET" {
				h.ListResourceRecordSets(w, r)
				return
			}
		} else if strings.Contains(path, "/hostedzone/") {
			// GET /hostedzone/{id} → GetHostedZone
			if method == "GET" {
				h.GetHostedZone(w, r)
				return
			}
			// DELETE /hostedzone/{id} → DeleteHostedZone
			if method == "DELETE" {
				h.DeleteHostedZone(w, r)
				return
			}
		}
	}

	// Fallback: try Query API format (for backwards compatibility)
	r.ParseForm()
	action := r.FormValue("Action")
	if action != "" {
		switch action {
		case "CreateHostedZone":
			h.CreateHostedZone(w, r)
			return
		case "GetHostedZone":
			h.GetHostedZone(w, r)
			return
		case "ListHostedZones":
			h.ListHostedZones(w, r)
			return
		case "ChangeResourceRecordSets":
			h.ChangeResourceRecordSets(w, r)
			return
		case "ListResourceRecordSets":
			h.ListResourceRecordSets(w, r)
			return
		case "DeleteHostedZone":
			h.DeleteHostedZone(w, r)
			return
		}
	}

	awsresponses.WriteErrorXML(
		w,
		http.StatusBadRequest,
		"InvalidAction",
		"Unknown Route53 Action",
		"",
	)
}

// CreateHostedZone creates a new hosted zone
func (h *Handler) CreateHostedZone(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateHostedZoneInput
	var name, callerReference string

	// Try to parse XML body first (REST API format)
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "xml") || r.Method == "POST" {
		body, err := io.ReadAll(r.Body)
		if err == nil && len(body) > 0 {
			// Route53 XML has namespace xmlns="https://route53.amazonaws.com/doc/2013-04-01/"
			// Go's xml package handles default namespaces automatically
			decoder := xml.NewDecoder(strings.NewReader(string(body)))
			decoder.Strict = false
			if err := decoder.Decode(&req); err == nil && req.Name != "" {
				name = req.Name
				callerReference = req.CallerReference
			} else {
				// Try direct unmarshal as fallback
				if err := xml.Unmarshal(body, &req); err == nil && req.Name != "" {
					name = req.Name
					callerReference = req.CallerReference
				}
			}
		}
	}

	// Fallback to form values (Query API format)
	if name == "" {
		r.ParseForm()
		name = r.FormValue("Name")
		callerReference = r.FormValue("CallerReference")
	}

	if name == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "Name is required", "")
		return
	}

	if callerReference == "" {
		callerReference = uuid.New().String()
	}

	// Normalize name (ensure it ends with a dot)
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	zoneID := hostedZoneID(name)

	// Check if zone already exists (by ID)
	_, err := h.Store.Get(zoneID, "route53", "hostedzone", ns)
	if err == nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "HostedZoneAlreadyExists", "Hosted zone already exists: "+name, "")
		return
	}

	changeID := changeID()
	// Generate deterministic delegation_set_id based on zone ID for consistency
	// This ensures the same zone always gets the same delegation_set_id
	delegationSetID := "/delegationset/N" + util.DeterministicHex(zoneID, 12)
	now := time.Now().UTC()

	// Create name servers (mock)
	nameServers := []string{
		"ns-1.opensnack.local.",
		"ns-2.opensnack.local.",
		"ns-3.opensnack.local.",
		"ns-4.opensnack.local.",
	}

	// Extract Config comment if provided
	comment := ""
	if req.HostedZoneConfig != nil && req.HostedZoneConfig.Comment != "" {
		comment = req.HostedZoneConfig.Comment
	}

	// Store hosted zone metadata
	zoneMetadata := map[string]any{
		"id":                        zoneID,
		"name":                      name,
		"caller_reference":          callerReference,
		"resource_record_set_count": 2, // NS and SOA records
		"created_at":                now,
		"name_servers":              nameServers,
		"delegation_set_id":         delegationSetID,
		"comment":                   comment,
	}

	buf, _ := json.Marshal(zoneMetadata)
	res := &resource.Resource{
		ID:         zoneID,
		Namespace:  ns,
		Service:    "route53",
		Type:       "hostedzone",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to create hosted zone: "+err.Error(), "")
		return
	}

	// Create default NS and SOA records
	h.createDefaultRecords(zoneID, name, nameServers, ns)

	// Build Config from request
	config := &HostedZoneConfig{}
	if req.HostedZoneConfig != nil {
		config.Comment = req.HostedZoneConfig.Comment
		config.PrivateZone = req.HostedZoneConfig.PrivateZone
	}

	output := CreateHostedZoneOutput{
		HostedZone: HostedZone{
			ID:                     "/hostedzone/" + zoneID,
			Name:                   name,
			CallerReference:        callerReference,
			Config:                 config,
			ResourceRecordSetCount: 2,
		},
		ChangeInfo: ChangeInfo{
			ID:          "/change/" + changeID,
			Status:      "INSYNC",
			SubmittedAt: now.Format(time.RFC3339),
		},
		DelegationSet: DelegationSet{
			ID: delegationSetID,
			NameServers: NameServers{
				NameServer: nameServers,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// createDefaultRecords creates default NS and SOA records for a hosted zone
func (h *Handler) createDefaultRecords(zoneID, zoneName string, nameServers []string, ns string) {
	// Create NS record
	nsRecord := map[string]any{
		"name":    zoneName,
		"type":    "NS",
		"ttl":     172800,
		"records": nameServers,
		"zone_id": zoneID,
	}
	nsBuf, _ := json.Marshal(nsRecord)
	nsRes := &resource.Resource{
		ID:         zoneID + ":NS:" + zoneName,
		Namespace:  ns,
		Service:    "route53",
		Type:       "record",
		Attributes: nsBuf,
	}
	h.Store.Create(nsRes)

	// Create SOA record
	soaRecord := map[string]any{
		"name":    zoneName,
		"type":    "SOA",
		"ttl":     900,
		"records": []string{nameServers[0] + " admin.opensnack.local. 1 7200 900 1209600 86400"},
		"zone_id": zoneID,
	}
	soaBuf, _ := json.Marshal(soaRecord)
	soaRes := &resource.Resource{
		ID:         zoneID + ":SOA:" + zoneName,
		Namespace:  ns,
		Service:    "route53",
		Type:       "record",
		Attributes: soaBuf,
	}
	h.Store.Create(soaRes)
}

// GetHostedZone retrieves a hosted zone
func (h *Handler) GetHostedZone(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var zoneID string

	// Extract zone ID from path: /route53/2013-04-01/hostedzone/{id}
	path := r.URL.Path
	if strings.Contains(path, "/hostedzone/") {
		parts := strings.Split(path, "/hostedzone/")
		if len(parts) > 1 {
			zoneID = parts[1]
			// Remove any trailing path segments
			if idx := strings.Index(zoneID, "/"); idx != -1 {
				zoneID = zoneID[:idx]
			}
		}
	}

	// Fallback to form/query parameter
	if zoneID == "" {
		r.ParseForm()
		zoneID = r.FormValue("Id")
		if zoneID == "" {
			zoneID = r.URL.Query().Get("Id")
		}
	}

	if zoneID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "Id is required", "")
		return
	}

	// Extract zone ID from /hostedzone/Z123 format
	if strings.HasPrefix(zoneID, "/hostedzone/") {
		zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")
	}

	// Get hosted zone from store
	res, err := h.Store.Get(zoneID, "route53", "hostedzone", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "NoSuchHostedZone", "Hosted zone not found: "+zoneID, "")
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to decode hosted zone metadata", "")
		return
	}

	// Extract name servers from stored data
	var nameServers []string
	if nsRaw, ok := entry["name_servers"]; ok {
		if nsArray, ok := nsRaw.([]interface{}); ok {
			for _, ns := range nsArray {
				if nsStr, ok := ns.(string); ok {
					nameServers = append(nameServers, nsStr)
				}
			}
		} else if nsArray, ok := nsRaw.([]string); ok {
			nameServers = nsArray
		}
	}

	// If no name servers found, use defaults
	if len(nameServers) == 0 {
		nameServers = []string{
			"ns-1.opensnack.local.",
			"ns-2.opensnack.local.",
			"ns-3.opensnack.local.",
			"ns-4.opensnack.local.",
		}
	}

	delegationSetID := getString(entry, "delegation_set_id")
	if delegationSetID == "" {
		// Generate deterministic delegation_set_id based on zone ID for consistency
		// This ensures old zones without delegation_set_id get a stable ID
		zoneID := entry["id"].(string)
		delegationSetID = "/delegationset/N" + util.DeterministicHex(zoneID, 12)
		// Persist the generated ID back to storage
		entry["delegation_set_id"] = delegationSetID
		buf, _ := json.Marshal(entry)
		res.Attributes = buf
		h.Store.Update(res)
	} else {
		// Normalize format: ensure it has /delegationset/ prefix
		// Handle both stored formats: "/delegationset/N..." and "N..."
		originalID := delegationSetID
		if !strings.HasPrefix(delegationSetID, "/delegationset/") {
			if strings.HasPrefix(delegationSetID, "N") {
				delegationSetID = "/delegationset/" + delegationSetID
			} else {
				// Invalid format, regenerate deterministically
				zoneID := entry["id"].(string)
				delegationSetID = "/delegationset/N" + util.DeterministicHex(zoneID, 12)
			}
		}
		// If we normalized it, persist back to storage
		if originalID != delegationSetID {
			entry["delegation_set_id"] = delegationSetID
			buf, _ := json.Marshal(entry)
			res.Attributes = buf
			h.Store.Update(res)
		}
	}

	// Extract Config from stored data if available
	config := &HostedZoneConfig{}
	if commentRaw, ok := entry["comment"]; ok {
		if comment, ok := commentRaw.(string); ok && comment != "" {
			config.Comment = comment
		}
	}
	config.PrivateZone = false // Default to public zone

	output := GetHostedZoneOutput{
		HostedZone: HostedZone{
			ID:                     "/hostedzone/" + entry["id"].(string),
			Name:                   entry["name"].(string),
			CallerReference:        entry["caller_reference"].(string),
			Config:                 config,
			ResourceRecordSetCount: int64(entry["resource_record_set_count"].(float64)),
		},
		DelegationSet: DelegationSet{
			ID: delegationSetID,
			NameServers: NameServers{
				NameServer: nameServers,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// ListHostedZones lists all hosted zones
func (h *Handler) ListHostedZones(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	zones, err := h.Store.List("route53", "hostedzone", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to list hosted zones: "+err.Error(), "")
		return
	}

	var hostedZones []HostedZone
	for _, zoneRes := range zones {
		var entry map[string]any
		if err := json.Unmarshal(zoneRes.Attributes, &entry); err != nil {
			continue
		}

		hostedZone := HostedZone{
			ID:                     "/hostedzone/" + entry["id"].(string),
			Name:                   entry["name"].(string),
			CallerReference:        entry["caller_reference"].(string),
			Config:                 &HostedZoneConfig{},
			ResourceRecordSetCount: int64(entry["resource_record_set_count"].(float64)),
		}

		hostedZones = append(hostedZones, hostedZone)
	}

	output := ListHostedZonesOutput{
		ListHostedZonesResult: ListHostedZonesResult{
			HostedZones: HostedZones{
				HostedZone: hostedZones,
			},
			IsTruncated: false,
			MaxItems:    fmt.Sprintf("%d", len(hostedZones)),
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// ChangeResourceRecordSets creates or updates resource record sets
func (h *Handler) ChangeResourceRecordSets(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var hostedZoneID string

	// Extract zone ID from path: /route53/2013-04-01/hostedzone/{id}/rrset
	path := r.URL.Path
	if strings.Contains(path, "/hostedzone/") && strings.Contains(path, "/rrset") {
		parts := strings.Split(path, "/hostedzone/")
		if len(parts) > 1 {
			hostedZoneID = parts[1]
			// Remove /rrset suffix
			if idx := strings.Index(hostedZoneID, "/rrset"); idx != -1 {
				hostedZoneID = hostedZoneID[:idx]
			}
		}
	}

	// Fallback to form/query parameter
	if hostedZoneID == "" {
		r.ParseForm()
		hostedZoneID = r.FormValue("HostedZoneId")
		if hostedZoneID == "" {
			hostedZoneID = r.URL.Query().Get("HostedZoneId")
		}
	}

	if hostedZoneID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "HostedZoneId is required", "")
		return
	}

	// Extract zone ID from /hostedzone/Z123 format
	if strings.HasPrefix(hostedZoneID, "/hostedzone/") {
		hostedZoneID = strings.TrimPrefix(hostedZoneID, "/hostedzone/")
	}

	// Verify zone exists
	_, err := h.Store.Get(hostedZoneID, "route53", "hostedzone", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "NoSuchHostedZone", "Hosted zone not found: "+hostedZoneID, "")
		return
	}

	// Parse XML body first (REST API format)
	var action, name, recordType, ttl string
	var records []string

	if r.Body != nil && r.ContentLength > 0 {
		body, err := io.ReadAll(r.Body)
		if err == nil && len(body) > 0 {
			var xmlReq ChangeResourceRecordSetsInput
			if err := xml.Unmarshal(body, &xmlReq); err == nil {
				if len(xmlReq.ChangeBatch.Changes.Change) > 0 {
					change := xmlReq.ChangeBatch.Changes.Change[0]
					action = change.Action
					name = change.ResourceRecordSet.Name
					recordType = change.ResourceRecordSet.Type
					ttl = fmt.Sprintf("%d", change.ResourceRecordSet.TTL)
					for _, rr := range change.ResourceRecordSet.ResourceRecords.ResourceRecord {
						records = append(records, rr.Value)
					}
				}
			}
		}
	}

	// Fallback to form-based parsing (Query API format)
	if action == "" {
		r.ParseForm()
		action = r.FormValue("ChangeBatch.Change.1.Action")
		name = r.FormValue("ChangeBatch.Change.1.ResourceRecordSet.Name")
		recordType = r.FormValue("ChangeBatch.Change.1.ResourceRecordSet.Type")
		ttl = r.FormValue("ChangeBatch.Change.1.ResourceRecordSet.TTL")

		// Collect all resource record values (they may be numbered 1, 2, 3, etc.)
		for i := 1; i <= 100; i++ { // Support up to 100 records
			value := r.FormValue(fmt.Sprintf("ChangeBatch.Change.1.ResourceRecordSet.ResourceRecords.ResourceRecord.%d.Value", i))
			if value == "" {
				break
			}
			records = append(records, value)
		}
	}

	if action == "" || name == "" || recordType == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "Invalid change request", "")
		return
	}

	// Normalize name (ensure it ends with a dot)
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	// Process the change
	recordID := hostedZoneID + ":" + recordType + ":" + name

	if action == "CREATE" || action == "UPSERT" {
		if len(records) == 0 {
			awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "At least one resource record value is required", "")
			return
		}

		record := map[string]any{
			"name":    name,
			"type":    recordType,
			"ttl":     parseTTL(ttl),
			"records": records,
			"zone_id": hostedZoneID,
		}
		buf, _ := json.Marshal(record)
		recordRes := &resource.Resource{
			ID:         recordID,
			Namespace:  ns,
			Service:    "route53",
			Type:       "record",
			Attributes: buf,
		}

		// Check if record exists
		_, err := h.Store.Get(recordID, "route53", "record", ns)
		if err == nil {
			// Update existing record
			h.Store.Update(recordRes)
		} else {
			// Create new record
			h.Store.Create(recordRes)
		}
	} else if action == "DELETE" {
		h.Store.Delete(recordID, "route53", "record", ns)
	}

	changeID := changeID()
	now := time.Now().UTC()

	output := ChangeResourceRecordSetsOutput{
		ChangeResourceRecordSetsResult: ChangeResourceRecordSetsResult{
			ChangeInfo: ChangeInfo{
				ID:          "/change/" + changeID,
				Status:      "INSYNC",
				SubmittedAt: now.Format(time.RFC3339),
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// ListResourceRecordSets lists resource record sets for a hosted zone
func (h *Handler) ListResourceRecordSets(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var hostedZoneID string

	// Extract zone ID from path: /route53/2013-04-01/hostedzone/{id}/rrset
	path := r.URL.Path
	if strings.Contains(path, "/hostedzone/") && strings.Contains(path, "/rrset") {
		parts := strings.Split(path, "/hostedzone/")
		if len(parts) > 1 {
			hostedZoneID = parts[1]
			// Remove /rrset suffix
			if idx := strings.Index(hostedZoneID, "/rrset"); idx != -1 {
				hostedZoneID = hostedZoneID[:idx]
			}
		}
	}

	// Fallback to form/query parameter
	if hostedZoneID == "" {
		r.ParseForm()
		hostedZoneID = r.FormValue("HostedZoneId")
		if hostedZoneID == "" {
			hostedZoneID = r.URL.Query().Get("HostedZoneId")
		}
	}

	if hostedZoneID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "HostedZoneId is required", "")
		return
	}

	// Extract zone ID from /hostedzone/Z123 format
	if strings.HasPrefix(hostedZoneID, "/hostedzone/") {
		hostedZoneID = strings.TrimPrefix(hostedZoneID, "/hostedzone/")
	}

	// Verify zone exists
	_, err := h.Store.Get(hostedZoneID, "route53", "hostedzone", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "NoSuchHostedZone", "Hosted zone not found: "+hostedZoneID, "")
		return
	}

	// Parse query parameters for filtering
	r.ParseForm()
	filterName := r.FormValue("name")
	filterType := r.FormValue("type")
	maxItemsStr := r.FormValue("maxitems")
	maxItems := 0
	if maxItemsStr != "" {
		fmt.Sscanf(maxItemsStr, "%d", &maxItems)
	}

	// Get all records for this zone
	allRecords, err := h.Store.List("route53", "record", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to list records: "+err.Error(), "")
		return
	}

	var recordSets []ResourceRecordSet
	for _, recordRes := range allRecords {
		var entry map[string]any
		if err := json.Unmarshal(recordRes.Attributes, &entry); err != nil {
			continue
		}

		// Filter by zone ID
		if entry["zone_id"].(string) != hostedZoneID {
			continue
		}

		recordName := entry["name"].(string)
		recordType := entry["type"].(string)

		// Filter by name if provided
		if filterName != "" {
			// Normalize both names (ensure they end with dot)
			normalizedFilterName := filterName
			if !strings.HasSuffix(normalizedFilterName, ".") {
				normalizedFilterName = normalizedFilterName + "."
			}
			if recordName != normalizedFilterName {
				continue
			}
		}

		// Filter by type if provided
		if filterType != "" && recordType != filterType {
			continue
		}

		var records []ResourceRecord
		if recordValues, ok := entry["records"].([]string); ok {
			for _, value := range recordValues {
				records = append(records, ResourceRecord{Value: value})
			}
		} else if recordValues, ok := entry["records"].([]interface{}); ok {
			for _, v := range recordValues {
				if value, ok := v.(string); ok {
					records = append(records, ResourceRecord{Value: value})
				}
			}
		}

		recordSet := ResourceRecordSet{
			Name: recordName,
			Type: recordType,
			TTL:  int64(entry["ttl"].(float64)),
			ResourceRecords: ResourceRecords{
				ResourceRecord: records,
			},
		}

		recordSets = append(recordSets, recordSet)

		// Apply maxItems limit if specified
		if maxItems > 0 && len(recordSets) >= maxItems {
			break
		}
	}

	output := ListResourceRecordSetsOutput{
		ResourceRecordSets: ResourceRecordSets{
			ResourceRecordSet: recordSets,
		},
		IsTruncated: false,
		MaxItems:    fmt.Sprintf("%d", len(recordSets)),
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// Helper function to parse TTL
func parseTTL(ttlStr string) int64 {
	var ttl int64
	fmt.Sscanf(ttlStr, "%d", &ttl)
	if ttl == 0 {
		ttl = 300 // Default TTL
	}
	return ttl
}

// GetChange retrieves the status of a change
func (h *Handler) GetChange(w http.ResponseWriter, r *http.Request) {
	var changeID string

	// Extract change ID from path: /route53/2013-04-01/change/{id}
	path := r.URL.Path
	if strings.Contains(path, "/change/") {
		parts := strings.Split(path, "/change/")
		if len(parts) > 1 {
			changeID = parts[1]
		}
	}

	// Fallback to form/query parameter
	if changeID == "" {
		r.ParseForm()
		changeID = r.FormValue("Id")
		if changeID == "" {
			changeID = r.URL.Query().Get("Id")
		}
	}

	if changeID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "Id is required", "")
		return
	}

	// Extract change ID from /change/C123 format
	if strings.HasPrefix(changeID, "/change/") {
		changeID = strings.TrimPrefix(changeID, "/change/")
	}

	// For now, all changes are INSYNC immediately
	// In a real implementation, you'd track change status in the store
	now := time.Now().UTC()

	output := GetChangeOutput{
		ChangeInfo: ChangeInfo{
			ID:          "/change/" + changeID,
			Status:      "INSYNC",
			SubmittedAt: now.Format(time.RFC3339),
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// ListTagsForResource lists tags for a Route53 resource
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	// Extract resource type and ID from path: /route53/2013-04-01/tags/{type}/{id}
	path := r.URL.Path
	var resourceType, resourceID string

	if strings.Contains(path, "/tags/") {
		parts := strings.Split(path, "/tags/")
		if len(parts) > 1 {
			// parts[1] should be like "hostedzone/Zb368b8601ed5eaae0b1cf84d"
			subParts := strings.SplitN(parts[1], "/", 2)
			if len(subParts) == 2 {
				resourceType = subParts[0]
				resourceID = subParts[1]
			}
		}
	}

	// Fallback to query parameters
	if resourceType == "" || resourceID == "" {
		r.ParseForm()
		resourceType = r.FormValue("ResourceType")
		resourceID = r.FormValue("ResourceId")
		if resourceType == "" {
			resourceType = r.URL.Query().Get("ResourceType")
		}
		if resourceID == "" {
			resourceID = r.URL.Query().Get("ResourceId")
		}
	}

	if resourceType == "" || resourceID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "ResourceType and ResourceId are required", "")
		return
	}

	// Extract ID from /hostedzone/Z123 format if needed
	if strings.HasPrefix(resourceID, "/hostedzone/") {
		resourceID = strings.TrimPrefix(resourceID, "/hostedzone/")
	}

	// For now, return empty tags (Route53 resources don't have tags by default)
	// In a real implementation, you'd store and retrieve tags from the store
	output := ListTagsForResourceOutput{
		ResourceTagSet: ResourceTagSet{
			ResourceType: resourceType,
			ResourceID:   resourceID,
			Tags: Tags{
				Tag: []Tag{},
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// DeleteHostedZone deletes a hosted zone
func (h *Handler) DeleteHostedZone(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var zoneID string

	// Extract zone ID from path: /route53/2013-04-01/hostedzone/{id}
	path := r.URL.Path
	if strings.Contains(path, "/hostedzone/") {
		parts := strings.Split(path, "/hostedzone/")
		if len(parts) > 1 {
			zoneID = parts[1]
			// Remove any trailing path segments
			if idx := strings.Index(zoneID, "/"); idx != -1 {
				zoneID = zoneID[:idx]
			}
		}
	}

	// Fallback to form/query parameter
	if zoneID == "" {
		r.ParseForm()
		zoneID = r.FormValue("Id")
		if zoneID == "" {
			zoneID = r.URL.Query().Get("Id")
		}
	}

	if zoneID == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInput", "Id is required", "")
		return
	}

	// Extract zone ID from /hostedzone/Z123 format
	if strings.HasPrefix(zoneID, "/hostedzone/") {
		zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")
	}

	// Verify zone exists
	_, err := h.Store.Get(zoneID, "route53", "hostedzone", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "NoSuchHostedZone", "Hosted zone not found: "+zoneID, "")
		return
	}

	// Delete the hosted zone
	if err := h.Store.Delete(zoneID, "route53", "hostedzone", ns); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to delete hosted zone: "+err.Error(), "")
		return
	}

	// Return change info (similar to CreateHostedZone)
	changeIDVal := changeID()
	now := time.Now().UTC()

	output := DeleteHostedZoneOutput{
		ChangeInfo: ChangeInfo{
			ID:          "/change/" + changeIDVal,
			Status:      "PENDING",
			SubmittedAt: now.Format(time.RFC3339),
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, output)
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
