// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build unix

package lambda

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// configureProcess puts the function in its own process group, so
// killProcess takes whatever it spawned too.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
}

// killProcess kills the function's whole process group.
func killProcess(p *os.Process) {
	syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// maxMemoryMB is the process's peak resident set size. ru_maxrss is in
// bytes on macOS and kilobytes elsewhere.
func maxMemoryMB(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return 0
	}
	ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int(ru.Maxrss >> 20)
	}
	return int(ru.Maxrss >> 10)
}
