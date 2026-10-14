// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import "testing"

const sampleEvent = `{
  "version": "0",
  "id": "6a7e8feb-b491-4cf7-a9f1-bf3703467718",
  "detail-type": "EC2 Instance State-change Notification",
  "source": "aws.ec2",
  "account": "111122223333",
  "time": "2017-12-22T18:43:48Z",
  "region": "us-west-1",
  "resources": ["arn:aws:ec2:us-west-1:123456789012:instance/i-1234567890abcdef0"],
  "detail": {
    "instance-id": "i-1234567890abcdef0",
    "state": "terminated",
    "c-count": 5,
    "d-count": 0.5,
    "source-ip": "10.0.0.123",
    "flag": true,
    "nothing": null,
    "tags": ["red", "Green"],
    "location": {"city": "Lapland", "team": 120},
    "items": [{"sku": "widget-1", "qty": 3}, {"sku": "scrap", "qty": 1}]
  }
}`

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"exact", `{"source":["aws.ec2"]}`, true},
		{"exact miss", `{"source":["aws.s3"]}`, false},
		{"array of values", `{"source":["aws.s3","aws.ec2"]}`, true},
		{"and across fields", `{"source":["aws.ec2"],"detail-type":["nope"]}`, false},
		{"nested", `{"detail":{"state":["terminated"]}}`, true},
		{"deep nested", `{"detail":{"location":{"city":["Lapland"]}}}`, true},
		{"number exact", `{"detail":{"c-count":[5]}}`, true},
		{"number exact as float", `{"detail":{"c-count":[5.0]}}`, true},
		{"string does not match number", `{"detail":{"c-count":["5"]}}`, false},
		{"bool", `{"detail":{"flag":[true]}}`, true},
		{"null", `{"detail":{"nothing":[null]}}`, true},
		{"null vs missing", `{"detail":{"missing":[null]}}`, false},
		{"event array any element", `{"detail":{"tags":["red"]}}`, true},
		{"resources array", `{"resources":[{"prefix":"arn:aws:ec2:"}]}`, true},
		{"array of objects", `{"detail":{"items":{"sku":["scrap"]}}}`, true},
		{"array of objects miss", `{"detail":{"items":{"sku":["widget-a"]}}}`, false},
		{"prefix", `{"detail":{"state":[{"prefix":"term"}]}}`, true},
		{"prefix miss", `{"detail":{"state":[{"prefix":"run"}]}}`, false},
		{"prefix ignore case", `{"detail":{"state":[{"prefix":{"equals-ignore-case":"TERM"}}]}}`, true},
		{"suffix", `{"detail":{"instance-id":[{"suffix":"def0"}]}}`, true},
		{"suffix ignore case", `{"detail":{"instance-id":[{"suffix":{"equals-ignore-case":"DEF0"}}]}}`, true},
		{"equals ignore case", `{"detail":{"state":[{"equals-ignore-case":"TERMINATED"}]}}`, true},
		{"equals ignore case in array", `{"detail":{"tags":[{"equals-ignore-case":"green"}]}}`, true},
		{"anything-but string", `{"detail":{"state":[{"anything-but":"running"}]}}`, true},
		{"anything-but string miss", `{"detail":{"state":[{"anything-but":"terminated"}]}}`, false},
		{"anything-but list", `{"detail":{"state":[{"anything-but":["running","terminated"]}]}}`, false},
		{"anything-but number", `{"detail":{"c-count":[{"anything-but":[1,2]}]}}`, true},
		{"anything-but number miss", `{"detail":{"c-count":[{"anything-but":5}]}}`, false},
		{"anything-but prefix", `{"detail":{"state":[{"anything-but":{"prefix":"run"}}]}}`, true},
		{"anything-but prefix miss", `{"detail":{"state":[{"anything-but":{"prefix":"term"}}]}}`, false},
		{"anything-but suffix", `{"detail":{"state":[{"anything-but":{"suffix":"ed"}}]}}`, false},
		{"anything-but ignore case", `{"detail":{"state":[{"anything-but":{"equals-ignore-case":["TERMINATED"]}}]}}`, false},
		{"anything-but missing field", `{"detail":{"missing":[{"anything-but":"x"}]}}`, false},
		{"numeric range", `{"detail":{"c-count":[{"numeric":[">",0,"<=",5]}]}}`, true},
		{"numeric range exclusive miss", `{"detail":{"c-count":[{"numeric":[">",0,"<",5]}]}}`, false},
		{"numeric equals", `{"detail":{"d-count":[{"numeric":["=",0.5]}]}}`, true},
		{"numeric on string", `{"detail":{"state":[{"numeric":[">",0]}]}}`, false},
		{"numeric nested", `{"detail":{"location":{"team":[{"numeric":[">=",100]}]}}}`, true},
		{"exists true", `{"detail":{"state":[{"exists":true}]}}`, true},
		{"exists true missing", `{"detail":{"missing":[{"exists":true}]}}`, false},
		{"exists false missing", `{"detail":{"missing":[{"exists":false}]}}`, true},
		{"exists false present", `{"detail":{"state":[{"exists":false}]}}`, false},
		{"exists false under missing parent", `{"nope":{"x":[{"exists":false}]}}`, true},
		{"exists on object is false", `{"detail":{"location":[{"exists":true}]}}`, false},
		{"cidr", `{"detail":{"source-ip":[{"cidr":"10.0.0.0/24"}]}}`, true},
		{"cidr miss", `{"detail":{"source-ip":[{"cidr":"10.0.1.0/24"}]}}`, false},
		{"wildcard", `{"detail":{"instance-id":[{"wildcard":"i-*def0"}]}}`, true},
		{"wildcard miss", `{"detail":{"instance-id":[{"wildcard":"i-*abc"}]}}`, false},
		{"mixed matchers OR", `{"detail":{"state":["running",{"prefix":"term"}]}}`, true},
		{"$or top", `{"$or":[{"source":["aws.s3"]},{"detail-type":[{"prefix":"EC2"}]}]}`, true},
		{"$or none", `{"$or":[{"source":["aws.s3"]},{"detail-type":["x"]}]}`, false},
		{"$or nested", `{"source":["aws.ec2"],"detail":{"$or":[{"state":["running"]},{"c-count":[{"numeric":[">",3]}]}]}}`, true},
		{"$or nested AND fails", `{"source":["aws.s3"],"detail":{"$or":[{"state":["terminated"]},{"flag":[true]}]}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := CompilePattern(c.pattern)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := p.Match([]byte(sampleEvent)); got != c.want {
				t.Fatalf("Match = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPatternInvalid(t *testing.T) {
	for _, s := range []string{
		``,
		`[]`,
		`{"source":"aws.ec2"}`,
		`{"source":[]}`,
		`{"source":[{"bogus":"x"}]}`,
		`{"source":[{"prefix":1}]}`,
		`{"x":[{"numeric":[">"]}]}`,
		`{"x":[{"numeric":["~",1]}]}`,
		`{"x":[{"numeric":["<",5,">",1]}]}`,
		`{"x":[{"exists":"yes"}]}`,
		`{"x":[{"cidr":"nope"}]}`,
		`{"$or":[{"a":["b"]}]}`,
		`{"x":[{"anything-but":{"numeric":[">",1]}}]}`,
		`{"x":[{"anything-but":[]}]}`,
	} {
		if _, err := CompilePattern(s); err == nil {
			t.Errorf("CompilePattern(%q) succeeded, want error", s)
		}
	}
}

func TestWildcard(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{
		{"*", "", true},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a*c", "ac", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "acb", false},
		{"ab", "ab", true},
		{`a\*`, "a*", true},
		{`a\*`, "ab", false},
		{"a*a", "a", false},
	} {
		if got := wildcardMatch(c.p, c.s); got != c.want {
			t.Errorf("wildcardMatch(%q,%q)=%v", c.p, c.s, got)
		}
	}
}
