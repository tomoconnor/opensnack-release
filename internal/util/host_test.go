// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import "testing"

func TestParseAWSHost(t *testing.T) {
	cases := []struct {
		host string
		want AWSHost
		ok   bool
	}{
		{"sqs.eu-west-1.amazonaws.com", AWSHost{Service: "sqs", Region: "eu-west-1"}, true},
		{"dynamodb.us-east-1.amazonaws.com:443", AWSHost{Service: "dynamodb", Region: "us-east-1"}, true},
		{"iam.amazonaws.com", AWSHost{Service: "iam"}, true},
		{"route53.amazonaws.com", AWSHost{Service: "route53"}, true},
		{"sts.amazonaws.com", AWSHost{Service: "sts"}, true},
		{"s3.amazonaws.com", AWSHost{Service: "s3"}, true},
		{"s3.us-west-2.amazonaws.com", AWSHost{Service: "s3", Region: "us-west-2"}, true},
		{"bucket.s3.amazonaws.com", AWSHost{Service: "s3", Bucket: "bucket"}, true},
		{"my.dotted.bucket.s3.eu-west-1.amazonaws.com", AWSHost{Service: "s3", Region: "eu-west-1", Bucket: "my.dotted.bucket"}, true},
		{"bucket.s3.dualstack.ap-south-1.amazonaws.com", AWSHost{Service: "s3", Region: "ap-south-1", Bucket: "bucket"}, true},
		{"bucket.s3-us-west-2.amazonaws.com", AWSHost{Service: "s3", Region: "us-west-2", Bucket: "bucket"}, true},
		{"s3.s3.us-east-1.amazonaws.com", AWSHost{Service: "s3", Region: "us-east-1", Bucket: "s3"}, true},
		{"123456789012.s3-control.us-east-1.amazonaws.com", AWSHost{Service: "s3-control", Region: "us-east-1", AccountID: "123456789012"}, true},
		{"123456789012.ddb.eu-west-1.amazonaws.com", AWSHost{Service: "dynamodb", Region: "eu-west-1", AccountID: "123456789012"}, true},
		{"localhost:4566", AWSHost{}, false},
		{"127.0.0.1", AWSHost{}, false},
		{"amazonaws.com.evil.example", AWSHost{}, false},
	}
	for _, c := range cases {
		got, ok := ParseAWSHost(c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.host, got, ok, c.want, c.ok)
		}
	}

	t.Setenv("OPENSNACK_HOST_SUFFIXES", "opensnack.test")
	if got, ok := ParseAWSHost("bkt.s3.opensnack.test"); !ok || got.Bucket != "bkt" {
		t.Errorf("custom suffix: %+v %v", got, ok)
	}
}
