// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package route53

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/resource"
)

type client struct {
	t *testing.T
	h *Handler
}

func newClient(t *testing.T) *client {
	return &client{t: t, h: NewHandler(resource.NewMemStore())}
}

func (c *client) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/2013-04-01"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	c.h.Dispatch(rec, req)
	return rec
}

func (c *client) ok(method, path, body string, out any) {
	c.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code/100 != 2 {
		c.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	if out != nil {
		if err := xml.Unmarshal(rec.Body.Bytes(), out); err != nil {
			c.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

func (c *client) createZone(name string) string {
	c.t.Helper()
	var out CreateHostedZoneResponse
	c.ok("POST", "/hostedzone", fmt.Sprintf(`<CreateHostedZoneRequest xmlns="%s"><Name>%s</Name><CallerReference>%s</CallerReference></CreateHostedZoneRequest>`, xmlns, name, name), &out)
	return cleanZoneID(out.HostedZone.ID)
}

func changeXML(changes ...string) string {
	return fmt.Sprintf(`<ChangeResourceRecordSetsRequest xmlns="%s"><ChangeBatch><Changes>%s</Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`, xmlns, strings.Join(changes, ""))
}

func rrXML(action, name, typ string, ttl int, values ...string) string {
	var rrs strings.Builder
	for _, v := range values {
		fmt.Fprintf(&rrs, "<ResourceRecord><Value>%s</Value></ResourceRecord>", xmlEscape(v))
	}
	return fmt.Sprintf(`<Change><Action>%s</Action><ResourceRecordSet><Name>%s</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords>%s</ResourceRecords></ResourceRecordSet></Change>`, action, name, typ, ttl, rrs.String())
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (c *client) list(zoneID, query string) ListResourceRecordSetsResponse {
	c.t.Helper()
	var out ListResourceRecordSetsResponse
	c.ok("GET", "/hostedzone/"+zoneID+"/rrset"+query, "", &out)
	return out
}

func names(sets []ResourceRecordSet) []string {
	var out []string
	for _, s := range sets {
		n := s.Name + " " + s.Type
		if s.SetIdentifier != "" {
			n += " " + s.SetIdentifier
		}
		out = append(out, n)
	}
	return out
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), "<InvalidChangeBatch") {
		var icb invalidChangeBatchResponse
		if err := xml.Unmarshal(rec.Body.Bytes(), &icb); err != nil {
			t.Fatalf("decode error: %v: %s", err, rec.Body.String())
		}
		return "InvalidChangeBatch", strings.Join(icb.Messages, "\n")
	}
	var e errorResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error: %v: %s", err, rec.Body.String())
	}
	return e.Error.Code, e.Error.Message
}

func TestCreateZoneHasDefaultRecords(t *testing.T) {
	c := newClient(t)
	id := c.createZone("Example.COM")

	var z GetHostedZoneResponse
	c.ok("GET", "/hostedzone/"+id, "", &z)
	if z.HostedZone.Name != "example.com." || z.HostedZone.ResourceRecordSetCount != 2 {
		t.Fatalf("zone: %+v", z.HostedZone)
	}
	if z.DelegationSet == nil || len(z.DelegationSet.NameServers) != 4 || z.DelegationSet.ID != "" {
		t.Fatalf("delegation set: %+v", z.DelegationSet)
	}
	got := names(c.list(id, "").ResourceRecordSets)
	if strings.Join(got, ",") != "example.com. NS,example.com. SOA" {
		t.Fatalf("records: %v", got)
	}
}

func TestChangeBatchIsAtomic(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "www.example.com", "A", 300, "192.0.2.1")), nil)

	// The second change fails, so the first must not be applied either.
	rec := c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(
		rrXML("CREATE", "api.example.com", "A", 300, "192.0.2.2"),
		rrXML("CREATE", "www.example.com", "A", 300, "192.0.2.3"),
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
	code, msg := errorCode(t, rec)
	if code != "InvalidChangeBatch" || !strings.Contains(msg, "Tried to create resource record set [name='www.example.com.', type='A'] but it already exists") {
		t.Fatalf("error: %s %s", code, msg)
	}
	if got := names(c.list(id, "").ResourceRecordSets); len(got) != 3 {
		t.Fatalf("batch was partly applied: %v", got)
	}

	// DELETE of a missing record fails; DELETE with the wrong values fails.
	rec = c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("DELETE", "nope.example.com", "A", 300, "192.0.2.1")))
	if code, msg := errorCode(t, rec); code != "InvalidChangeBatch" || !strings.Contains(msg, "but it was not found") {
		t.Fatalf("delete missing: %s %s", code, msg)
	}
	rec = c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("DELETE", "www.example.com", "A", 60, "192.0.2.1")))
	if code, msg := errorCode(t, rec); code != "InvalidChangeBatch" || !strings.Contains(msg, "do not match") {
		t.Fatalf("delete mismatch: %s %s", code, msg)
	}

	// DELETE then CREATE of the same record in one batch is how the provider
	// renames a type; it must work.
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(
		rrXML("DELETE", "www.example.com", "A", 300, "192.0.2.1"),
		rrXML("CREATE", "www.example.com", "A", 60, "192.0.2.9"),
	), nil)
	out := c.list(id, "?name=www.example.com&type=A&maxitems=1")
	if rr := out.ResourceRecordSets[0]; *rr.TTL != 60 || rr.ResourceRecords[0].Value != "192.0.2.9" {
		t.Fatalf("after replace: %+v", rr)
	}

	// UPSERT replaces in place.
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("UPSERT", "www.example.com", "A", 120, "192.0.2.10", "192.0.2.11")), nil)
	out = c.list(id, "?name=www.example.com&type=A&maxitems=1")
	if rr := out.ResourceRecordSets[0]; *rr.TTL != 120 || len(rr.ResourceRecords) != 2 {
		t.Fatalf("after upsert: %+v", rr)
	}

	// Several failures come back together in the InvalidChangeBatch document.
	rec = c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(
		rrXML("CREATE", "bad.example.com", "A", 300, "not-an-ip"),
		rrXML("CREATE", "www.other.org", "A", 300, "192.0.2.1"),
	))
	var icb invalidChangeBatchResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &icb); err != nil || len(icb.Messages) != 2 {
		t.Fatalf("multi error: %v %s", err, rec.Body.String())
	}
}

func TestRecordValidation(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	cases := []struct {
		change, want string
	}{
		{rrXML("CREATE", "example.com", "CNAME", 300, "foo.example.org"), "not permitted at apex"},
		{rrXML("CREATE", "a.example.com", "AAAA", 300, "192.0.2.1"), "IllegalIPv6Address"},
		{rrXML("CREATE", "a.example.com", "MX", 300, "mail.example.com"), "MXRRDATA"},
		{rrXML("CREATE", "a.example.com", "TXT", 300, `"`+strings.Repeat("x", 256)+`"`), "CharacterStringTooLong"},
		{rrXML("DELETE", "example.com", "NS", 172800, "x."), "do not match"},
	}
	for _, tc := range cases {
		rec := c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(tc.change))
		if _, msg := errorCode(t, rec); rec.Code != 400 || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: got %d %s", tc.want, rec.Code, msg)
		}
	}

	// A long TXT value split into quoted strings is fine, and is returned
	// exactly as sent.
	long := `"` + strings.Repeat("a", 255) + `" "` + strings.Repeat("b", 10) + `"`
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "txt.example.com", "TXT", 300, long, `"v=spf1 -all"`)), nil)
	rr := c.list(id, "?name=txt.example.com&type=TXT").ResourceRecordSets[0]
	if rr.ResourceRecords[0].Value != long && rr.ResourceRecords[1].Value != long {
		t.Fatalf("TXT not preserved: %+v", rr.ResourceRecords)
	}

	// CNAME can't share a name with another type.
	rec := c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "txt.example.com", "CNAME", 300, "x.example.org")))
	if _, msg := errorCode(t, rec); !strings.Contains(msg, "conflicts with other records") {
		t.Fatalf("CNAME conflict: %s", msg)
	}

	// Missing TTL is a request error, not a batch error.
	rec = c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(`<Change><Action>CREATE</Action><ResourceRecordSet><Name>x.example.com</Name><Type>A</Type><ResourceRecords><ResourceRecord><Value>192.0.2.1</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change>`))
	if code, _ := errorCode(t, rec); code != "InvalidInput" {
		t.Fatalf("missing TTL: %s", code)
	}
}

func TestNameNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM":       "example.com.",
		"example.com.":      "example.com.",
		"*.example.com":     `\052.example.com.`,
		`\052.example.com.`: `\052.example.com.`,
		"a b.example.com":   `a\040b.example.com.`,
		"_dmarc.example":    "_dmarc.example.",
	} {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	c := newClient(t)
	id := c.createZone("example.com")
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "*.Example.com", "A", 300, "192.0.2.1")), nil)
	if rr := c.list(id, `?name=*.example.com&type=A&maxitems=1`).ResourceRecordSets[0]; rr.Name != `\052.example.com.` {
		t.Fatalf("wildcard stored as %q", rr.Name)
	}
}

func TestAliasRecords(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	alias := func(action, name, zone, target string) string {
		return fmt.Sprintf(`<Change><Action>%s</Action><ResourceRecordSet><Name>%s</Name><Type>A</Type><AliasTarget><HostedZoneId>%s</HostedZoneId><DNSName>%s</DNSName><EvaluateTargetHealth>false</EvaluateTargetHealth></AliasTarget></ResourceRecordSet></Change>`, action, name, zone, target)
	}

	// The target must exist when it is in one of the account's own zones...
	rec := c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(alias("CREATE", "app.example.com", id, "www.example.com")))
	if _, msg := errorCode(t, rec); !strings.Contains(msg, "but that target was not found") {
		t.Fatalf("missing target: %s", msg)
	}
	// ...but can be created in the same batch.
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(
		rrXML("CREATE", "www.example.com", "A", 300, "192.0.2.1"),
		alias("CREATE", "app.example.com", "/hostedzone/"+id, "WWW.example.com"),
	), nil)
	rr := c.list(id, "?name=app.example.com&type=A&maxitems=1").ResourceRecordSets[0]
	if rr.AliasTarget == nil || rr.AliasTarget.DNSName != "www.example.com." || rr.AliasTarget.HostedZoneID != id || rr.TTL != nil {
		t.Fatalf("alias: %+v %+v", rr, rr.AliasTarget)
	}

	// AWS service zones (here CloudFront) are accepted as given.
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(alias("CREATE", "cdn.example.com", "Z2FDTNDATAQYW2", "d111111abcdef8.cloudfront.net")), nil)

	// Alias and TTL together is a request error.
	rec = c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(`<Change><Action>CREATE</Action><ResourceRecordSet><Name>x.example.com</Name><Type>A</Type><TTL>60</TTL><AliasTarget><HostedZoneId>Z2FDTNDATAQYW2</HostedZoneId><DNSName>d.cloudfront.net</DNSName><EvaluateTargetHealth>false</EvaluateTargetHealth></AliasTarget></ResourceRecordSet></Change>`))
	if code, _ := errorCode(t, rec); code != "InvalidInput" {
		t.Fatalf("alias with TTL: %s", code)
	}

	// The provider deletes exactly what it listed.
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(alias("DELETE", "app.example.com", id, "www.example.com.")), nil)
}

func TestWeightedRecordsAndPagination(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	weighted := func(id string, w int, v string) string {
		return fmt.Sprintf(`<Change><Action>CREATE</Action><ResourceRecordSet><Name>w.example.com</Name><Type>A</Type><SetIdentifier>%s</SetIdentifier><Weight>%d</Weight><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>%s</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change>`, id, w, v)
	}
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(
		weighted("blue", 90, "192.0.2.1"),
		weighted("green", 10, "192.0.2.2"),
		rrXML("CREATE", "a.example.com", "TXT", 300, `"hello"`),
		rrXML("CREATE", "a.example.com", "A", 300, "192.0.2.3"),
		rrXML("CREATE", "b.a.example.com", "A", 300, "192.0.2.4"),
		rrXML("CREATE", "z.example.com", "MX", 300, "10 mail.example.com"),
	), nil)

	// A simple record can't join a weighted set.
	rec := c.do("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "w.example.com", "A", 60, "192.0.2.9")))
	if _, msg := errorCode(t, rec); !strings.Contains(msg, "weighted record sets exist") {
		t.Fatalf("mixed routing: %s", msg)
	}

	want := []string{
		"example.com. NS", "example.com. SOA",
		"a.example.com. A", "a.example.com. TXT", "b.a.example.com. A",
		"w.example.com. A blue", "w.example.com. A green",
		"z.example.com. MX",
	}
	if got := names(c.list(id, "").ResourceRecordSets); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}

	// Page through two at a time with the Next* markers.
	var got []string
	query := "?maxitems=2"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		page := c.list(id, query)
		got = append(got, names(page.ResourceRecordSets)...)
		if !page.IsTruncated {
			break
		}
		query = "?maxitems=2&name=" + page.NextRecordName + "&type=" + page.NextRecordType
		if page.NextRecordIdentifier != "" {
			query += "&identifier=" + page.NextRecordIdentifier
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paged:\n got %v\nwant %v", got, want)
	}

	// Starting at a name and type that don't exist starts at the next one.
	if page := c.list(id, "?name=a.example.com&type=CNAME&maxitems=1"); page.ResourceRecordSets[0].Type != "TXT" || !page.IsTruncated || page.NextRecordName != "b.a.example.com." {
		t.Fatalf("start: %+v", page)
	}
}

func TestDeleteZoneRequiresEmpty(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(rrXML("CREATE", "www.example.com", "A", 300, "192.0.2.1")), nil)
	rec := c.do("DELETE", "/hostedzone/"+id, "")
	if code, _ := errorCode(t, rec); code != "HostedZoneNotEmpty" {
		t.Fatalf("delete non-empty: %d %s", rec.Code, code)
	}

	// force_destroy: list everything, delete all but the apex NS and SOA,
	// then delete the zone.
	var del []string
	for _, rr := range c.list(id, "").ResourceRecordSets {
		if isApexDefault(rr, "example.com.") {
			continue
		}
		b, _ := xml.Marshal(Change{Action: "DELETE", ResourceRecordSet: rr})
		del = append(del, string(b))
	}
	c.ok("POST", "/hostedzone/"+id+"/rrset", changeXML(del...), nil)
	c.ok("DELETE", "/hostedzone/"+id, "", nil)
	if rec := c.do("GET", "/hostedzone/"+id, ""); rec.Code != 404 {
		t.Fatalf("zone still there: %d", rec.Code)
	}
	left, _ := c.h.Store.List(service, typeRecord, "default")
	if len(left) != 0 {
		t.Fatalf("records left behind: %d", len(left))
	}
}

func TestTags(t *testing.T) {
	c := newClient(t)
	id := c.createZone("example.com")
	c.ok("POST", "/tags/hostedzone/"+id, `<ChangeTagsForResourceRequest><AddTags><Tag><Key>Team</Key><Value>team</Value></Tag><Tag><Key>Env</Key><Value>dev</Value></Tag></AddTags></ChangeTagsForResourceRequest>`, nil)
	c.ok("POST", "/tags/hostedzone/"+id, `<ChangeTagsForResourceRequest><RemoveTagKeys><Key>Env</Key></RemoveTagKeys></ChangeTagsForResourceRequest>`, nil)
	var out ListTagsForResourceResponse
	c.ok("GET", "/tags/hostedzone/"+id, "", &out)
	if len(out.ResourceTagSet.Tags) != 1 || out.ResourceTagSet.Tags[0] != (Tag{"Team", "team"}) || out.ResourceTagSet.ResourceID != id {
		t.Fatalf("tags: %+v", out.ResourceTagSet)
	}
	var many ListTagsForResourcesResponse
	c.ok("POST", "/tags/hostedzone", `<ListTagsForResourcesRequest><ResourceIds><ResourceId>`+id+`</ResourceId></ResourceIds></ListTagsForResourcesRequest>`, &many)
	if len(many.ResourceTagSets) != 1 || len(many.ResourceTagSets[0].Tags) != 1 {
		t.Fatalf("tags for resources: %+v", many)
	}
	if rec := c.do("GET", "/tags/hostedzone/ZNOPE", ""); rec.Code != 404 {
		t.Fatalf("missing zone tags: %d", rec.Code)
	}
}

func TestHealthCheckLifecycle(t *testing.T) {
	c := newClient(t)
	var created HealthCheckResponse
	c.ok("POST", "/healthcheck", `<CreateHealthCheckRequest><CallerReference>r1</CallerReference><HealthCheckConfig><Type>HTTP</Type><FullyQualifiedDomainName>example.com</FullyQualifiedDomainName><Port>80</Port><ResourcePath>/</ResourcePath><RequestInterval>30</RequestInterval></HealthCheckConfig></CreateHealthCheckRequest>`, &created)
	hc := created.HealthCheck
	if *hc.HealthCheckConfig.FailureThreshold != 3 || hc.HealthCheckVersion != 1 {
		t.Fatalf("defaults: %+v", hc)
	}
	var updated HealthCheckResponse
	c.ok("POST", "/healthcheck/"+hc.ID, `<UpdateHealthCheckRequest><FailureThreshold>5</FailureThreshold></UpdateHealthCheckRequest>`, &updated)
	if *updated.HealthCheck.HealthCheckConfig.FailureThreshold != 5 || updated.HealthCheck.HealthCheckVersion != 2 {
		t.Fatalf("update: %+v", updated.HealthCheck)
	}

	zone := c.createZone("example.com")
	c.ok("POST", "/hostedzone/"+zone+"/rrset", changeXML(`<Change><Action>CREATE</Action><ResourceRecordSet><Name>f.example.com</Name><Type>A</Type><SetIdentifier>p</SetIdentifier><Failover>PRIMARY</Failover><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>192.0.2.1</Value></ResourceRecord></ResourceRecords><HealthCheckId>`+hc.ID+`</HealthCheckId></ResourceRecordSet></Change>`), nil)
	if code, _ := errorCode(t, c.do("DELETE", "/healthcheck/"+hc.ID, "")); code != "HealthCheckInUse" {
		t.Fatalf("delete in use: %s", code)
	}
}
