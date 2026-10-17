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

func TestLifecycleRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/lc", ""))

	// What aws_s3_bucket_lifecycle_configuration sends: every action, an
	// empty filter, a tag filter and an And filter.
	body := `<LifecycleConfiguration>
<Rule><ID>old-versions</ID><Status>Enabled</Status><Filter></Filter>
  <NoncurrentVersionExpiration><NoncurrentDays>30</NoncurrentDays><NewerNoncurrentVersions>3</NewerNoncurrentVersions></NoncurrentVersionExpiration>
  <NoncurrentVersionTransition><NoncurrentDays>7</NoncurrentDays><StorageClass>STANDARD_IA</StorageClass></NoncurrentVersionTransition>
</Rule>
<Rule><ID>logs</ID><Status>Disabled</Status><Filter><Prefix>logs/</Prefix></Filter>
  <Expiration><Days>90</Days></Expiration>
  <Transition><Days>0</Days><StorageClass>GLACIER_IR</StorageClass></Transition>
  <Transition><Days>30</Days><StorageClass>DEEP_ARCHIVE</StorageClass></Transition>
  <AbortIncompleteMultipartUpload><DaysAfterInitiation>2</DaysAfterInitiation></AbortIncompleteMultipartUpload>
</Rule>
<Rule><ID>markers</ID><Status>Enabled</Status><Filter><Tag><Key>tier</Key><Value>tmp</Value></Tag></Filter>
  <Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>
</Rule>
<Rule><ID>big</ID><Status>Enabled</Status>
  <Filter><And><Prefix>x/</Prefix><Tag><Key>a</Key><Value>1</Value></Tag><Tag><Key>b</Key><Value>2</Value></Tag><ObjectSizeGreaterThan>1024</ObjectSizeGreaterThan></And></Filter>
  <Expiration><Date>2027-01-01T00:00:00Z</Date></Expiration>
</Rule>
</LifecycleConfiguration>`
	rec := e.must(200, e.do("PUT", "/lc?lifecycle", body,
		"X-Amz-Transition-Default-Minimum-Object-Size", "varies_by_storage_class"))
	if rec.Header().Get("X-Amz-Transition-Default-Minimum-Object-Size") != "varies_by_storage_class" {
		t.Fatalf("put header %v", rec.Header())
	}

	var sent, got s3.LifecycleConfiguration
	if err := xml.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	rec = e.must(200, e.do("GET", "/lc?lifecycle", ""))
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad body %s: %v", rec.Body.String(), err)
	}
	if rec.Header().Get("X-Amz-Transition-Default-Minimum-Object-Size") != "varies_by_storage_class" {
		t.Errorf("get header %v", rec.Header())
	}
	if !reflect.DeepEqual(sent.Rules, got.Rules) {
		t.Errorf("rules changed:\nsent %s\ngot  %s", body, rec.Body.String())
	}
	// An empty filter stays an empty filter, not a missing one.
	if !strings.Contains(rec.Body.String(), "<Filter></Filter>") {
		t.Errorf("empty filter lost: %s", rec.Body.String())
	}

	e.must(204, e.do("DELETE", "/lc?lifecycle", ""))
	e.must(404, e.do("GET", "/lc?lifecycle", ""))
}

func TestLifecycleDefaultsAndValidation(t *testing.T) {
	e := newEnv(t)
	e.must(200, e.do("PUT", "/lv", ""))

	// A rule sent without an ID gets one, and the size default is filled in.
	e.must(200, e.do("PUT", "/lv?lifecycle",
		`<LifecycleConfiguration><Rule><Status>Enabled</Status><Prefix></Prefix><Expiration><Days>1</Days></Expiration></Rule></LifecycleConfiguration>`))
	rec := e.must(200, e.do("GET", "/lv?lifecycle", ""))
	var got s3.LifecycleConfiguration
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Rules) != 1 || got.Rules[0].ID == "" {
		t.Fatalf("generated ID: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Amz-Transition-Default-Minimum-Object-Size") != "all_storage_classes_128K" {
		t.Errorf("default size header %v", rec.Header())
	}

	rule := func(inner string) string {
		return `<LifecycleConfiguration><Rule><ID>r</ID>` + inner + `</Rule></LifecycleConfiguration>`
	}
	for name, body := range map[string]string{
		"no action":          rule(`<Status>Enabled</Status><Filter></Filter>`),
		"bad status":         rule(`<Status>On</Status><Filter></Filter><Expiration><Days>1</Days></Expiration>`),
		"zero days":          rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>0</Days></Expiration>`),
		"prefix+filter":      rule(`<Status>Enabled</Status><Prefix>a</Prefix><Filter></Filter><Expiration><Days>1</Days></Expiration>`),
		"two filters":        rule(`<Status>Enabled</Status><Filter><Prefix>a</Prefix><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter><Expiration><Days>1</Days></Expiration>`),
		"bad class":          rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition>`),
		"not midnight":       rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Date>2027-01-01T10:00:00Z</Date></Expiration>`),
		"noncurrent 0":       rule(`<Status>Enabled</Status><Filter></Filter><NoncurrentVersionExpiration><NoncurrentDays>0</NoncurrentDays></NoncurrentVersionExpiration>`),
		"duplicate ids":      `<LifecycleConfiguration><Rule><ID>d</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule><Rule><ID>d</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>2</Days></Expiration></Rule></LifecycleConfiguration>`,
		"days and date":      rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days><Date>2027-01-01T00:00:00Z</Date></Expiration>`),
		"abort zero":         rule(`<Status>Enabled</Status><Filter></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>0</DaysAfterInitiation></AbortIncompleteMultipartUpload>`),
		"transition no days": rule(`<Status>Enabled</Status><Filter></Filter><Transition><StorageClass>GLACIER</StorageClass></Transition>`),
	} {
		if rec := e.do("PUT", "/lv?lifecycle", body); rec.Code != 400 {
			t.Errorf("%s: want 400, got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	e.must(400, e.do("PUT", "/lv?lifecycle", `<LifecycleConfiguration></LifecycleConfiguration>`))
	e.must(400, e.do("PUT", "/lv?lifecycle", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration>`),
		"X-Amz-Transition-Default-Minimum-Object-Size", "tiny"))
	e.must(404, e.do("PUT", "/nobucket?lifecycle", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration>`)))
}
