// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import "syscall"

// RLIMIT_NPROC counts every process and thread of the real UID. Each
// sandbox pod runs as its own random UID, so the limit is per sandbox even
// where pods share a kernel.
const rlimitNproc = 6

func limitProcesses(n uint64) error {
	return syscall.Setrlimit(rlimitNproc, &syscall.Rlimit{Cur: n, Max: n})
}

func limitFiles(n uint64) error {
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: n, Max: n})
}
