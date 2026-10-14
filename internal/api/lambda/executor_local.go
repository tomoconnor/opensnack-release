// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalExecutor runs functions as ordinary child processes of opensnack:
// python3 for python* runtimes, node for nodejs*.
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
}

//go:embed bootstrap/bootstrap.py
var pythonBootstrap []byte

//go:embed bootstrap/bootstrap.mjs
var nodeBootstrap []byte

const (
	maxUnzipped   = 250 << 20
	maxLogCapture = 1 << 20
)

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

	work, err := os.MkdirTemp(e.TempDir, "opensnack-lambda-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	// The bootstrap lives outside the task root so the function's files
	// can't shadow it.
	taskRoot := filepath.Join(work, "task")
	runtimeDir := filepath.Join(work, "runtime")
	tmpDir := filepath.Join(work, "tmp")
	for _, d := range []string{taskRoot, runtimeDir, tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := unzipTo(inv.Code, taskRoot); err != nil {
		return functionError("Runtime.UnzipError", "Could not unzip the deployment package: "+err.Error()), nil
	}
	bootPath := filepath.Join(runtimeDir, bootName)
	if err := os.WriteFile(bootPath, boot, 0o644); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(inv.Timeout)
	spec := map[string]any{
		"handler":     inv.Handler,
		"task_root":   taskRoot,
		"event_path":  filepath.Join(runtimeDir, "event.json"),
		"result_path": filepath.Join(runtimeDir, "result.json"),
		"deadline_ms": deadline.UnixMilli(),
		"context": map[string]any{
			"function_name":        inv.FunctionName,
			"function_version":     inv.Version,
			"invoked_function_arn": inv.InvokedARN,
			"memory_limit_in_mb":   inv.MemoryMB,
			"aws_request_id":       inv.RequestID,
			"log_group_name":       inv.LogGroup,
			"log_stream_name":      inv.LogStream,
		},
	}
	if len(inv.ClientContext) > 0 && json.Valid(inv.ClientContext) {
		spec["client_context"] = json.RawMessage(inv.ClientContext)
	}
	specPath := filepath.Join(runtimeDir, "spec.json")
	specJSON, _ := json.Marshal(spec)
	if err := os.WriteFile(specPath, specJSON, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec["event_path"].(string), inv.Payload, 0o600); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	args := []string{bootPath, specPath}
	if bootName == "bootstrap.py" {
		args = append([]string{"-u", "-B"}, args...)
	}
	cmd := exec.CommandContext(runCtx, interp, args...)
	cmd.Dir = taskRoot
	cmd.Env = lambdaEnv(inv, taskRoot, runtimeDir, tmpDir)
	out := &cappedBuffer{max: maxLogCapture}
	cmd.Stdout, cmd.Stderr = out, out
	configureProcess(cmd)

	started := time.Now()
	runErr := cmd.Run()
	res := &InvocationResult{Duration: time.Since(started), Logs: splitLines(out.String()), MaxMemoryMB: maxMemoryMB(cmd)}

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		fe := functionError("Sandbox.Timedout", fmt.Sprintf("RequestId: %s Error: Task timed out after %.2f seconds", inv.RequestID, inv.Timeout.Seconds()))
		fe.Logs, fe.Duration, fe.MaxMemoryMB = res.Logs, res.Duration, res.MaxMemoryMB
		return fe, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	raw, readErr := os.ReadFile(spec["result_path"].(string))
	var result struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if readErr != nil || json.Unmarshal(raw, &result) != nil {
		msg := "Runtime exited without providing a reason"
		if runErr != nil {
			msg = "Runtime exited with error: " + runErr.Error()
		}
		fe := functionError("Runtime.ExitError", fmt.Sprintf("RequestId: %s Error: %s", inv.RequestID, msg))
		fe.Logs, fe.Duration, fe.MaxMemoryMB = res.Logs, res.Duration, res.MaxMemoryMB
		return fe, nil
	}
	if result.OK {
		res.Payload = result.Result
		if len(res.Payload) == 0 {
			res.Payload = []byte("null")
		}
		return res, nil
	}
	res.FunctionError = "Unhandled"
	res.Payload = result.Error
	return res, nil
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

// lambdaEnv is the process environment: the variables Lambda defines plus
// the function's own. Nothing is inherited from opensnack except PATH, so
// its credentials and DSNs don't leak into the function.
func lambdaEnv(inv *Invocation, taskRoot, runtimeDir, tmpDir string) []string {
	env := map[string]string{}
	for k, v := range inv.Environment {
		env[k] = v
	}
	for k, v := range map[string]string{
		"PATH":                            os.Getenv("PATH"),
		"HOME":                            tmpDir,
		"TMPDIR":                          tmpDir,
		"LANG":                            "en_US.UTF-8",
		"TZ":                              ":UTC",
		"LAMBDA_TASK_ROOT":                taskRoot,
		"LAMBDA_RUNTIME_DIR":              runtimeDir,
		"_HANDLER":                        inv.Handler,
		"AWS_REGION":                      inv.Region,
		"AWS_DEFAULT_REGION":              inv.Region,
		"AWS_EXECUTION_ENV":               "AWS_Lambda_" + inv.Runtime,
		"AWS_LAMBDA_FUNCTION_NAME":        inv.FunctionName,
		"AWS_LAMBDA_FUNCTION_VERSION":     inv.Version,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprint(inv.MemoryMB),
		"AWS_LAMBDA_LOG_GROUP_NAME":       inv.LogGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      inv.LogStream,
		"AWS_LAMBDA_INITIALIZATION_TYPE":  "on-demand",
	} {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// unzipTo extracts a deployment package, refusing entries that would land
// outside dir and packages that unpack too large.
func unzipTo(code []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("illegal path in archive: %s", f.Name)
		}
		dst := filepath.Join(dir, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm() | 0o600
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(w, io.LimitReader(rc, maxUnzipped-total+1))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
		if total += n; total > maxUnzipped {
			return errors.New("unzipped size must be smaller than 262144000 bytes")
		}
	}
	return nil
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
