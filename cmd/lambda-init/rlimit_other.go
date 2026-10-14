// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !linux

package main

// lambda-init only runs in Linux sandboxes; elsewhere it builds for tests.
func limitProcesses(uint64) error { return nil }
func limitFiles(uint64) error     { return nil }
