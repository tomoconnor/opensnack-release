// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"opensnack/internal/kube"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// fakeIssuer hands out numbered credentials and remembers revocations.
type fakeIssuer struct {
	mu      sync.Mutex
	issued  []string
	revoked []string
	fail    bool
}

func (f *fakeIssuer) RoleCredentials(namespace, accountID, roleARN, session string, ttl time.Duration) (*RoleCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("no such role")
	}
	key := fmt.Sprintf("ASIA%s%d", strings.ToUpper(namespace), len(f.issued))
	f.issued = append(f.issued, key)
	return &RoleCredentials{AccessKeyID: key, SecretAccessKey: "s-" + key, SessionToken: "t-" + key, Expires: time.Now().Add(ttl)}, nil
}

func (f *fakeIssuer) Revoke(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, key)
}

func (f *fakeIssuer) revokedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// harness is an executor on a fake cluster where every pod runs fakeRuntime.
type harness struct {
	t     *testing.T
	e     *KubernetesExecutor
	pods  *kube.Fake
	creds *fakeIssuer

	mu      sync.Mutex
	created []kube.Pod
	envs    map[string]map[string]string // pod name -> env from /init
	// startDelay is how long a pod takes to call /next for the first time.
	startDelay time.Duration
	// noStart leaves new pods to the test.
	noStart bool
}

func testConfig() KubernetesConfig {
	return KubernetesConfig{
		Namespace:      "sandboxes",
		RuntimeClass:   "microvm",
		ServiceAccount: "sandbox",
		Images:         map[string]string{"python": "registry.example/python@sha256:abc", "nodejs": "registry.example/node@sha256:def"},
		RuntimeAPI:     "opensnack-runtime.system.svc:9001",
		MaxPods:        10,
		IdleTTL:        5 * time.Minute,
		StartTimeout:   5 * time.Second,
		MaxLifetime:    time.Hour,
		TmpSize:        "512Mi",
		TaskSize:       "320Mi",
		MaxProcs:       1024,
		MBPerVCPU:      1769,
		UIDBase:        100000,
		UIDCount:       50000,
		PollInterval:   time.Hour, // tests call Poll themselves
		BindPodIP:      true,
		Instance:       "test",
	}
}

func newHarness(t *testing.T, mutate ...func(*KubernetesConfig)) *harness {
	cfg := testConfig()
	for _, m := range mutate {
		m(&cfg)
	}
	h := &harness{t: t, pods: kube.NewFake(), creds: &fakeIssuer{}, envs: map[string]map[string]string{}}
	h.e = NewKubernetesExecutor(cfg, h.pods)
	h.e.SetCredentialIssuer(h.creds)
	h.pods.OnCreate = func(p *kube.Pod) {
		h.mu.Lock()
		h.created = append(h.created, *p)
		noStart := h.noStart
		h.mu.Unlock()
		if !noStart {
			go h.runPod(p)
		}
	}
	t.Cleanup(h.e.Stop)
	return h
}

func podEnv(p *kube.Pod, name string) string {
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// call sends a Runtime API request as the pod would, from its address.
func (h *harness) call(p *kube.Pod, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+h.e.cfg.RuntimeAPI+runtimePrefix+path, strings.NewReader(body))
	req.RemoteAddr = p.Status.PodIP + ":40000"
	req.Header.Set(runtimeTokenHeader, podEnv(p, runtimeTokenEnv))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.e.Handler().ServeHTTP(rec, req)
	return rec
}

// runPod is a pretend runtime. The event says what the "function" does:
// {"echo": x} returns x, {"fail": msg} raises, {"sleep": ms} sleeps first,
// {"oom": true} gets the pod OOM-killed, {"print": s} logs s.
func (h *harness) runPod(p *kube.Pod) {
	h.pods.Update(p.Metadata.Name, func(pp *kube.Pod) { pp.Status.Phase = kube.PodRunning })
	time.Sleep(h.startDelay)
	rec := h.call(p, "GET", "opensnack/init", "", nil)
	if rec.Code != 200 {
		return
	}
	var init struct{ Env map[string]string }
	json.Unmarshal(rec.Body.Bytes(), &init)
	h.mu.Lock()
	h.envs[p.Metadata.Name] = init.Env
	h.mu.Unlock()
	if init.Env["_HANDLER"] == "missing.handler" {
		h.call(p, "POST", "init/logs", "loading missing\n", nil)
		h.call(p, "POST", "init/error", `{"errorType":"Runtime.ImportModuleError","errorMessage":"Unable to import module 'missing'"}`,
			map[string]string{"Lambda-Runtime-Function-Error-Type": "Runtime.ImportModuleError"})
		return
	}
	for {
		rec := h.call(p, "GET", "invocation/next", "", nil)
		if rec.Code != 200 {
			return
		}
		id := rec.Header().Get("Lambda-Runtime-Aws-Request-Id")
		var ev struct {
			Echo  any    `json:"echo"`
			Fail  string `json:"fail"`
			Sleep int    `json:"sleep"`
			OOM   bool   `json:"oom"`
			Print string `json:"print"`
		}
		json.Unmarshal(rec.Body.Bytes(), &ev)
		if ev.Print != "" {
			h.call(p, "POST", "invocation/"+id+"/logs", ev.Print+"\n", nil)
		}
		time.Sleep(time.Duration(ev.Sleep) * time.Millisecond)
		switch {
		case ev.OOM:
			h.pods.Update(p.Metadata.Name, func(pp *kube.Pod) {
				pp.Status.Phase = kube.PodFailed
				pp.Status.ContainerStatuses = []kube.ContainerStatus{{Name: "function", State: kube.ContainerState{
					Terminated: &kube.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}}}
			})
			return
		case ev.Fail != "":
			h.call(p, "POST", "invocation/"+id+"/error", `{"errorType":"ValueError","errorMessage":"`+ev.Fail+`"}`,
				map[string]string{"Lambda-Runtime-Function-Error-Type": "ValueError", maxRSSHeader: "40960"})
		default:
			b, _ := json.Marshal(ev.Echo)
			if h.call(p, "POST", "invocation/"+id+"/response", string(b), map[string]string{maxRSSHeader: "51200"}).Code != 202 {
				return
			}
		}
	}
}

func (h *harness) podCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.created)
}

func testInvocation(fn string, payload string) *Invocation {
	return &Invocation{
		RequestID:    fmt.Sprintf("req-%d", time.Now().UnixNano()),
		Namespace:    "tenant-a",
		Region:       "us-east-1",
		AccountID:    "000000000000",
		FunctionName: fn,
		InvokedARN:   "arn:aws:lambda:us-east-1:000000000000:function:" + fn,
		Version:      "$LATEST",
		Runtime:      "python3.12",
		Handler:      "app.handler",
		Architecture: "x86_64",
		MemoryMB:     128,
		Timeout:      3 * time.Second,
		Environment:  map[string]string{"GREETING": "hello"},
		LogGroup:     "/aws/lambda/" + fn,
		LogStream:    "stream",
		Role:         "arn:aws:iam::000000000000:role/fn",
		LoadCode:     func() ([]byte, error) { return []byte("PK"), nil },
		CodeSha256:   "sha-1",
		Payload:      []byte(payload),
	}
}

func (h *harness) invoke(inv *Invocation) *InvocationResult {
	h.t.Helper()
	res, err := h.e.Invoke(context.Background(), inv)
	if err != nil {
		h.t.Fatalf("invoke: %v", err)
	}
	return res
}

func TestK8sPodSpecIsolation(t *testing.T) {
	h := newHarness(t)
	inv := testInvocation("isolated-fn", `{"echo":1}`)
	inv.MemoryMB = 1769
	h.invoke(inv)
	h.mu.Lock()
	p := h.created[0]
	h.mu.Unlock()
	s := p.Spec
	c := s.Containers[0]

	if p.Metadata.Namespace != "sandboxes" || s.RuntimeClassName == nil || *s.RuntimeClassName != "microvm" || s.ServiceAccountName != "sandbox" {
		t.Errorf("placement: ns %q runtimeClass %v sa %q", p.Metadata.Namespace, s.RuntimeClassName, s.ServiceAccountName)
	}
	if s.AutomountServiceAccountToken == nil || *s.AutomountServiceAccountToken || s.EnableServiceLinks == nil || *s.EnableServiceLinks {
		t.Error("the service account token and service links must not be mounted")
	}
	if s.HostNetwork || s.HostPID || s.HostIPC || s.RestartPolicy != "Never" {
		t.Errorf("host namespaces or restart policy: %+v", s)
	}
	if s.ActiveDeadlineSeconds == nil || *s.ActiveDeadlineSeconds != int64((time.Hour+16*time.Minute).Seconds()) {
		t.Errorf("activeDeadlineSeconds: %v", s.ActiveDeadlineSeconds)
	}
	psc, sc := s.SecurityContext, c.SecurityContext
	uid := *psc.RunAsUser
	if !*psc.RunAsNonRoot || uid < 100000 || uid >= 150000 || *psc.RunAsGroup != uid || psc.SeccompProfile.Type != "RuntimeDefault" {
		t.Errorf("pod security context: %+v", psc)
	}
	if !*sc.RunAsNonRoot || *sc.RunAsUser != uid || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || *sc.Privileged ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0 || sc.SeccompProfile.Type != "RuntimeDefault" {
		t.Errorf("container security context: %+v", sc)
	}
	if c.Resources.Limits["memory"] != "1769Mi" || c.Resources.Limits["cpu"] != "1000m" || c.Resources.Requests["memory"] != "1769Mi" {
		t.Errorf("resources: %+v", c.Resources)
	}
	if podEnv(&p, "_OPENSNACK_MAX_PROCS") != "1024" {
		t.Error("process limit not passed to the sandbox")
	}
	var tmp *kube.EmptyDirVolumeSource
	for _, v := range s.Volumes {
		if v.Name == "tmp" {
			tmp = v.EmptyDir
		}
		if v.EmptyDir == nil && v.ConfigMap == nil {
			t.Errorf("volume %s is neither an emptyDir nor a ConfigMap", v.Name)
		}
	}
	if tmp == nil || tmp.SizeLimit != "512Mi" || tmp.Medium != "" {
		t.Errorf("/tmp: %+v", tmp)
	}
	mounted := map[string]bool{}
	for _, m := range c.VolumeMounts {
		mounted[m.MountPath] = true
	}
	if !mounted["/tmp"] || !mounted["/var/task"] {
		t.Errorf("mounts: %v", c.VolumeMounts)
	}

	// Nothing about the tenant but hashes: no namespace, function name,
	// environment or credentials in the pod object.
	raw, _ := json.Marshal(p)
	for _, leak := range []string{"tenant-a", "isolated-fn", "GREETING", "hello", "ASIA", "arn:aws"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("pod spec contains %q: %s", leak, raw)
		}
	}
	dns1123 := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if !dns1123.MatchString(p.Metadata.Name) || len(p.Metadata.Name) > 63 {
		t.Errorf("pod name %q is not DNS-1123", p.Metadata.Name)
	}
	label := regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)
	for k, v := range p.Metadata.Labels {
		if len(v) > 63 || !label.MatchString(v) {
			t.Errorf("label %s=%q is not a valid label value", k, v)
		}
	}
	if p.Metadata.Labels[labelTenant] != tenantHash("tenant-a") || p.Metadata.Labels[labelInstance] != "test" {
		t.Errorf("labels: %v", p.Metadata.Labels)
	}

	// CPU is proportional to memory; no RuntimeClass means the default.
	if got := h.e.cpuMillis(128); got != 72 {
		t.Errorf("cpu at 128 MB: %dm", got)
	}
	h2 := newHarness(t, func(c *KubernetesConfig) { c.RuntimeClass = "" })
	h2.invoke(testInvocation("spec", `{"echo":1}`))
	if h2.created[0].Spec.RuntimeClassName != nil {
		t.Error("empty RuntimeClass should leave runtimeClassName unset")
	}
}

func TestK8sWarmReuseAndEnvironment(t *testing.T) {
	h := newHarness(t)
	h.startDelay = 300 * time.Millisecond
	inv := testInvocation("warm", `{"echo":"one","print":"first call"}`)
	inv.Environment = map[string]string{
		"GREETING": "hello", "AWS_ACCESS_KEY_ID": "AKIAEVIL", "AWS_LAMBDA_RUNTIME_API": "evil:1",
		"_HANDLER": "evil.handler", "AWS_ENDPOINT_URL": "http://evil", "LAMBDA_TASK_ROOT": "/evil",
	}
	start := time.Now()
	res := h.invoke(inv)
	cold := time.Since(start)
	if string(res.Payload) != `"one"` || res.FunctionError != "" {
		t.Fatalf("result: %s %s", res.Payload, res.FunctionError)
	}
	if !strings.Contains(strings.Join(res.Logs, ""), "first call") || res.MaxMemoryMB != 50 || res.LogStream == "" {
		t.Fatalf("logs %q, max memory %d, stream %q", res.Logs, res.MaxMemoryMB, res.LogStream)
	}
	// The cold start isn't part of the duration.
	if res.Duration >= h.startDelay {
		t.Errorf("duration %s includes the cold start", res.Duration)
	}

	inv2 := testInvocation("warm", `{"echo":"two"}`)
	inv2.Environment = inv.Environment
	start = time.Now()
	res2 := h.invoke(inv2)
	if warm := time.Since(start); warm > cold/2 {
		t.Errorf("warm invoke took %s, cold %s", warm, cold)
	}
	if string(res2.Payload) != `"two"` || h.podCount() != 1 || res2.LogStream != res.LogStream {
		t.Fatalf("second invoke: %s, %d pods, stream %q", res2.Payload, h.podCount(), res2.LogStream)
	}

	h.mu.Lock()
	env := h.envs[h.created[0].Metadata.Name]
	h.mu.Unlock()
	want := map[string]string{
		"GREETING": "hello", "_HANDLER": "app.handler", "LAMBDA_TASK_ROOT": "/var/task",
		"AWS_ACCESS_KEY_ID": "ASIATENANT-A0", "AWS_SESSION_TOKEN": "t-ASIATENANT-A0",
		"AWS_LAMBDA_FUNCTION_NAME": "warm", "AWS_REGION": "us-east-1", "AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"AWS_LAMBDA_RUNTIME_API", "AWS_ENDPOINT_URL"} {
		if _, ok := env[k]; ok {
			t.Errorf("function-set %s reached the sandbox", k)
		}
	}

	// A new configuration needs a new pod.
	inv3 := testInvocation("warm", `{"echo":"three"}`)
	inv3.MemoryMB = 256
	h.invoke(inv3)
	if h.podCount() != 2 {
		t.Fatalf("pods after a config change: %d", h.podCount())
	}
}

func TestK8sIdleReapingAndLifetime(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.e.now = func() time.Time { return now }
	h.e.d.now = func() time.Time { return now }
	h.invoke(testInvocation("idle", `{"echo":1}`))
	waitFor(t, func() bool { return idleCount(h.e.d) == 1 })

	h.e.Poll(context.Background())
	if len(h.pods.Names()) != 1 {
		t.Fatal("a fresh idle pod was reaped")
	}
	now = now.Add(6 * time.Minute)
	h.e.Poll(context.Background())
	if len(h.pods.Names()) != 0 || len(h.creds.revokedKeys()) != 1 {
		t.Fatalf("idle pod not reaped: pods %v, revoked %v", h.pods.Names(), h.creds.revokedKeys())
	}

	// A busy pod past its lifetime finishes its invocation, then goes.
	h.invoke(testInvocation("idle", `{"echo":1}`))
	waitFor(t, func() bool { return idleCount(h.e.d) == 1 })
	done := make(chan *InvocationResult)
	go func() { done <- h.invoke(testInvocation("idle", `{"echo":"late","sleep":300}`)) }()
	waitFor(t, func() bool { return busyCount(h.e.d) == 1 })
	now = now.Add(2 * time.Hour)
	h.e.Poll(context.Background())
	if len(h.pods.Names()) != 1 {
		t.Fatal("a busy pod was killed for its age")
	}
	if res := <-done; string(res.Payload) != `"late"` {
		t.Fatalf("result: %s", res.Payload)
	}
	waitFor(t, func() bool { return len(h.pods.Names()) == 0 })
}

func idleCount(d *dispatcher) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, sb := range d.sandboxes {
		if sb.idle() {
			n++
		}
	}
	return n
}

func busyCount(d *dispatcher) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, sb := range d.sandboxes {
		if sb.current != nil && sb.current.isTaken {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestK8sPodCapEvictsIdleThenThrottles(t *testing.T) {
	h := newHarness(t, func(c *KubernetesConfig) { c.MaxPods = 1 })
	h.invoke(testInvocation("a", `{"echo":1}`))
	waitFor(t, func() bool { return idleCount(h.e.d) == 1 })

	// a's idle pod makes way for b.
	h.invoke(testInvocation("b", `{"echo":1}`))
	if h.podCount() != 2 || len(h.pods.Names()) != 1 {
		t.Fatalf("eviction: created %d, live %v", h.podCount(), h.pods.Names())
	}

	// With b busy there's no room for c.
	waitFor(t, func() bool { return idleCount(h.e.d) == 1 })
	done := make(chan struct{})
	go func() { h.invoke(testInvocation("b", `{"echo":1,"sleep":300}`)); close(done) }()
	waitFor(t, func() bool { return busyCount(h.e.d) == 1 })
	if _, err := h.e.Invoke(context.Background(), testInvocation("c", `{}`)); !errors.Is(err, ErrThrottled) {
		t.Fatalf("expected ErrThrottled, got %v", err)
	}
	<-done
}

func TestK8sTimeoutKillsAndReplacesPod(t *testing.T) {
	h := newHarness(t)
	inv := testInvocation("slow", `{"echo":1,"sleep":2000}`)
	inv.Timeout = 200 * time.Millisecond
	res := h.invoke(inv)
	var doc map[string]string
	json.Unmarshal(res.Payload, &doc)
	if res.FunctionError != "Unhandled" || doc["errorType"] != "Sandbox.Timedout" ||
		!strings.Contains(doc["errorMessage"], "Task timed out after 0.20 seconds") {
		t.Fatalf("timeout result: %s %s", res.FunctionError, res.Payload)
	}
	if len(h.pods.Names()) != 0 {
		t.Fatalf("timed-out pod still exists: %v", h.pods.Names())
	}
	inv = testInvocation("slow", `{"echo":"again"}`)
	inv.Timeout = 200 * time.Millisecond
	if res := h.invoke(inv); string(res.Payload) != `"again"` || h.podCount() != 2 {
		t.Fatalf("after timeout: %s, %d pods", res.Payload, h.podCount())
	}
}

func TestK8sFunctionErrorsAndCrashes(t *testing.T) {
	h := newHarness(t)
	res := h.invoke(testInvocation("err", `{"fail":"nope"}`))
	if res.FunctionError != "Unhandled" || !strings.Contains(string(res.Payload), `"errorType":"ValueError"`) {
		t.Fatalf("function error: %s %s", res.FunctionError, res.Payload)
	}

	// Over its memory limit, the pod is OOM-killed: Runtime.ExitError,
	// not a failed executor.
	done := make(chan *InvocationResult)
	go func() { done <- h.invoke(testInvocation("err", `{"oom":true}`)) }()
	time.Sleep(50 * time.Millisecond)
	h.e.Poll(context.Background())
	res = <-done
	if res.FunctionError != "Unhandled" || !strings.Contains(string(res.Payload), "Runtime.ExitError") ||
		!strings.Contains(string(res.Payload), "signal: killed") {
		t.Fatalf("OOM: %s %s", res.FunctionError, res.Payload)
	}

	// An init failure is the invocation's error, with the init output.
	inv := testInvocation("broken", `{}`)
	inv.Handler = "missing.handler"
	res = h.invoke(inv)
	if res.FunctionError != "Unhandled" || !strings.Contains(string(res.Payload), "Runtime.ImportModuleError") ||
		!strings.Contains(strings.Join(res.Logs, ""), "loading missing") {
		t.Fatalf("init error: %s %s %q", res.FunctionError, res.Payload, res.Logs)
	}

	// An image that can't be pulled is the executor's failure.
	h.noStart = true
	go func() {
		waitFor(t, func() bool { return len(h.pods.Names()) == 1 })
		name := h.pods.Names()[0]
		h.pods.Update(name, func(p *kube.Pod) {
			p.Status.ContainerStatuses = []kube.ContainerStatus{{State: kube.ContainerState{Waiting: &kube.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}}}}
		})
		h.e.Poll(context.Background())
	}()
	_, err := h.e.Invoke(context.Background(), testInvocation("pull", `{}`))
	if err == nil || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Fatalf("image pull failure: %v", err)
	}

	// No pod comes up in time.
	h2 := newHarness(t, func(c *KubernetesConfig) { c.StartTimeout = 100 * time.Millisecond })
	h2.noStart = true
	if _, err := h2.e.Invoke(context.Background(), testInvocation("never", `{}`)); err == nil || !strings.Contains(err.Error(), "no sandbox started") {
		t.Fatalf("start timeout: %v", err)
	}

	// A role that can't be assumed fails before any pod is made.
	h3 := newHarness(t)
	h3.creds.fail = true
	var ae *apiError
	if _, err := h3.e.Invoke(context.Background(), testInvocation("norole", `{}`)); !errors.As(err, &ae) || h3.podCount() != 0 {
		t.Fatalf("unassumable role: %v, %d pods", err, h3.podCount())
	}

	if _, err := h.e.Invoke(context.Background(), &Invocation{Runtime: "ruby3.3"}); !errors.Is(err, ErrUnsupportedRuntime) {
		t.Fatalf("unsupported runtime: %v", err)
	}
}

func TestK8sRuntimeAPIAuth(t *testing.T) {
	h := newHarness(t)
	h.noStart = true
	done := make(chan *InvocationResult)
	go func() { done <- h.invoke(testInvocation("a", `{"echo":"a"}`)) }()
	waitFor(t, func() bool { return h.podCount() == 1 })
	go func() {
		inv := testInvocation("b", `{"echo":"b"}`)
		inv.Namespace = "tenant-b"
		h.e.Invoke(context.Background(), inv)
	}()
	waitFor(t, func() bool { return h.podCount() == 2 })
	h.mu.Lock()
	a, b := h.created[0], h.created[1]
	h.mu.Unlock()
	for _, p := range []*kube.Pod{&a, &b} {
		h.pods.Update(p.Metadata.Name, func(pp *kube.Pod) { pp.Status.Phase = kube.PodRunning })
	}

	// No token, a made-up one, and a's token from b's address.
	forged := a
	forged.Spec.Containers = []kube.Container{{Env: []kube.EnvVar{{Name: runtimeTokenEnv, Value: strings.Split(podEnv(&a, runtimeTokenEnv), ".")[0] + ".guess"}}}}
	stolen := a
	stolen.Status.PodIP = b.Status.PodIP
	for name, p := range map[string]*kube.Pod{"forged": &forged, "stolen": &stolen} {
		if rec := h.call(p, "GET", "invocation/next", "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s token: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "http://x"+runtimePrefix+"invocation/next", nil)
	rec := httptest.NewRecorder()
	h.e.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no token: %d", rec.Code)
	}

	// b's own token only ever reaches b's invocations.
	rec = h.call(&b, "GET", "invocation/next", "", nil)
	if rec.Code != 200 || string(rec.Body.Bytes()) != `{"echo":"b"}` {
		t.Fatalf("b's next: %d %s", rec.Code, rec.Body.String())
	}
	// ...and can't answer a's.
	aID := ""
	go func() {
		r := h.call(&a, "GET", "invocation/next", "", nil)
		aID = r.Header().Get("Lambda-Runtime-Aws-Request-Id")
		h.call(&a, "POST", "invocation/"+aID+"/response", `"from a"`, nil)
	}()
	res := <-done
	if string(res.Payload) != `"from a"` {
		t.Fatalf("a's result: %s", res.Payload)
	}
	if rec := h.call(&b, "POST", "invocation/"+aID+"/response", `"hijack"`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("b answering a's invocation: %d", rec.Code)
	}
	// b can fetch only its own code and environment.
	rec = h.call(&b, "GET", "opensnack/init", "", nil)
	if !strings.Contains(rec.Body.String(), `"AWS_LAMBDA_FUNCTION_NAME":"b"`) {
		t.Errorf("b's init: %s", rec.Body.String())
	}
}

func TestK8sOrphanCollection(t *testing.T) {
	h := newHarness(t, func(c *KubernetesConfig) { c.ListenAddr = "" })
	managed := map[string]string{labelManagedBy: "opensnack", labelComponent: componentValue, labelInstance: "test"}
	h.pods.Put(kube.Pod{Metadata: kube.ObjectMeta{Name: "lambda-old-1", Labels: managed}})
	h.pods.Put(kube.Pod{Metadata: kube.ObjectMeta{Name: "lambda-old-2", Labels: managed}})
	other := map[string]string{labelManagedBy: "opensnack", labelComponent: componentValue, labelInstance: "someone-else"}
	h.pods.Put(kube.Pod{Metadata: kube.ObjectMeta{Name: "lambda-theirs", Labels: other}})
	h.pods.Put(kube.Pod{Metadata: kube.ObjectMeta{Name: "unrelated"}})

	if err := h.e.Start(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.pods.Names(), ","); got != "lambda-theirs,unrelated" {
		t.Fatalf("after start: %s", got)
	}

	// Pods that appear later without a sandbox are collected by Poll; ours
	// survive it.
	h.invoke(testInvocation("keep", `{"echo":1}`))
	h.pods.Put(kube.Pod{Metadata: kube.ObjectMeta{Name: "lambda-stray", Labels: managed}})
	h.e.Poll(context.Background())
	names := strings.Join(h.pods.Names(), ",")
	if strings.Contains(names, "lambda-stray") || len(h.pods.Names()) != 3 {
		t.Fatalf("after poll: %s", names)
	}

	// A pod deleted behind our back fails its sandbox.
	mine := h.created[0].Metadata.Name
	h.pods.Delete(context.Background(), mine)
	h.e.Poll(context.Background())
	if n := len(h.e.d.snapshot()); n != 0 {
		t.Fatalf("sandbox for a deleted pod survives: %d", n)
	}
}

func TestK8sRetire(t *testing.T) {
	h := newHarness(t)
	h.invoke(testInvocation("one", `{"echo":1}`))
	h.invoke(testInvocation("two", `{"echo":1}`))
	other := testInvocation("one", `{"echo":1}`)
	other.Namespace = "tenant-b"
	h.invoke(other)
	waitFor(t, func() bool { return idleCount(h.e.d) == 3 })

	h.e.RetireFunction("tenant-a", "us-east-1", "one")
	waitFor(t, func() bool { return len(h.pods.Names()) == 2 })

	// A busy pod of a retired function finishes first.
	done := make(chan *InvocationResult)
	go func() { done <- h.invoke(testInvocation("two", `{"echo":"done","sleep":200}`)) }()
	waitFor(t, func() bool { return busyCount(h.e.d) == 1 })
	h.e.RetireFunction("tenant-a", "us-east-1", "two")
	if res := <-done; string(res.Payload) != `"done"` {
		t.Fatalf("retired mid-invocation: %s", res.Payload)
	}
	waitFor(t, func() bool { return len(h.pods.Names()) == 1 })

	// Deleting a namespace kills its pods, busy or not.
	go func() {
		inv := testInvocation("one", `{"echo":1,"sleep":2000}`)
		inv.Namespace = "tenant-b"
		done <- h.invoke(inv)
	}()
	waitFor(t, func() bool { return busyCount(h.e.d) == 1 })
	h.e.RetireNamespace("tenant-b")
	if res := <-done; !strings.Contains(string(res.Payload), "Runtime.ExitError") {
		t.Fatalf("namespace deletion: %s", res.Payload)
	}
	if len(h.pods.Names()) != 0 {
		t.Fatalf("pods left: %v", h.pods.Names())
	}
}

// The handler's caps sit in front of any executor.
type blockingExecutor struct {
	release chan struct{}
	started chan string
}

func (b *blockingExecutor) Invoke(ctx context.Context, inv *Invocation) (*InvocationResult, error) {
	b.started <- inv.FunctionName
	<-b.release
	return &InvocationResult{Payload: []byte(`"ok"`)}, nil
}

func TestConcurrencyCaps(t *testing.T) {
	c := newConcurrency(2)
	inv := func(ns, fn string, reserved *int) *Invocation {
		return &Invocation{Namespace: ns, Region: "us-east-1", FunctionName: fn, ReservedConcurrency: reserved}
	}
	one, zero := 1, 0
	r1, err := c.acquire(inv("a", "f", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire(inv("a", "g", &one)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire(inv("a", "h", nil)); err == nil || err.Reason != "ConcurrentInvocationLimitExceeded" {
		t.Fatalf("namespace cap: %v", err)
	}
	if _, err := c.acquire(inv("b", "f", nil)); err != nil {
		t.Fatalf("other namespaces are separate: %v", err)
	}
	r1()
	r1() // idempotent
	if _, err := c.acquire(inv("a", "g", &one)); err == nil || err.Reason != "ReservedFunctionConcurrentInvocationLimitExceeded" {
		t.Fatalf("reserved cap: %v", err)
	}
	if _, err := c.acquire(inv("c", "off", &zero)); err == nil {
		t.Fatal("reserved concurrency 0 must throttle every invocation")
	}
	if _, err := newConcurrency(0).acquire(inv("a", "x", nil)); err != nil {
		t.Fatalf("0 means no namespace cap: %v", err)
	}
}

func TestInvokeThrottlesAndRetriesAsync(t *testing.T) {
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	h := newTestHandler(t)
	ex := &blockingExecutor{release: make(chan struct{}), started: make(chan string, 10)}
	h.Executor = ex
	h.concurrency = newConcurrency(1)
	h.AsyncTiming = AsyncTiming{RetryDelay: 10 * time.Millisecond, ThrottleBackoff: 10 * time.Millisecond, MaxThrottleBackoff: 20 * time.Millisecond}
	createTestFunction(t, h, "f")

	// One invocation holds the namespace's only slot...
	go h.ServeREST(httptest.NewRecorder(), lambdaRequest("POST", "/2015-03-31/functions/f/invocations", `{}`))
	<-ex.started
	// ...so a second synchronous one is throttled,
	rec := httptest.NewRecorder()
	h.ServeREST(rec, lambdaRequest("POST", "/2015-03-31/functions/f/invocations", `{}`))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("X-Amzn-ErrorType") != "TooManyRequestsException" ||
		!strings.Contains(rec.Body.String(), `"Reason":"ConcurrentInvocationLimitExceeded"`) {
		t.Fatalf("throttle: %d %s %s", rec.Code, rec.Header(), rec.Body.String())
	}
	// ...and an asynchronous one waits its turn instead of failing.
	req := lambdaRequest("POST", "/2015-03-31/functions/f/invocations", `{}`)
	req.Header.Set("X-Amz-Invocation-Type", "Event")
	rec = httptest.NewRecorder()
	h.ServeREST(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("async: %d %s", rec.Code, rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	close(ex.release)
	select {
	case <-ex.started:
	case <-time.After(5 * time.Second):
		t.Fatal("throttled async invocation was never retried")
	}
	h.WaitAsync()
}

func newTestHandler(t *testing.T) *Handler {
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "")
	return NewHandler(resource.NewMemStore())
}

func lambdaRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, "https://lambda.us-east-1.amazonaws.com"+path, strings.NewReader(body))
	id := &util.Identity{Namespace: "tenant-a", Region: "us-east-1", AccountID: "000000000000", SigningService: "lambda"}
	return req.WithContext(util.WithIdentity(req.Context(), id))
}

func createTestFunction(t *testing.T, h *Handler, name string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, _ := zw.Create("app.py")
	io.WriteString(w, "def handler(e, c):\n    return e\n")
	zw.Close()
	body, _ := json.Marshal(map[string]any{
		"FunctionName": name, "Role": "arn:aws:iam::000000000000:role/fn", "Runtime": "python3.12",
		"Handler": "app.handler", "Code": map[string]any{"ZipFile": zipBuf.Bytes()},
	})
	rec := httptest.NewRecorder()
	h.ServeREST(rec, lambdaRequest("POST", "/2015-03-31/functions", string(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create function: %d %s", rec.Code, rec.Body.String())
	}
}
