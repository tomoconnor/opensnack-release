// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !unix

package lambda

import (
	"os"
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}

func killProcess(p *os.Process) { p.Kill() }

func maxMemoryMB(*exec.Cmd) int { return 0 }
