// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"opensnack/internal/kube"
)

//
// KUBERNETES EXECUTOR
//
// KubernetesExecutor runs each function in its own pod, in a namespace set
// aside for sandboxes, under a configurable RuntimeClass (a microVM runtime
// in production, the default runtime in CI). A pod serves one function
// version: it fetches that version's code and environment from the Runtime
// API at start, then takes invocations one at a time from
// /invocation/next. It stays warm for the next invocation until it has been
// idle for IdleTTL, and is retired before its credentials could expire.
//
// The pod spec carries no tenant data: names and labels hold hashes, and
// the code, environment and credentials come over the Runtime API, which
// only that pod's token (and, by default, its address) can use.
//
// opensnack must run as a single replica: each replica collects pods it
// doesn't know as orphans.
//

// KubernetesConfig configures KubernetesExecutor. KubernetesExecutorFromEnv
// reads it from OPENSNACK_LAMBDA_K8S_* variables (see the README).
type KubernetesConfig struct {
	// Namespace holds the sandbox pods.
	Namespace string
	// RuntimeClass is the pods' runtimeClassName; empty uses the cluster's
	// default runtime.
	RuntimeClass string
	// ServiceAccount is the pods' service account, which should have no
	// RBAC bindings; its token is never mounted.
	ServiceAccount string
	// Images maps a runtime family ("python", "nodejs") to the runtime
	// image, ideally pinned by digest. A family with no image can't run.
	Images map[string]string
	// PullPolicy is the pods' imagePullPolicy: IfNotPresent by default;
	// Never for images imported onto the nodes by hand, so a missing one
	// fails instead of being fetched from a registry.
	PullPolicy string
	// RuntimeAPI is the host:port pods reach the Runtime API on (a Service
	// in front of ListenAddr).
	RuntimeAPI string
	// ListenAddr is where opensnack serves the Runtime API.
	ListenAddr string
	// MaxPods caps sandbox pods across all tenants. Idle pods make way for
	// new ones; with none idle, invocations are throttled.
	MaxPods int
	// IdleTTL is how long a pod stays warm with nothing to do.
	IdleTTL time.Duration
	// StartTimeout bounds a cold start: from creating the pod to its first
	// /next. It doesn't count toward the function's own timeout.
	StartTimeout time.Duration
	// MaxLifetime is how long a pod takes new invocations for; its
	// credentials and activeDeadlineSeconds are sized from it.
	MaxLifetime time.Duration
	// TmpSize and TaskSize are the size limits of /tmp and /var/task.
	TmpSize  string
	TaskSize string
	// MaxProcs is the sandbox's process (and thread) limit.
	MaxProcs int
	// GuestOverheadMB is added to a pod's memory on top of the function's
	// MemorySize. A microVM runtime that sizes the guest from the pod
	// (Kata's static_sandbox_resource_mgmt) spends part of it on the guest
	// kernel and agent, which would otherwise come out of the function's.
	GuestOverheadMB int
	// MBPerVCPU sets the CPU limit in proportion to memory: Lambda gives a
	// full vCPU at 1769 MB. MinCPUMillis is a floor.
	MBPerVCPU    int
	MinCPUMillis int
	// CPURequestMillis, when set and below the limit, is the pod's CPU
	// request; otherwise the request equals the limit. A sandbox mostly
	// idles between invocations, so a low request lets a node schedule
	// many more of them, while the limit still sizes a cold start.
	CPURequestMillis int
	// CAConfigMap names a ConfigMap whose ca.crt is added to the sandbox's
	// trusted CAs (for an opensnack serving AWS host names with its own CA).
	CAConfigMap string
	// EndpointURL, when set, is AWS_ENDPOINT_URL in every sandbox.
	EndpointURL string
	// UIDBase and UIDCount: each pod runs as a random UID in
	// [UIDBase, UIDBase+UIDCount), so sandboxes sharing a kernel don't share
	// a user (or its process limit).
	UIDBase  int64
	UIDCount int64
	// PollInterval is how often pods are checked for crashes, idleness and
	// orphans.
	PollInterval time.Duration
	// BindPodIP makes Runtime API requests come from the pod's own address
	// as well as carry its token. Turn it off only if traffic to opensnack
	// is source-NATed.
	BindPodIP bool
	// Instance labels this opensnack's pods.
	Instance string
}

// Runtime families and how invocations pick one.
func runtimeFamily(runtime string) string {
	switch {
	case strings.HasPrefix(runtime, "python3"):
		return "python"
	case strings.HasPrefix(runtime, "nodejs"):
		return "nodejs"
	}
	return ""
}

// Labels on every sandbox pod.
const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelComponent = "app.kubernetes.io/component"
	labelInstance  = "opensnack/instance"
	labelTenant    = "opensnack/tenant"
	labelFunction  = "opensnack/function"
	labelRuntime   = "opensnack/runtime"
	componentValue = "lambda-sandbox"
	caMountPath    = "/etc/opensnack/ca"
)

// KubernetesExecutor runs functions in sandbox pods. See KubernetesConfig.
type KubernetesExecutor struct {
	cfg   KubernetesConfig
	pods  kube.Pods
	d     *dispatcher
	creds CredentialIssuer
	now   func() time.Time

	// mu makes capacity decisions (count, evict, reserve) atomic.
	mu   sync.Mutex
	srv  *http.Server
	stop chan struct{}
	wg   sync.WaitGroup
}

// podInfo is a sandbox's pod.
type podInfo struct {
	name      string
	tenant    string
	function  string
	logStream string
	created   bool // the API server has accepted it
}

// NewKubernetesExecutor builds an executor on a pod client.
func NewKubernetesExecutor(cfg KubernetesConfig, pods kube.Pods) *KubernetesExecutor {
	e := &KubernetesExecutor{cfg: cfg, pods: pods, d: newDispatcher(maxLogBytes()), now: time.Now, stop: make(chan struct{})}
	e.d.bindIP = cfg.BindPodIP
	e.d.lookupIP = e.podIP
	return e
}

func envDuration(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive duration", name, v)
	}
	return d, nil
}

func envNumber(name string, def int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: %q is not a non-negative integer", name, v)
	}
	return n, nil
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// KubernetesConfigFromEnv reads the executor's settings.
func KubernetesConfigFromEnv() (KubernetesConfig, error) {
	cfg := KubernetesConfig{
		Namespace:      envString("OPENSNACK_LAMBDA_K8S_NAMESPACE", "opensnack-lambda"),
		RuntimeClass:   os.Getenv("OPENSNACK_LAMBDA_K8S_RUNTIME_CLASS"),
		ServiceAccount: envString("OPENSNACK_LAMBDA_K8S_SERVICE_ACCOUNT", "opensnack-lambda"),
		Images: map[string]string{
			"python": os.Getenv("OPENSNACK_LAMBDA_K8S_IMAGE_PYTHON"),
			"nodejs": os.Getenv("OPENSNACK_LAMBDA_K8S_IMAGE_NODEJS"),
		},
		PullPolicy:  envString("OPENSNACK_LAMBDA_K8S_IMAGE_PULL_POLICY", "IfNotPresent"),
		RuntimeAPI:  os.Getenv("OPENSNACK_LAMBDA_K8S_RUNTIME_API"),
		ListenAddr:  envString("OPENSNACK_LAMBDA_RUNTIME_LISTEN_ADDR", ":9001"),
		TmpSize:     envString("OPENSNACK_LAMBDA_K8S_TMP_SIZE", "512Mi"),
		TaskSize:    envString("OPENSNACK_LAMBDA_K8S_TASK_SIZE", "320Mi"),
		CAConfigMap: os.Getenv("OPENSNACK_LAMBDA_K8S_CA_CONFIGMAP"),
		EndpointURL: endpointFromEnv(),
		Instance:    envString("OPENSNACK_LAMBDA_K8S_INSTANCE", "opensnack"),
		BindPodIP:   os.Getenv("OPENSNACK_LAMBDA_K8S_BIND_POD_IP") != "false",
	}
	var err error
	num := func(name string, def int64) int64 {
		n, e := envNumber(name, def)
		if e != nil && err == nil {
			err = e
		}
		return n
	}
	dur := func(name string, def time.Duration) time.Duration {
		d, e := envDuration(name, def)
		if e != nil && err == nil {
			err = e
		}
		return d
	}
	cfg.MaxPods = int(num("OPENSNACK_LAMBDA_K8S_MAX_PODS", 20))
	cfg.MaxProcs = int(num("OPENSNACK_LAMBDA_K8S_MAX_PROCS", 1024))
	cfg.GuestOverheadMB = int(num("OPENSNACK_LAMBDA_K8S_GUEST_OVERHEAD_MB", 0))
	cfg.MBPerVCPU = int(num("OPENSNACK_LAMBDA_K8S_MB_PER_VCPU", 1769))
	cfg.MinCPUMillis = int(num("OPENSNACK_LAMBDA_K8S_MIN_CPU_MILLIS", 0))
	cfg.CPURequestMillis = int(num("OPENSNACK_LAMBDA_K8S_CPU_REQUEST_MILLIS", 0))
	cfg.UIDBase = num("OPENSNACK_LAMBDA_K8S_UID_BASE", 100000)
	cfg.UIDCount = num("OPENSNACK_LAMBDA_K8S_UID_COUNT", 50000)
	cfg.IdleTTL = dur("OPENSNACK_LAMBDA_K8S_IDLE_TTL", 5*time.Minute)
	cfg.StartTimeout = dur("OPENSNACK_LAMBDA_K8S_START_TIMEOUT", time.Minute)
	cfg.MaxLifetime = dur("OPENSNACK_LAMBDA_K8S_MAX_POD_LIFETIME", time.Hour)
	cfg.PollInterval = dur("OPENSNACK_LAMBDA_K8S_POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (cfg *KubernetesConfig) validate() error {
	switch {
	case cfg.RuntimeAPI == "":
		return errors.New("OPENSNACK_LAMBDA_K8S_RUNTIME_API is required: the host:port sandbox pods reach opensnack's Runtime API on")
	case cfg.Images["python"] == "" && cfg.Images["nodejs"] == "":
		return errors.New("set OPENSNACK_LAMBDA_K8S_IMAGE_PYTHON and/or OPENSNACK_LAMBDA_K8S_IMAGE_NODEJS")
	case cfg.PullPolicy != "IfNotPresent" && cfg.PullPolicy != "Never" && cfg.PullPolicy != "Always":
		return errors.New("OPENSNACK_LAMBDA_K8S_IMAGE_PULL_POLICY must be IfNotPresent, Never or Always")
	case cfg.MaxPods < 1:
		return errors.New("OPENSNACK_LAMBDA_K8S_MAX_PODS must be at least 1")
	case cfg.GuestOverheadMB < 0:
		return errors.New("OPENSNACK_LAMBDA_K8S_GUEST_OVERHEAD_MB can't be negative")
	case cfg.MBPerVCPU < 1:
		return errors.New("OPENSNACK_LAMBDA_K8S_MB_PER_VCPU must be at least 1")
	case cfg.CPURequestMillis < 0:
		return errors.New("OPENSNACK_LAMBDA_K8S_CPU_REQUEST_MILLIS can't be negative")
	case cfg.UIDBase < 1 || cfg.UIDCount < 1:
		return errors.New("OPENSNACK_LAMBDA_K8S_UID_BASE and _UID_COUNT must be at least 1 (sandboxes never run as root)")
	}
	if _, _, err := net.SplitHostPort(cfg.RuntimeAPI); err != nil {
		return fmt.Errorf("OPENSNACK_LAMBDA_K8S_RUNTIME_API: %w", err)
	}
	for family, image := range cfg.Images {
		if image != "" && !strings.Contains(image, "@sha256:") {
			zap.L().Warn("lambda runtime image is not pinned by digest", zap.String("runtime", family), zap.String("image", image))
		}
	}
	return nil
}

// KubernetesExecutorFromEnv builds the executor from the environment, with
// in-cluster credentials or OPENSNACK_LAMBDA_K8S_KUBECONFIG.
func KubernetesExecutorFromEnv() (*KubernetesExecutor, error) {
	cfg, err := KubernetesConfigFromEnv()
	if err != nil {
		return nil, err
	}
	kc, err := kube.Load(os.Getenv("OPENSNACK_LAMBDA_K8S_KUBECONFIG"), os.Getenv("OPENSNACK_LAMBDA_K8S_CONTEXT"))
	if err != nil {
		return nil, err
	}
	return NewKubernetesExecutor(cfg, kube.NewClient(kc, cfg.Namespace)), nil
}

func (e *KubernetesExecutor) SetCredentialIssuer(c CredentialIssuer) { e.creds = c }

// Handler is the Runtime API, for a caller that serves it itself.
func (e *KubernetesExecutor) Handler() http.Handler { return e.d }

// Start collects pods a previous run left behind, then serves the Runtime
// API and starts the loop that watches the pods.
func (e *KubernetesExecutor) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, err := e.collectOrphans(ctx); err != nil {
		return fmt.Errorf("lambda sandboxes: listing pods in %s: %w", e.cfg.Namespace, err)
	} else if n > 0 {
		zap.L().Info("lambda sandboxes: deleted pods left by a previous run", zap.Int("pods", n))
	}
	if e.cfg.ListenAddr != "" {
		ln, err := net.Listen("tcp", e.cfg.ListenAddr)
		if err != nil {
			return fmt.Errorf("lambda runtime API: %w", err)
		}
		// No write timeout: /next is a long poll.
		e.srv = &http.Server{Handler: e.d, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			if err := e.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				zap.L().Error("lambda runtime API stopped", zap.Error(err))
			}
		}()
		zap.L().Info("lambda runtime API listening", zap.String("addr", ln.Addr().String()))
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(e.cfg.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-t.C:
				e.Poll(context.Background())
			}
		}
	}()
	return nil
}

// Stop deletes every sandbox and stops serving.
func (e *KubernetesExecutor) Stop() {
	select {
	case <-e.stop:
		return
	default:
		close(e.stop)
	}
	e.d.retire(func(*sandbox) bool { return true })
	for _, sb := range e.d.snapshot() {
		e.d.remove(sb, sandboxDeath{Exit: true, Reason: "opensnack is shutting down"})
	}
	if e.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e.srv.Shutdown(ctx)
		cancel()
	}
	e.wg.Wait()
}

func (e *KubernetesExecutor) selector() string {
	return labelManagedBy + "=opensnack," + labelComponent + "=" + componentValue + "," + labelInstance + "=" + e.cfg.Instance
}

// collectOrphans deletes every sandbox pod this instance doesn't know.
func (e *KubernetesExecutor) collectOrphans(ctx context.Context) (int, error) {
	pods, err := e.pods.List(ctx, e.selector())
	if err != nil {
		return 0, err
	}
	known := e.knownPods()
	n := 0
	for _, p := range pods {
		if _, ok := known[p.Metadata.Name]; ok || p.Metadata.DeletionTimestamp != nil {
			continue
		}
		if err := e.pods.Delete(ctx, p.Metadata.Name); err == nil {
			n++
		}
	}
	return n, nil
}

func (e *KubernetesExecutor) knownPods() map[string]*sandbox {
	out := map[string]*sandbox{}
	for _, sb := range e.d.snapshot() {
		if pi, ok := sb.tag.(*podInfo); ok {
			out[pi.name] = sb
		}
	}
	return out
}

func hashHex(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func tenantHash(namespace string) string { return hashHex("tenant", namespace)[:16] }

func functionHash(namespace, region, function string) string {
	return hashHex("function", namespace, region, function)[:16]
}

// versionKey identifies what a warm pod can serve: one version of one
// function, with exactly this code and configuration.
func versionKey(inv *Invocation) string {
	keys := make([]string, 0, len(inv.Environment))
	for k := range inv.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{inv.Namespace, inv.Region, inv.AccountID, inv.FunctionName, inv.Version, inv.CodeSha256,
		inv.Runtime, inv.Handler, inv.Architecture, strconv.Itoa(inv.MemoryMB), inv.Timeout.String(), inv.Role, inv.LogGroup}
	for _, k := range keys {
		parts = append(parts, k+"="+inv.Environment[k])
	}
	return hashHex(parts...)
}

// podName is DNS-1123 safe and says nothing about the tenant but a hash.
func podName(namespace string) string {
	return "lambda-" + tenantHash(namespace)[:10] + "-" + randomLowerHex(10)
}

func randomLowerHex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[rand.IntN(16)]
	}
	return string(b)
}

func (e *KubernetesExecutor) Invoke(ctx context.Context, inv *Invocation) (*InvocationResult, error) {
	family := runtimeFamily(inv.Runtime)
	image := e.cfg.Images[family]
	if image == "" {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRuntime, inv.Runtime)
	}
	key := versionKey(inv)
	c := newCall(inv, key, e.d.logLimit)
	e.d.submit(c)
	if err := e.scale(inv, key, family, image); err != nil {
		if e.d.cancel(c, err) {
			return nil, err
		}
	}

	start := time.NewTimer(e.cfg.StartTimeout)
	defer start.Stop()
	select {
	case <-c.taken:
	case <-c.done:
	case <-ctx.Done():
		if e.d.cancel(c, ctx.Err()) {
			return nil, ctx.Err()
		}
	case <-start.C:
		if e.d.cancel(c, fmt.Errorf("no sandbox started within %s", e.cfg.StartTimeout)) {
			return nil, c.err
		}
	}
	// Once a sandbox has the invocation it runs to the end (its timeout
	// still applies) even if the caller goes, and Invoke waits for it, so
	// the invocation keeps its place under the concurrency caps.
	<-c.done
	if c.err != nil {
		return nil, c.err
	}
	res := c.result
	e.d.mu.Lock()
	if sb := c.sb; sb != nil {
		if pi, ok := sb.tag.(*podInfo); ok {
			res.LogStream = pi.logStream
		}
	}
	e.d.mu.Unlock()
	return res, nil
}

// scale starts pods until every queued invocation of key has a sandbox
// coming. At the pod cap, an idle pod of another function makes way; with
// none, the newest invocation is throttled.
func (e *KubernetesExecutor) scale(inv *Invocation, key, family, image string) error {
	e.mu.Lock()
	pending, idle, starting := e.d.keyStats(key)
	need := pending - idle - starting
	var victims []*sandbox
	var reserved []*sandbox
	for i := 0; i < need; i++ {
		live := e.d.snapshot()
		if len(live)+len(reserved)-len(victims) >= e.cfg.MaxPods {
			v := e.leastRecentlyIdle(live, victims)
			if v == nil {
				break
			}
			victims = append(victims, v)
		}
		sb, err := e.newSandbox(inv, key, image)
		if err != nil {
			e.mu.Unlock()
			e.retireSandboxes(victims)
			for _, r := range reserved {
				e.d.remove(r, sandboxDeath{Reason: err.Error()})
			}
			return err
		}
		e.d.register(sb)
		reserved = append(reserved, sb)
	}
	e.mu.Unlock()
	e.retireSandboxes(victims)
	for _, sb := range reserved {
		e.createPod(inv, sb, family, image)
	}
	if need > 0 && len(reserved) == 0 {
		return ErrThrottled
	}
	return nil
}

func (e *KubernetesExecutor) retireSandboxes(sbs []*sandbox) {
	if len(sbs) == 0 {
		return
	}
	set := map[*sandbox]bool{}
	for _, sb := range sbs {
		set[sb] = true
	}
	e.d.retire(func(sb *sandbox) bool { return set[sb] })
}

// leastRecentlyIdle picks the idle sandbox unused for longest.
func (e *KubernetesExecutor) leastRecentlyIdle(live, skip []*sandbox) *sandbox {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()
	var best *sandbox
outer:
	for _, sb := range live {
		if !sb.idle() || sb.retiring {
			continue
		}
		for _, s := range skip {
			if s == sb {
				continue outer
			}
		}
		if best == nil || sb.lastUsed.Before(best.lastUsed) {
			best = sb
		}
	}
	return best
}

// newSandbox prepares a sandbox for inv's function version: its
// credentials, environment and pod name.
func (e *KubernetesExecutor) newSandbox(inv *Invocation, key, image string) (*sandbox, error) {
	opts := sandboxOptions{
		Paths:       sandboxPaths{TaskRoot: "/var/task", RuntimeDir: "/var/runtime", Tmp: "/tmp"},
		LogStream:   newLogStream(inv.Version),
		EndpointURL: e.cfg.EndpointURL,
	}
	if e.cfg.CAConfigMap != "" {
		bundle := "/tmp/.opensnack/ca-bundle.pem" // written by lambda-init
		opts.Extra = map[string]string{
			"AWS_CA_BUNDLE":       bundle,
			"SSL_CERT_FILE":       bundle,
			"REQUESTS_CA_BUNDLE":  bundle,
			"NODE_EXTRA_CA_CERTS": caMountPath + "/ca.crt",
		}
	}
	var accessKey string
	if e.creds != nil && inv.Role != "" {
		session := inv.FunctionName
		if len(session) > 64 {
			session = session[:64]
		}
		creds, err := e.creds.RoleCredentials(inv.Namespace, inv.AccountID, inv.Role, session, credentialTTL(e.cfg.MaxLifetime))
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "InvalidParameterValueException", "The role defined for the function cannot be assumed by Lambda."}
		}
		opts.Creds = creds
		accessKey = creds.AccessKeyID
	}
	sb := e.d.newSandbox(key, sandboxEnv(inv, opts), inv.LoadCode)
	pi := &podInfo{
		name:      podName(inv.Namespace),
		tenant:    tenantHash(inv.Namespace),
		function:  functionHash(inv.Namespace, inv.Region, inv.FunctionName),
		logStream: opts.LogStream,
	}
	sb.tag = pi
	var once sync.Once
	sb.stop = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := e.pods.Delete(ctx, pi.name); err != nil {
				zap.L().Warn("lambda sandbox: deleting pod failed; it will be collected later", zap.String("pod", pi.name), zap.Error(err))
			}
			if accessKey != "" {
				e.creds.Revoke(accessKey)
			}
		})
	}
	return sb, nil
}

func (e *KubernetesExecutor) createPod(inv *Invocation, sb *sandbox, family, image string) {
	pi := sb.tag.(*podInfo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.pods.Create(ctx, e.podSpec(inv, sb, family, image)); err != nil {
		zap.L().Warn("lambda sandbox: creating pod failed", zap.String("pod", pi.name), zap.Error(err))
		e.d.remove(sb, sandboxDeath{Reason: "creating the sandbox pod failed: " + err.Error()})
		return
	}
	e.d.mu.Lock()
	pi.created = true
	e.d.mu.Unlock()
}

func boolPtr(b bool) *bool    { return &b }
func int64Ptr(n int64) *int64 { return &n }

// cpuMillis is the CPU limit for a memory size, in proportion as Lambda
// allocates it.
func (e *KubernetesExecutor) cpuMillis(memoryMB int) int {
	m := memoryMB * 1000 / e.cfg.MBPerVCPU
	if m < e.cfg.MinCPUMillis {
		m = e.cfg.MinCPUMillis
	}
	return max(m, 10)
}

// podSpec is the sandbox pod: locked down, sized from the function, and
// told nothing but how to reach the Runtime API.
func (e *KubernetesExecutor) podSpec(inv *Invocation, sb *sandbox, family, image string) *kube.Pod {
	pi := sb.tag.(*podInfo)
	uid := e.cfg.UIDBase + rand.Int64N(e.cfg.UIDCount)
	mem := strconv.Itoa(inv.MemoryMB+e.cfg.GuestOverheadMB) + "Mi"
	limitMillis := e.cpuMillis(inv.MemoryMB)
	limits := map[string]string{"memory": mem, "cpu": strconv.Itoa(limitMillis) + "m"}
	requests := limits
	if r := e.cfg.CPURequestMillis; r > 0 && r < limitMillis {
		requests = map[string]string{"memory": mem, "cpu": strconv.Itoa(r) + "m"}
	}
	seccomp := &kube.SeccompProfile{Type: "RuntimeDefault"}

	env := []kube.EnvVar{
		{Name: "AWS_LAMBDA_RUNTIME_API", Value: e.cfg.RuntimeAPI},
		{Name: runtimeTokenEnv, Value: sb.token()},
		{Name: "_OPENSNACK_RUNTIME", Value: family},
		{Name: "_OPENSNACK_MAX_PROCS", Value: strconv.Itoa(e.cfg.MaxProcs)},
	}
	mounts := []kube.VolumeMount{
		{Name: "tmp", MountPath: "/tmp"},
		{Name: "task", MountPath: "/var/task"},
	}
	volumes := []kube.Volume{
		{Name: "tmp", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: e.cfg.TmpSize}},
		{Name: "task", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: e.cfg.TaskSize}},
	}
	if e.cfg.CAConfigMap != "" {
		env = append(env, kube.EnvVar{Name: "_OPENSNACK_CA_FILE", Value: caMountPath + "/ca.crt"})
		mounts = append(mounts, kube.VolumeMount{Name: "ca", MountPath: caMountPath, ReadOnly: true})
		volumes = append(volumes, kube.Volume{Name: "ca", ConfigMap: &kube.ConfigMapVolumeSource{Name: e.cfg.CAConfigMap}})
	}

	spec := kube.PodSpec{
		ServiceAccountName:            e.cfg.ServiceAccount,
		AutomountServiceAccountToken:  boolPtr(false),
		EnableServiceLinks:            boolPtr(false),
		RestartPolicy:                 "Never",
		ActiveDeadlineSeconds:         int64Ptr(int64((e.cfg.MaxLifetime + 16*time.Minute).Seconds())),
		TerminationGracePeriodSeconds: int64Ptr(1),
		ShareProcessNamespace:         boolPtr(false),
		SecurityContext: &kube.PodSecurityContext{
			RunAsNonRoot:   boolPtr(true),
			RunAsUser:      int64Ptr(uid),
			RunAsGroup:     int64Ptr(uid),
			SeccompProfile: seccomp,
		},
		Containers: []kube.Container{{
			Name:            "function",
			Image:           image,
			ImagePullPolicy: e.cfg.PullPolicy,
			Env:             env,
			Resources:       kube.ResourceRequirements{Limits: limits, Requests: requests},
			SecurityContext: &kube.SecurityContext{
				Privileged:               boolPtr(false),
				AllowPrivilegeEscalation: boolPtr(false),
				ReadOnlyRootFilesystem:   boolPtr(true),
				RunAsNonRoot:             boolPtr(true),
				RunAsUser:                int64Ptr(uid),
				RunAsGroup:               int64Ptr(uid),
				Capabilities:             &kube.Capabilities{Drop: []string{"ALL"}},
				SeccompProfile:           seccomp,
			},
			VolumeMounts: mounts,
		}},
		Volumes: volumes,
	}
	if e.cfg.RuntimeClass != "" {
		rc := e.cfg.RuntimeClass
		spec.RuntimeClassName = &rc
	}
	return &kube.Pod{
		Metadata: kube.ObjectMeta{
			Name:      pi.name,
			Namespace: e.cfg.Namespace,
			Labels: map[string]string{
				labelManagedBy: "opensnack",
				labelComponent: componentValue,
				labelInstance:  e.cfg.Instance,
				labelTenant:    pi.tenant,
				labelFunction:  pi.function,
				labelRuntime:   family,
			},
		},
		Spec: spec,
	}
}

// podIP looks a sandbox's address up, for Runtime API requests that arrive
// before Poll has seen it.
func (e *KubernetesExecutor) podIP(sb *sandbox) string {
	pi, ok := sb.tag.(*podInfo)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := e.pods.Get(ctx, pi.name)
	if err != nil {
		return ""
	}
	return p.Status.PodIP
}

// Poll makes one pass over the sandbox pods: it notices pods that died or
// never started, retires idle and elderly ones, and deletes pods nothing
// knows about.
func (e *KubernetesExecutor) Poll(ctx context.Context) {
	now := e.now()
	e.d.reap(now, e.cfg.IdleTTL, e.cfg.MaxLifetime)

	pods, err := e.pods.List(ctx, e.selector())
	if err != nil {
		zap.L().Warn("lambda sandboxes: listing pods failed", zap.Error(err))
		return
	}
	known := e.knownPods()
	seen := map[string]bool{}
	for i := range pods {
		p := &pods[i]
		name := p.Metadata.Name
		seen[name] = true
		sb, ok := known[name]
		if !ok {
			if p.Metadata.DeletionTimestamp == nil {
				e.pods.Delete(ctx, name)
			}
			continue
		}
		if p.Status.PodIP != "" {
			e.d.mu.Lock()
			if sb.ip == "" {
				sb.ip = p.Status.PodIP
			}
			e.d.mu.Unlock()
		}
		if why, dead := podDeath(p); dead {
			e.d.remove(sb, why)
		}
	}
	for name, sb := range known {
		e.d.mu.Lock()
		pi := sb.tag.(*podInfo)
		created, registered, age := pi.created, sb.registered, now.Sub(sb.created)
		e.d.mu.Unlock()
		switch {
		case created && !seen[name]:
			e.d.remove(sb, sandboxDeath{Exit: true, Reason: "Runtime exited with error: sandbox was deleted"})
		case !registered && age > e.cfg.StartTimeout:
			e.d.remove(sb, sandboxDeath{Reason: fmt.Sprintf("the sandbox did not start within %s", e.cfg.StartTimeout)})
		}
	}
	// Invocations queued for a sandbox that has since gone need another.
	for key, inv := range e.d.waiting() {
		family := runtimeFamily(inv.Runtime)
		e.scale(inv, key, family, e.cfg.Images[family])
	}
}

// podDeath reads a pod's status: whether its function container has
// stopped (or can never start), and why, in the words Lambda uses.
func podDeath(p *kube.Pod) (sandboxDeath, bool) {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			return sandboxDeath{Exit: true, Reason: exitReason(t)}, true
		}
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError",
				"CreateContainerError", "RunContainerError":
				return sandboxDeath{Reason: w.Reason + ": " + w.Message}, true
			}
		}
	}
	switch p.Status.Phase {
	case kube.PodFailed, kube.PodSucceeded:
		msg := "Runtime exited with error: signal: killed"
		if p.Status.Reason != "" && p.Status.Reason != "DeadlineExceeded" {
			msg = "Runtime exited with error: " + p.Status.Reason
		}
		return sandboxDeath{Exit: true, Reason: msg}, true
	}
	return sandboxDeath{}, false
}

func exitReason(t *kube.ContainerStateTerminated) string {
	switch {
	case t.Reason == "OOMKilled", t.ExitCode == 137, t.Signal == 9:
		return "Runtime exited with error: signal: killed"
	case t.Signal != 0:
		return fmt.Sprintf("Runtime exited with error: signal %d", t.Signal)
	default:
		return fmt.Sprintf("Runtime exited with error: exit status %d", t.ExitCode)
	}
}

// RetireFunction stops a function's warm pods (after any invocation in
// flight) when it is updated or deleted.
func (e *KubernetesExecutor) RetireFunction(namespace, region, function string) {
	want := functionHash(namespace, region, function)
	e.d.retire(func(sb *sandbox) bool {
		pi, ok := sb.tag.(*podInfo)
		return ok && pi.function == want
	})
}

// RetireNamespace kills every pod a namespace has, busy or not.
func (e *KubernetesExecutor) RetireNamespace(namespace string) {
	want := tenantHash(namespace)
	for _, sb := range e.d.snapshot() {
		if pi, ok := sb.tag.(*podInfo); ok && pi.tenant == want {
			e.d.remove(sb, sandboxDeath{Exit: true, Reason: "Runtime exited with error: the namespace was deleted"})
		}
	}
}
