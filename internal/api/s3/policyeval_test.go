// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package s3

import "testing"

func TestPolicyDenies(t *testing.T) {
	const (
		me      = "arn:aws:iam::000000000042:user/ns1"
		keeper  = "arn:aws:iam::000000000042:role/list-keepers/keeper"
		account = "000000000042"
		obj     = "arn:aws:s3:::b/list.csv"
	)
	deny := func(rest string) string {
		return `{"Version":"2012-10-17","Statement":[{"Effect":"Deny",` + rest + `}]}`
	}
	exceptKeeper := `"Principal":"*","Action":["s3:DeleteBucket","s3:DeleteObjectVersion"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"],"Condition":{"ArnNotEquals":{"aws:PrincipalArn":"` + keeper + `"}}`
	for _, tc := range []struct {
		name, policy, principal, action, resource string
		want                                      bool
	}{
		{"everyone but the keeper", deny(exceptKeeper), me, "s3:DeleteObjectVersion", obj, true},
		{"the keeper", deny(exceptKeeper), keeper, "s3:DeleteObjectVersion", obj, false},
		{"the bucket", deny(exceptKeeper), me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
		{"a delete marker is allowed", deny(exceptKeeper), me, "s3:DeleteObject", obj, false},
		{"another bucket", deny(exceptKeeper), me, "s3:DeleteObjectVersion", "arn:aws:s3:::c/list.csv", false},
		{"wildcard action", deny(`"Principal":{"AWS":"*"},"Action":"s3:Delete*","Resource":"arn:aws:s3:::b/*"`), me, "s3:DeleteObjectVersion", obj, true},
		{"action case", deny(`"Principal":"*","Action":"S3:deleteobjectversion","Resource":"*"`), me, "s3:DeleteObjectVersion", obj, true},
		{"NotPrincipal", deny(`"NotPrincipal":{"AWS":"` + keeper + `"},"Action":"s3:*","Resource":"*"`), me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
		{"NotPrincipal keeper", deny(`"NotPrincipal":{"AWS":"` + keeper + `"},"Action":"s3:*","Resource":"*"`), keeper, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"NotAction", deny(`"Principal":"*","NotAction":"s3:Get*","Resource":"*"`), me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
		{"NotResource", deny(`"Principal":"*","Action":"*","NotResource":"arn:aws:s3:::b/*"`), me, "s3:DeleteObject", obj, false},
		{"account principal", deny(`"Principal":{"AWS":"arn:aws:iam::000000000042:root"},"Action":"*","Resource":"*"`), me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
		{"someone else", deny(`"Principal":{"AWS":"arn:aws:iam::000000000042:user/gremlin"},"Action":"*","Resource":"*"`), me, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"like", deny(`"Principal":"*","Action":"*","Resource":"*","Condition":{"ArnNotLike":{"aws:PrincipalArn":"arn:aws:iam::*:role/list-keepers/*"}}`), keeper, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"unknown condition never denies", deny(`"Principal":"*","Action":"*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`), me, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"secure transport", deny(`"Principal":"*","Action":"*","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"false"}}`), me, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"account condition", deny(`"Principal":"*","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalAccount":"000000000042"}}`), me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
		{"allow", `{"Statement":{"Effect":"Allow","Principal":"*","Action":"*","Resource":"*"}}`, me, "s3:DeleteBucket", "arn:aws:s3:::b", false},
		{"one statement, not a list", `{"Statement":{"Effect":"Deny","Principal":"*","Action":"*","Resource":"*"}}`, me, "s3:DeleteBucket", "arn:aws:s3:::b", true},
	} {
		if got := policyDenies(tc.policy, tc.principal, account, tc.action, tc.resource); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		want bool
	}{
		{"arn:aws:s3:::b/*", "arn:aws:s3:::b/x/y.csv", true},
		{"arn:aws:s3:::b/*", "arn:aws:s3:::b", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "", true},
		{"**x", "abx", true},
	} {
		if got := glob(tc.p, tc.s); got != tc.want {
			t.Errorf("%q %q: %v", tc.p, tc.s, got)
		}
	}
}
