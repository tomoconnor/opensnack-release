// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !unix

package lambda

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}

func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func maxMemoryMB(*exec.Cmd) int { return 0 }
