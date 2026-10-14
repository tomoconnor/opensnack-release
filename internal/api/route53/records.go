// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"opensnack/internal/util"
)

// recordKey is the store ID of a record set: zone, name, type and set
// identifier, which together identify it in Route 53.
func recordKey(zoneID string, rr ResourceRecordSet) string {
	return zoneID + "/" + rr.Name + "/" + rr.Type + "/" + rr.SetIdentifier
}

// describe is how Route 53 names a record set in error messages.
func describe(rr ResourceRecordSet) string {
	if rr.SetIdentifier != "" {
		return fmt.Sprintf("[name='%s', type='%s', set-identifier='%s']", rr.Name, rr.Type, rr.SetIdentifier)
	}
	return fmt.Sprintf("[name='%s', type='%s']", rr.Name, rr.Type)
}

func changeContext(c Change) string {
	id := "null"
	if c.ResourceRecordSet.SetIdentifier != "" {
		id = c.ResourceRecordSet.SetIdentifier
	}
	return fmt.Sprintf("[Action=%s, Name=%s, Type=%s, SetIdentifier=%s]", c.Action, c.ResourceRecordSet.Name, c.ResourceRecordSet.Type, id)
}

// routingPolicy names the routing policy of a record set; "" is simple.
func routingPolicy(rr ResourceRecordSet) string {
	switch {
	case rr.Weight != nil:
		return "weighted"
	case rr.Region != "":
		return "latency"
	case rr.Failover != "":
		return "failover"
	case rr.GeoLocation != nil:
		return "geolocation"
	case rr.MultiValueAnswer != nil:
		return "multivalue answer"
	case rr.CidrRoutingConfig != nil:
		return "CIDR"
	case rr.GeoProximityLocation != nil:
		return "geoproximity"
	}
	return ""
}

func routingPolicyCount(rr ResourceRecordSet) int {
	n := 0
	for _, set := range []bool{
		rr.Weight != nil, rr.Region != "", rr.Failover != "", rr.GeoLocation != nil,
		rr.MultiValueAnswer != nil, rr.CidrRoutingConfig != nil, rr.GeoProximityLocation != nil,
	} {
		if set {
			n++
		}
	}
	return n
}

// checkInput does the request-shape validation that Route 53 reports as
// InvalidInput before it looks at the zone.
func checkInput(c Change) string {
	rr := c.ResourceRecordSet
	switch c.Action {
	case "CREATE", "DELETE", "UPSERT":
	default:
		return fmt.Sprintf("Invalid request: Invalid action %q in Change with %s", c.Action, changeContext(c))
	}
	if rr.Name == "" || len(rr.Name) > 1024 {
		return "Invalid request: Invalid Name in Change with " + changeContext(c)
	}
	if !validTypes[rr.Type] {
		return fmt.Sprintf("1 validation error detected: Value '%s' at 'changeBatch.changes.member.resourceRecordSet.type' failed to satisfy constraint: Member must satisfy enum value set: [SOA, A, TXT, NS, CNAME, MX, NAPTR, PTR, SRV, SPF, AAAA, CAA, DS, TLSA, SSHFP, SVCB, HTTPS]", rr.Type)
	}
	hasAlias := rr.AliasTarget != nil
	hasRecords := rr.TTL != nil || len(rr.ResourceRecords) > 0
	switch {
	case hasAlias && hasRecords:
		return "Invalid request: Expected exactly one of [AliasTarget, all of [TTL, and ResourceRecords], or TrafficPolicyInstanceId], but found more than one in Change with " + changeContext(c)
	case !hasAlias && (rr.TTL == nil || len(rr.ResourceRecords) == 0):
		if rr.TTL != nil || len(rr.ResourceRecords) > 0 {
			missing := "TTL"
			if rr.TTL != nil {
				missing = "ResourceRecords"
			}
			return fmt.Sprintf("Invalid request: Expected exactly one of [AliasTarget, all of [TTL, and ResourceRecords], or TrafficPolicyInstanceId], but found none in Change with %s (missing %s)", changeContext(c), missing)
		}
		return "Invalid request: Expected exactly one of [AliasTarget, all of [TTL, and ResourceRecords], or TrafficPolicyInstanceId], but found none in Change with " + changeContext(c)
	}
	if hasAlias && (rr.AliasTarget.HostedZoneID == "" || rr.AliasTarget.DNSName == "") {
		return "Invalid request: AliasTarget requires HostedZoneId and DNSName in Change with " + changeContext(c)
	}
	if rr.TTL != nil && (*rr.TTL < 0 || *rr.TTL > 2147483647) {
		return fmt.Sprintf("1 validation error detected: Value '%d' at 'changeBatch.changes.member.resourceRecordSet.tTL' failed to satisfy constraint: Member must have value between 0 and 2147483647", *rr.TTL)
	}
	if len(rr.SetIdentifier) > 128 {
		return "Invalid request: SetIdentifier must be at most 128 characters in Change with " + changeContext(c)
	}
	n := routingPolicyCount(rr)
	switch {
	case n > 1:
		return "Invalid request: Expected exactly one of [Weight, Region, Failover, GeoLocation, MultiValueAnswer, GeoProximityLocation, or CidrRoutingConfig], but found more than one in Change with " + changeContext(c)
	case n == 1 && rr.SetIdentifier == "":
		return "Invalid request: Missing field 'SetIdentifier' in Change with " + changeContext(c)
	case n == 0 && rr.SetIdentifier != "":
		return "Invalid request: Expected exactly one of [Weight, Region, Failover, GeoLocation, MultiValueAnswer, GeoProximityLocation, or CidrRoutingConfig], but found none in Change with " + changeContext(c)
	}
	if rr.Weight != nil && (*rr.Weight < 0 || *rr.Weight > 255) {
		return fmt.Sprintf("1 validation error detected: Value '%d' at 'changeBatch.changes.member.resourceRecordSet.weight' failed to satisfy constraint: Member must have value between 0 and 255", *rr.Weight)
	}
	if rr.Failover != "" && rr.Failover != "PRIMARY" && rr.Failover != "SECONDARY" {
		return fmt.Sprintf("1 validation error detected: Value '%s' at 'changeBatch.changes.member.resourceRecordSet.failover' failed to satisfy constraint: Member must satisfy enum value set: [PRIMARY, SECONDARY]", rr.Failover)
	}
	if g := rr.GeoLocation; g != nil {
		if g.ContinentCode == "" && g.CountryCode == "" {
			return "Invalid request: GeoLocation must specify ContinentCode or CountryCode in Change with " + changeContext(c)
		}
		if g.ContinentCode != "" && (g.CountryCode != "" || g.SubdivisionCode != "") {
			return "Invalid request: GeoLocation cannot specify both ContinentCode and CountryCode in Change with " + changeContext(c)
		}
		if g.SubdivisionCode != "" && g.CountryCode == "" {
			return "Invalid request: GeoLocation SubdivisionCode requires CountryCode in Change with " + changeContext(c)
		}
	}
	if rr.MultiValueAnswer != nil && hasAlias {
		return "Invalid request: Multivalue answer record sets cannot be alias record sets in Change with " + changeContext(c)
	}
	return ""
}

// normalizeRecordSet puts a record set into the form Route 53 stores.
func normalizeRecordSet(rr ResourceRecordSet) ResourceRecordSet {
	rr.Name = normalizeName(rr.Name)
	if rr.AliasTarget != nil {
		a := *rr.AliasTarget
		a.HostedZoneID = cleanZoneID(a.HostedZoneID)
		a.DNSName = normalizeName(a.DNSName)
		rr.AliasTarget = &a
	}
	return rr
}

// sameRecordSet is the DELETE match: every value has to be what is stored.
func sameRecordSet(a, b ResourceRecordSet) bool {
	canon := func(rr ResourceRecordSet) ResourceRecordSet {
		vs := append([]ResourceRecord(nil), rr.ResourceRecords...)
		sort.Slice(vs, func(i, j int) bool { return vs[i].Value < vs[j].Value })
		if len(vs) == 0 {
			vs = nil
		}
		rr.ResourceRecords = vs
		if rr.MultiValueAnswer != nil && !*rr.MultiValueAnswer {
			rr.MultiValueAnswer = nil
		}
		return rr
	}
	return reflect.DeepEqual(canon(a), canon(b))
}

// batch is the working copy of a zone's record sets that a change batch is
// applied to; nothing is written unless every change succeeds.
type batch struct {
	h      *Handler
	ns     string
	zone   *zone
	sets   map[string]ResourceRecordSet
	errors []string
}

func (b *batch) fail(format string, args ...any) {
	b.errors = append(b.errors, fmt.Sprintf(format, args...))
}

func (b *batch) apply(c Change) {
	rr := c.ResourceRecordSet
	key := recordKey(b.zone.ID, rr)
	cur, exists := b.sets[key]
	switch c.Action {
	case "CREATE":
		if exists {
			b.fail("Tried to create resource record set %s but it already exists", describe(rr))
			return
		}
		if b.check(rr, key) {
			b.sets[key] = rr
		}
	case "UPSERT":
		if b.check(rr, key) {
			b.sets[key] = rr
		}
	case "DELETE":
		switch {
		case !exists:
			b.fail("Tried to delete resource record set %s but it was not found", describe(rr))
		case !sameRecordSet(cur, rr):
			b.fail("Tried to delete resource record set %s but the values provided do not match the current values", describe(rr))
		case isApexDefault(rr, b.zone.Name) && rr.Type == "NS":
			b.fail("A HostedZone must contain at least one NS record for the zone itself.")
		case isApexDefault(rr, b.zone.Name):
			b.fail("A HostedZone must contain exactly one SOA record.")
		default:
			delete(b.sets, key)
		}
	}
}

// check validates a record set that is about to be written at key against
// the rest of the zone.
func (b *batch) check(rr ResourceRecordSet, key string) bool {
	before := len(b.errors)
	zoneName := b.zone.Name
	if !inZone(rr.Name, zoneName) {
		b.fail("RRSet with DNS name %s is not permitted in zone %s", rr.Name, zoneName)
		return false
	}
	if rr.Type == "SOA" && rr.Name != zoneName {
		b.fail("RRSet of type SOA with DNS name %s is not permitted because SOA records are only allowed at the zone apex in zone %s", rr.Name, zoneName)
	}
	if rr.Name == zoneName && rr.Type == "CNAME" {
		b.fail("RRSet of type CNAME with DNS name %s is not permitted at apex in zone %s", rr.Name, zoneName)
	}
	if rr.Type == "CNAME" && len(rr.ResourceRecords) > 1 {
		b.fail("RRSet of type CNAME with DNS name %s is not permitted because it has more than one value", rr.Name)
	}
	if rr.Type == "SOA" && len(rr.ResourceRecords) != 1 {
		b.fail("RRSet of type SOA with DNS name %s must have exactly one value", rr.Name)
	}

	seen := map[string]bool{}
	for _, v := range rr.ResourceRecords {
		if seen[v.Value] {
			b.fail("[Duplicate Resource Record: '%s']", v.Value)
		}
		seen[v.Value] = true
		if msg := validateValue(rr.Type, v.Value); msg != "" {
			b.errors = append(b.errors, msg)
		}
	}

	policy := routingPolicy(rr)
	for k, other := range b.sets {
		if k == key || other.Name != rr.Name {
			continue
		}
		if rr.Type == "CNAME" && other.Type != "CNAME" {
			b.fail("RRSet of type CNAME with DNS name %s is not permitted as it conflicts with other records with the same DNS name in zone %s", rr.Name, zoneName)
			break
		}
		if rr.Type != "CNAME" && other.Type == "CNAME" {
			b.fail("RRSet of type %s with DNS name %s is not permitted because a conflicting RRSet of type CNAME with the same DNS name already exists in zone %s", rr.Type, rr.Name, zoneName)
			break
		}
		if other.Type != rr.Type {
			continue
		}
		if op := routingPolicy(other); op != policy {
			if policy == "" {
				b.fail("RRSet with DNS name %s, type %s cannot be created because %s record sets exist with the same name and type", rr.Name, rr.Type, op)
			} else if op == "" {
				b.fail("RRSet with DNS name %s, type %s, SetIdentifier %s cannot be created because a non-%s set exists with the same name and type", rr.Name, rr.Type, rr.SetIdentifier, policy)
			} else {
				b.fail("RRSet with DNS name %s, type %s, SetIdentifier %s cannot be created because a %s set exists with the same name and type", rr.Name, rr.Type, rr.SetIdentifier, op)
			}
			break
		}
		if rr.Failover != "" && other.Failover == rr.Failover {
			b.fail("RRSet with DNS name %s, type %s already has a %s failover record set", rr.Name, rr.Type, rr.Failover)
			break
		}
		if g := rr.GeoLocation; g != nil && reflect.DeepEqual(*g, *other.GeoLocation) {
			b.fail("RRSet with DNS name %s, type %s already has a record set for that location", rr.Name, rr.Type)
			break
		}
	}

	if a := rr.AliasTarget; a != nil {
		b.checkAlias(rr, a)
	}
	if rr.HealthCheckID != "" {
		var hc healthCheck
		if !b.h.load(b.ns, typeHealthCheck, rr.HealthCheckID, &hc) {
			b.fail("The health check with ID %s does not exist", rr.HealthCheckID)
		}
	}
	return len(b.errors) == before
}

// checkAlias requires an alias into one of the account's own zones to point
// at an existing record set of the same type. Targets in AWS service zones
// (CloudFront, S3 websites, load balancers and so on) are accepted as given.
func (b *batch) checkAlias(rr ResourceRecordSet, a *AliasTarget) {
	var target *zone
	var sets []ResourceRecordSet
	if a.HostedZoneID == b.zone.ID {
		target = b.zone
		for _, s := range b.sets {
			sets = append(sets, s)
		}
	} else if z, ok := b.h.getZone(b.ns, a.HostedZoneID); ok {
		target = z
		sets = b.h.zoneRecords(b.ns, z.ID)
	} else {
		return
	}
	if !inZone(a.DNSName, target.Name) {
		b.fail("Tried to create an alias that targets %s, type %s in zone %s, but the alias target name does not lie within the target zone", a.DNSName, rr.Type, a.HostedZoneID)
		return
	}
	if a.DNSName == rr.Name && target.ID == b.zone.ID {
		b.fail("Tried to create an alias that targets %s, type %s in zone %s, but that is the alias itself", a.DNSName, rr.Type, a.HostedZoneID)
		return
	}
	for _, s := range sets {
		if s.Name == a.DNSName && s.Type == rr.Type {
			return
		}
	}
	b.fail("Tried to create an alias that targets %s, type %s in zone %s, but that target was not found", a.DNSName, rr.Type, a.HostedZoneID)
}

func (h *Handler) ChangeResourceRecordSets(w http.ResponseWriter, r *http.Request, zoneID string) {
	ns := util.GlobalNamespace(r)
	var req ChangeResourceRecordSetsRequest
	if err := decodeBody(r, &req); err != nil {
		writeInvalidInput(w, "Could not parse ChangeResourceRecordSetsRequest: "+err.Error())
		return
	}
	changes := req.ChangeBatch.Changes
	if len(changes) == 0 {
		writeInvalidInput(w, "1 validation error detected: Value '[]' at 'changeBatch.changes' failed to satisfy constraint: Member must have length greater than or equal to 1")
		return
	}
	if len(changes) > 1000 {
		writeInvalidInput(w, "1 validation error detected: Value at 'changeBatch.changes' failed to satisfy constraint: Member must have length less than or equal to 1000")
		return
	}
	for i := range changes {
		if msg := checkInput(changes[i]); msg != "" {
			writeInvalidInput(w, msg)
			return
		}
		changes[i].ResourceRecordSet = normalizeRecordSet(changes[i].ResourceRecordSet)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	z, ok := h.getZone(ns, zoneID)
	if !ok {
		writeNoSuchZone(w, zoneID)
		return
	}
	existing := map[string]ResourceRecordSet{}
	b := &batch{h: h, ns: ns, zone: z, sets: map[string]ResourceRecordSet{}}
	for _, rr := range h.zoneRecords(ns, z.ID) {
		k := recordKey(z.ID, rr)
		existing[k] = rr
		b.sets[k] = rr
	}
	for _, c := range changes {
		b.apply(c)
	}
	if len(b.errors) > 0 {
		writeInvalidChangeBatch(w, b.errors)
		return
	}

	// Validation passed; write the difference.
	for k := range existing {
		if _, ok := b.sets[k]; !ok {
			if err := h.Store.Delete(k, service, typeRecord, ns); err != nil {
				writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
				return
			}
		}
	}
	for k, rr := range b.sets {
		old, had := existing[k]
		if had && reflect.DeepEqual(old, rr) {
			continue
		}
		if err := h.save(ns, typeRecord, k, rr, !had); err != nil {
			writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
			return
		}
	}
	writeXML(w, http.StatusOK, ChangeResourceRecordSetsResponse{Xmlns: xmlns, ChangeInfo: h.newChange(req.ChangeBatch.Comment)})
}

func (h *Handler) ListResourceRecordSets(w http.ResponseWriter, r *http.Request, zoneID string) {
	ns := util.GlobalNamespace(r)
	q := r.URL.Query()
	limit, ok := maxItems(r, 300)
	if !ok {
		writeInvalidInput(w, "maxitems must be a positive integer")
		return
	}
	startName, startType, startID := q.Get("name"), q.Get("type"), q.Get("identifier")
	if startType != "" && startName == "" {
		writeInvalidInput(w, "The input is not valid: you must specify a name when you specify a type")
		return
	}
	if startID != "" && startType == "" {
		writeInvalidInput(w, "The input is not valid: you must specify a name and type when you specify an identifier")
		return
	}
	if startType != "" && !validTypes[startType] {
		writeInvalidInput(w, "Invalid type: "+startType)
		return
	}
	z, found := h.getZone(ns, zoneID)
	if !found {
		writeNoSuchZone(w, zoneID)
		return
	}
	sets := h.zoneRecords(ns, z.ID)
	start := 0
	if startName != "" {
		startName = normalizeName(startName)
		start = sort.Search(len(sets), func(i int) bool {
			s := sets[i]
			return compareRecordKeys(s.Name, s.Type, s.SetIdentifier, startName, startType, startID) >= 0
		})
	}
	out := ListResourceRecordSetsResponse{Xmlns: xmlns, MaxItems: limit, ResourceRecordSets: []ResourceRecordSet{}}
	for i := start; i < len(sets); i++ {
		if len(out.ResourceRecordSets) == limit {
			out.IsTruncated = true
			out.NextRecordName, out.NextRecordType, out.NextRecordIdentifier = sets[i].Name, sets[i].Type, sets[i].SetIdentifier
			break
		}
		out.ResourceRecordSets = append(out.ResourceRecordSets, sets[i])
	}
	writeXML(w, http.StatusOK, out)
}
