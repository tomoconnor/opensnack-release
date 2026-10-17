// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import "syscall"

// RLIMIT_NPROC counts every process and thread of the real UID. Each
// sandbox pod runs as its own random UID, so the limit is per sandbox even
// where pods share a kernel.
const rlimitNproc = 6

func limitProcesses(n uint64) error { return lower(rlimitNproc, n) }

func limitFiles(n uint64) error { return lower(syscall.RLIMIT_NOFILE, n) }

// lower sets a limit to n, or leaves it where it is when the kernel's is
// already lower: without privileges a hard limit can only go down, and a
// small guest's defaults (which scale with its memory) can be under n.
func lower(resource int, n uint64) error {
	var cur syscall.Rlimit
	if err := syscall.Getrlimit(resource, &cur); err != nil {
		return err
	}
	n = min(n, cur.Max)
	return syscall.Setrlimit(resource, &syscall.Rlimit{Cur: n, Max: n})
}
