// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"syscall"
	"testing"
)

// Asking for more than the hard limit lowers nothing and doesn't fail; asking
// for less lowers both limits.
func TestLowerNeverRaises(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Fatal(err)
	}
	if err := limitFiles(before.Max + 1000); err != nil {
		t.Fatalf("asking above the hard limit: %v", err)
	}
	want := min(before.Max, 64)
	if err := limitFiles(want); err != nil {
		t.Fatal(err)
	}
	var after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); err != nil {
		t.Fatal(err)
	}
	if after.Cur != want || after.Max != want {
		t.Fatalf("limits %d/%d, want %d", after.Cur, after.Max, want)
	}
}
