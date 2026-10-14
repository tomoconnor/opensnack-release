// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// lambda-init is the entrypoint of opensnack's Lambda runtime images. In a
// sandbox pod it fetches the function's environment and code from
// opensnack's Runtime API, unpacks the code into LAMBDA_TASK_ROOT, sets
// the process limits, and execs the language bootstrap, which takes
// invocations from the same API.
//
// The pod is told only:
//
//	AWS_LAMBDA_RUNTIME_API     host:port of the Runtime API
//	_OPENSNACK_RUNTIME_TOKEN   this sandbox's token
//	_OPENSNACK_RUNTIME         python | nodejs
//	_OPENSNACK_MAX_PROCS       process and thread limit (0: leave it)
//	_OPENSNACK_CA_FILE         an extra CA to trust, optional
//
// The function's process gets the environment from the Runtime API and
// nothing else from the pod (no Kubernetes service variables, say).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"opensnack/internal/unzip"
)

const (
	prefix      = "/2018-06-01/runtime"
	maxUnzipped = 250 << 20
	maxZip      = maxUnzipped
	caBundle    = "/tmp/.opensnack/ca-bundle.pem"
)

var (
	api    = os.Getenv("AWS_LAMBDA_RUNTIME_API")
	token  = os.Getenv("_OPENSNACK_RUNTIME_TOKEN")
	client = &http.Client{Timeout: 2 * time.Minute}
)

func main() {
	if api == "" || token == "" {
		fatal("AWS_LAMBDA_RUNTIME_API and _OPENSNACK_RUNTIME_TOKEN must be set")
	}
	var init struct {
		Env map[string]string `json:"env"`
	}
	if err := fetchJSON("/opensnack/init", &init); err != nil {
		fatal("fetching the sandbox environment: %v", err)
	}
	env := init.Env
	taskRoot := env["LAMBDA_TASK_ROOT"]
	if taskRoot == "" {
		taskRoot = "/var/task"
	}

	code, err := fetch("/opensnack/code", maxZip)
	if err != nil {
		fatal("fetching the function's code: %v", err)
	}
	if err := unzip.Extract(code, taskRoot, maxUnzipped); err != nil {
		initError("Runtime.UnzipError", "Could not unzip the deployment package: "+err.Error())
	}
	code = nil

	if ca := os.Getenv("_OPENSNACK_CA_FILE"); ca != "" {
		if err := writeCABundle(ca); err != nil {
			fmt.Fprintf(os.Stderr, "lambda-init: CA bundle: %v\n", err)
		}
	}
	if n, _ := strconv.Atoi(os.Getenv("_OPENSNACK_MAX_PROCS")); n > 0 {
		if err := limitProcesses(uint64(n)); err != nil {
			fatal("setting the process limit: %v", err)
		}
	}
	if err := limitFiles(1024); err != nil {
		fatal("setting the file limit: %v", err)
	}

	var interp string
	var args []string
	switch rt := os.Getenv("_OPENSNACK_RUNTIME"); rt {
	case "python":
		interp, args = "python3", []string{"python3", "-u", "-B", "/var/runtime/bootstrap.py"}
	case "nodejs":
		interp, args = "node", []string{"node", "/var/runtime/bootstrap.mjs"}
	default:
		fatal("unknown runtime %q", rt)
	}
	path, err := exec.LookPath(interp)
	if err != nil {
		fatal("%s: %v", interp, err)
	}

	procEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"AWS_LAMBDA_RUNTIME_API=" + api,
		"_OPENSNACK_RUNTIME_TOKEN=" + token,
		"PYTHONDONTWRITEBYTECODE=1",
	}
	if p := os.Getenv("NODE_PATH"); p != "" {
		procEnv = append(procEnv, "NODE_PATH="+p)
	}
	for k, v := range env {
		procEnv = append(procEnv, k+"="+v)
	}
	if err := os.Chdir(taskRoot); err != nil {
		fatal("%v", err)
	}
	if err := syscall.Exec(path, args, procEnv); err != nil {
		fatal("exec %s: %v", path, err)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "lambda-init: "+format+"\n", a...)
	os.Exit(1)
}

// request retries briefly: the pod can be up before its network is.
func request(method, path string, body []byte, hdr map[string]string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		req, err := http.NewRequest(method, "http://"+api+prefix+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Opensnack-Runtime-Token", token)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(time.Duration(100+50*attempt) * time.Millisecond)
	}
	return nil, lastErr
}

func fetch(path string, max int64) ([]byte, error) {
	resp, err := request(http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s", resp.Status, b)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, nil
}

func fetchJSON(path string, v any) error {
	b, err := fetch(path, 1<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// initError reports a failure the function's caller should see, then
// exits; opensnack fails the invocation with it.
func initError(errType, msg string) {
	doc, _ := json.Marshal(map[string]any{"errorType": errType, "errorMessage": msg, "stackTrace": []string{}})
	if resp, err := request(http.MethodPost, "/init/error", doc, map[string]string{"Lambda-Runtime-Function-Error-Type": errType}); err == nil {
		resp.Body.Close()
	}
	fatal("%s: %s", errType, msg)
}

// writeCABundle joins the image's trusted CAs and the extra one, for the
// SDKs that take a single bundle file.
func writeCABundle(extra string) error {
	ca, err := os.ReadFile(extra)
	if err != nil {
		return err
	}
	var bundle []byte
	for _, sys := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if b, err := os.ReadFile(sys); err == nil {
			bundle = append(b, '\n')
			break
		}
	}
	bundle = append(bundle, ca...)
	if err := os.MkdirAll(filepath.Dir(caBundle), 0o755); err != nil {
		return err
	}
	return os.WriteFile(caBundle, bundle, 0o644)
}
