// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3_test

import (
	"encoding/xml"
	"reflect"
	"strings"
	"testing"

	"opensnack/internal/api/s3"
)

func replicationXML(rules string) string {
	return `<ReplicationConfiguration><Role>arn:aws:iam::000000000000:role/repl</Role>` + rules + `</ReplicationConfiguration>`
}

func TestReplicationConfigRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.versioned("src", "Enabled")
	e.versioned("dst", "Enabled")
	body := replicationXML(`<Rule><ID>presents</ID><Priority>2</Priority><Status>Enabled</Status>
<Filter><And><Prefix>presents/</Prefix><Tag><Key>a</Key><Value>1</Value></Tag></And></Filter>
<Destination><Bucket>arn:aws:s3:::dst</Bucket><StorageClass>STANDARD_IA</StorageClass>
<ReplicationTime><Status>Enabled</Status><Time><Minutes>15</Minutes></Time></ReplicationTime>
<Metrics><Status>Enabled</Status><EventThreshold><Minutes>15</Minutes></EventThreshold></Metrics></Destination>
<DeleteMarkerReplication><Status>Disabled</Status></DeleteMarkerReplication></Rule>
<Rule><ID>all</ID><Priority>1</Priority><Status>Disabled</Status><Filter></Filter>
<Destination><Bucket>arn:aws:s3:::dst</Bucket></Destination>
<DeleteMarkerReplication><Status>Enabled</Status></DeleteMarkerReplication></Rule>`)
	e.must(200, e.do("PUT", "/src?replication", body))

	var sent, got s3.ReplicationConfiguration
	if err := xml.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	rec := e.must(200, e.do("GET", "/src?replication", ""))
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if sent.Role != got.Role || !reflect.DeepEqual(sent.Rules, got.Rules) {
		t.Errorf("configuration changed:\nsent %s\ngot  %s", body, rec.Body.String())
	}

	// Versioning can't be suspended while replication is configured.
	e.must(409, e.do("PUT", "/src?versioning", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`))
	e.must(204, e.do("DELETE", "/src?replication", ""))
	e.must(404, e.do("GET", "/src?replication", ""))
}

func TestReplicationValidation(t *testing.T) {
	e := newEnv(t)
	e.versioned("src", "Enabled")
	e.versioned("dst", "Enabled")
	e.must(200, e.do("PUT", "/plain", ""))
	e.versioned("off", "Suspended")
	rule := func(dest, extra string) string {
		return replicationXML(`<Rule><ID>r</ID><Status>Enabled</Status>` + extra +
			`<Destination><Bucket>arn:aws:s3:::` + dest + `</Bucket></Destination></Rule>`)
	}
	dm := `<Filter></Filter><DeleteMarkerReplication><Status>Disabled</Status></DeleteMarkerReplication>`
	for name, c := range map[string]struct{ bucket, body string }{
		"unversioned source":     {"plain", rule("dst", dm)},
		"suspended source":       {"off", rule("dst", dm)},
		"missing destination":    {"src", rule("nope", dm)},
		"unversioned dest":       {"src", rule("plain", dm)},
		"same bucket":            {"src", rule("src", dm)},
		"filter without markers": {"src", rule("dst", `<Filter></Filter>`)},
		"prefix and filter":      {"src", rule("dst", `<Prefix>a</Prefix>`+dm)},
		"bad status":             {"src", strings.Replace(rule("dst", dm), "Enabled", "On", 1)},
		"duplicate priority": {"src", replicationXML(`<Rule><ID>a</ID><Priority>1</Priority><Status>Enabled</Status>` + dm +
			`<Destination><Bucket>arn:aws:s3:::dst</Bucket></Destination></Rule><Rule><ID>b</ID><Priority>1</Priority><Status>Enabled</Status>` + dm +
			`<Destination><Bucket>arn:aws:s3:::dst</Bucket></Destination></Rule>`)},
	} {
		if rec := e.do("PUT", "/"+c.bucket+"?replication", c.body); rec.Code != 400 {
			t.Errorf("%s: want 400, got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	e.must(400, e.do("PUT", "/src?replication", `<ReplicationConfiguration><Rule><Status>Enabled</Status></Rule></ReplicationConfiguration>`))
}

func TestReplicationCopiesObjects(t *testing.T) {
	e := newEnv(t)
	e.versioned("home", "Enabled")
	e.versioned("stop", "Enabled")
	e.must(200, e.do("PUT", "/home/before", "old"))
	e.must(200, e.do("PUT", "/home?replication", replicationXML(`<Rule><ID>p</ID><Priority>1</Priority><Status>Enabled</Status>
<Filter><Prefix>presents/</Prefix></Filter>
<Destination><Bucket>arn:aws:s3:::stop</Bucket><StorageClass>STANDARD_IA</StorageClass></Destination>
<DeleteMarkerReplication><Status>Enabled</Status></DeleteMarkerReplication></Rule>`)))

	put := e.must(200, e.do("PUT", "/home/presents/bike", "bike", "Content-Type", "text/plain", "X-Amz-Meta-For", "alf"))
	vid := put.Header().Get("X-Amz-Version-Id")
	if s := e.must(200, e.do("HEAD", "/home/presents/bike", "")).Header().Get("X-Amz-Replication-Status"); s != "COMPLETED" {
		t.Errorf("source status %q", s)
	}
	got := e.must(200, e.do("GET", "/stop/presents/bike", ""))
	if got.Body.String() != "bike" || got.Header().Get("X-Amz-Version-Id") != vid ||
		got.Header().Get("X-Amz-Replication-Status") != "REPLICA" || got.Header().Get("X-Amz-Meta-For") != "alf" ||
		got.Header().Get("X-Amz-Storage-Class") != "STANDARD_IA" {
		t.Fatalf("replica: %q %v", got.Body.String(), got.Header())
	}

	// Outside the filter, already there before, or a copy: as S3 does.
	e.must(200, e.do("PUT", "/home/other", "x"))
	e.must(404, e.do("GET", "/stop/other", ""))
	e.must(404, e.do("GET", "/stop/before", ""))
	e.must(200, e.do("PUT", "/home/presents/copy", "", "X-Amz-Copy-Source", "/home/before"))
	if got := e.must(200, e.do("GET", "/stop/presents/copy", "")).Body.String(); got != "old" {
		t.Errorf("copied object replica %q", got)
	}

	// Delete markers follow when the rule says so; version deletes never do.
	del := e.must(204, e.do("DELETE", "/home/presents/bike", ""))
	e.must(404, e.do("GET", "/stop/presents/bike", ""))
	lv := e.versions("stop")
	if len(lv.DeleteMarkers) != 1 || lv.DeleteMarkers[0].VersionID != del.Header().Get("X-Amz-Version-Id") {
		t.Fatalf("replicated markers %+v", lv.DeleteMarkers)
	}
	e.must(204, e.do("DELETE", "/home/presents/bike?versionId="+vid, ""))
	e.must(200, e.do("GET", "/stop/presents/bike?versionId="+vid, ""))

	// A replica isn't replicated again, even with a rule pointing back.
	e.must(200, e.do("PUT", "/stop?replication", replicationXML(`<Rule><ID>back</ID><Status>Enabled</Status><Prefix></Prefix>
<Destination><Bucket>arn:aws:s3:::home</Bucket></Destination></Rule>`)))
	e.must(200, e.do("PUT", "/home/presents/sled", "sled"))
	// before, other, presents/copy and presents/sled; bike's version was deleted.
	if n := len(e.versions("home").Versions); n != 4 {
		t.Errorf("home has %d versions, want 4", n)
	}
	e.must(200, e.do("PUT", "/stop/presents/back", "back"))
	e.must(200, e.do("GET", "/home/presents/back", ""))
}
