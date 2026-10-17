// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opensnack/internal/unzip"
)

// LocalExecutor runs functions as ordinary child processes of opensnack:
// python3 for python* runtimes, node for nodejs*. Each invocation gets a
// fresh process that takes it from the Runtime API on loopback.
//
// UNSAFE FOR UNTRUSTED CODE. There is no sandbox: the function runs as the
// opensnack user, can read its files, reach the network and use as much
// memory as it likes. Only the timeout is enforced. It exists so the Invoke
// path can be developed and tested; enable it with
// OPENSNACK_LAMBDA_EXECUTOR=local on a machine where you trust every
// function you deploy.
type LocalExecutor struct {
	// Python and Node are the interpreters; empty means "python3" and
	// "node" from PATH.
	Python string
	Node   string
	// TempDir holds each invocation's unpacked code; empty means os.TempDir.
	TempDir string
	// Credentials, when set, gives functions credentials for their
	// execution role. Without it they run with none.
	Credentials CredentialIssuer

	once     sync.Once
	d        *dispatcher
	apiAddr  string
	startErr error
}

//go:embed bootstrap/bootstrap.py
var pythonBootstrap []byte

//go:embed bootstrap/bootstrap.mjs
var nodeBootstrap []byte

const (
	maxUnzipped   = 250 << 20
	maxLogCapture = 1 << 20
)

func (e *LocalExecutor) SetCredentialIssuer(c CredentialIssuer) { e.Credentials = c }

// runtime starts the loopback Runtime API the first time it's needed.
func (e *LocalExecutor) runtime() (*dispatcher, string, error) {
	e.once.Do(func() {
		e.d = newDispatcher(maxLogBytes())
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			e.startErr = err
			return
		}
		e.apiAddr = ln.Addr().String()
		go http.Serve(ln, e.d)
	})
	return e.d, e.apiAddr, e.startErr
}

func (e *LocalExecutor) Invoke(ctx context.Context, inv *Invocation) (*InvocationResult, error) {
	var interp, bootName string
	var boot []byte
	switch {
	case strings.HasPrefix(inv.Runtime, "python3"):
		interp, bootName, boot = firstNonEmpty(e.Python, "python3"), "bootstrap.py", pythonBootstrap
	case strings.HasPrefix(inv.Runtime, "nodejs"):
		interp, bootName, boot = firstNonEmpty(e.Node, "node"), "bootstrap.mjs", nodeBootstrap
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRuntime, inv.Runtime)
	}
	interp, err := resolveInterpreter(interp)
	if err != nil {
		return nil, fmt.Errorf("%w: %s (%v)", ErrUnsupportedRuntime, inv.Runtime, err)
	}
	d, apiAddr, err := e.runtime()
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp(e.TempDir, "opensnack-lambda-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	// The bootstrap lives outside the task root so the function's files
	// can't shadow it.
	paths := sandboxPaths{
		TaskRoot:   filepath.Join(work, "task"),
		RuntimeDir: filepath.Join(work, "runtime"),
		Tmp:        filepath.Join(work, "tmp"),
	}
	for _, dir := range []string{paths.TaskRoot, paths.RuntimeDir, paths.Tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	code, err := inv.LoadCode()
	if err != nil {
		return nil, err
	}
	if err := unzip.Extract(code, paths.TaskRoot, maxUnzipped); err != nil {
		return functionError("Runtime.UnzipError", "Could not unzip the deployment package: "+err.Error()), nil
	}
	bootPath := filepath.Join(paths.RuntimeDir, bootName)
	if err := os.WriteFile(bootPath, boot, 0o644); err != nil {
		return nil, err
	}

	opts := sandboxOptions{Paths: paths, LogStream: inv.LogStream, EndpointURL: endpointFromEnv()}
	if e.Credentials != nil && inv.Role != "" {
		if creds, err := e.Credentials.RoleCredentials(inv.Namespace, inv.AccountID, inv.Role, "opensnack-local", time.Hour); err == nil {
			opts.Creds = creds
			defer e.Credentials.Revoke(creds.AccessKeyID)
		}
	}
	env := sandboxEnv(inv, opts)
	sb := d.newSandbox("local/"+inv.RequestID, env, nil)
	c := newCall(inv, sb.key, d.logLimit)

	args := []string{bootPath}
	if bootName == "bootstrap.py" {
		args = append([]string{"-u", "-B"}, args...)
	}
	cmd := exec.Command(interp, args...)
	cmd.Dir = paths.TaskRoot
	cmd.Env = append(envList(env), "PATH="+os.Getenv("PATH"), "AWS_LAMBDA_RUNTIME_API="+apiAddr, runtimeTokenEnv+"="+sb.token())
	out := &cappedBuffer{max: maxLogCapture}
	cmd.Stdout, cmd.Stderr = out, out
	configureProcess(cmd)
	var proc atomic.Pointer[os.Process]
	sb.stop = func() {
		if p := proc.Load(); p != nil {
			killProcess(p)
		}
	}

	d.register(sb)
	d.submit(c)
	if err := cmd.Start(); err != nil {
		d.remove(sb, sandboxDeath{Reason: err.Error()})
		return nil, err
	}
	proc.Store(cmd.Process)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-c.done:
	case err := <-exited:
		msg := "Runtime exited without providing a reason"
		if err != nil {
			msg = "Runtime exited with error: " + err.Error()
		}
		d.remove(sb, sandboxDeath{Exit: true, Reason: msg})
		exited <- err
	case <-ctx.Done():
		d.cancel(c, ctx.Err())
	}
	d.remove(sb, sandboxDeath{Reason: "invocation over"})
	<-exited
	<-c.done
	if c.err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.err
	}
	res := c.result
	// Anything the process wrote outside the bootstrap's capture (an
	// interpreter that failed to start, say).
	if extra := splitLines(out.String()); len(extra) > 0 {
		res.Logs = append(res.Logs, extra...)
	}
	if res.MaxMemoryMB == 0 {
		res.MaxMemoryMB = maxMemoryMB(cmd)
	}
	return res, nil
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

var interpreters sync.Map // name -> resolved path

// resolveInterpreter finds the real binary behind name. Version-manager
// shims (asdf, pyenv) need the caller's HOME and environment, which the
// function's process deliberately doesn't get, so ask the interpreter once,
// in opensnack's environment, where it really lives.
func resolveInterpreter(name string) (string, error) {
	if p, ok := interpreters.Load(name); ok {
		return p.(string), nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	flag, probe := "-c", "import sys; sys.stdout.write(sys.executable)"
	if !strings.Contains(filepath.Base(name), "python") {
		flag, probe = "-e", "process.stdout.write(process.execPath)"
	}
	out, err := exec.Command(path, flag, probe).Output()
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("%s does not run: %v", name, err)
	}
	interpreters.Store(name, string(out))
	return string(out), nil
}

func functionError(typ, msg string) *InvocationResult {
	b, _ := json.Marshal(map[string]string{"errorType": typ, "errorMessage": msg})
	return &InvocationResult{Payload: b, FunctionError: "Unhandled"}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// cappedBuffer keeps the first max bytes written and drops the rest.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	if c.truncated {
		return c.buf.String() + "\n[log output truncated]\n"
	}
	return c.buf.String()
}
