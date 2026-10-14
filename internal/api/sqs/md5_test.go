// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import "testing"

// A digest real SQS returns, so the algorithm is checked against AWS rather
// than against itself.
func TestMD5OfAttributesMatchesAWS(t *testing.T) {
	got := md5OfAttributes(map[string]MessageAttributeValue{
		"timestamp": {DataType: "Number", StringValue: "1493147359900"},
	})
	if want := "235c5c510d26fb653d073faed50ae77c"; got != want {
		t.Fatalf("md5OfAttributes = %s, want %s", got, want)
	}
}
