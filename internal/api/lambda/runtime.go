// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//
// RUNTIME API
//
// Executors hand invocations to function code through the Lambda Runtime
// API, served by opensnack itself, so a sandbox runs the same loop it would
// in AWS and stays warm between invocations:
//
//	GET  /2018-06-01/runtime/invocation/next
//	POST /2018-06-01/runtime/invocation/{id}/response
//	POST /2018-06-01/runtime/invocation/{id}/error
//	POST /2018-06-01/runtime/init/error
//
// plus extensions the bundled bootstraps use:
//
//	GET  /2018-06-01/runtime/opensnack/init      the sandbox's environment, JSON
//	GET  /2018-06-01/runtime/opensnack/code      the deployment package
//	POST /2018-06-01/runtime/invocation/{id}/logs   output, appended
//	POST /2018-06-01/runtime/init/logs              output before the first invocation
//
// Each sandbox (a process or a pod) is registered with a token, sent as
// X-Opensnack-Runtime-Token, and can optionally be pinned to its source
// address. A token reaches only its own sandbox: the invocations of the one
// function version it was started for, and that version's code and
// environment.
//
// The API must only be reachable by sandboxes. LocalExecutor serves it on
// loopback; KubernetesExecutor on its own listener, which the public edge
// never routes to.
//

const (
	runtimeTokenHeader = "X-Opensnack-Runtime-Token"
	runtimeTokenEnv    = "_OPENSNACK_RUNTIME_TOKEN"
	maxRSSHeader       = "X-Opensnack-Max-Rss-Kb"
	runtimePrefix      = "/2018-06-01/runtime/"
	maxErrorDoc        = 1 << 20
)

// sandbox is one execution environment: it serves one function version,
// one invocation at a time.
type sandbox struct {
	id     string
	secret string
	// key names the function version; the sandbox takes only invocations
	// submitted under it.
	key string
	// env and code are what the sandbox fetches at start (code may be nil
	// when the executor delivers it another way).
	env  map[string]string
	code func() ([]byte, error)
	// stop tears the sandbox down (kills the process, deletes the pod). It
	// runs without the dispatcher's lock and must be idempotent.
	stop    func()
	created time.Time
	// tag is the executor's own bookkeeping (the pod's labels, say).
	tag any

	// The rest is guarded by dispatcher.mu.
	ip         string // when set, requests must come from this address
	registered bool   // has called /next or /init
	waiting    chan *call
	waitCancel chan struct{}
	current    *call
	lastUsed   time.Time
	dead       bool
	retiring   bool
	gone       chan struct{}
	initLogs   *cappedBuffer
	maxRSSKB   int
	requests   int // Runtime API requests in progress
}

// maxSandboxRequests bounds a sandbox's Runtime API requests in progress,
// so function code can't tie up the listener with connections.
const maxSandboxRequests = 8

func (sb *sandbox) token() string { return sb.id + "." + sb.secret }

// idle reports whether the sandbox is blocked in /next with nothing to do.
func (sb *sandbox) idle() bool { return sb.waiting != nil && sb.current == nil && !sb.dead }

// call is one invocation on its way through the dispatcher.
type call struct {
	inv *Invocation
	key string
	// taken closes when a sandbox has the invocation (its timeout starts
	// then, so a cold start doesn't count); done when the result is in.
	taken chan struct{}
	done  chan struct{}

	// Guarded by dispatcher.mu.
	sb       *sandbox
	started  time.Time
	deadline time.Time
	timer    *time.Timer
	logs     *cappedBuffer
	result   *InvocationResult
	err      error
	isTaken  bool
}

func newCall(inv *Invocation, key string, logLimit int) *call {
	return &call{inv: inv, key: key, taken: make(chan struct{}), done: make(chan struct{}), logs: &cappedBuffer{max: logLimit}}
}

// sandboxDeath says why a sandbox went away. Exit means the function's
// own process ended (a crash, the OOM killer): an invocation it held fails
// with Runtime.ExitError. Otherwise the sandbox itself failed (a pod that
// never started) and the invocation could not be run.
type sandboxDeath struct {
	Exit   bool
	Reason string
}

type dispatcher struct {
	mu        sync.Mutex
	sandboxes map[string]*sandbox
	pending   map[string][]*call
	logLimit  int
	now       func() time.Time
	// bindIP makes each sandbox's requests come from the address lookupIP
	// reports for it.
	bindIP   bool
	lookupIP func(sb *sandbox) string
}

func newDispatcher(logLimit int) *dispatcher {
	return &dispatcher{sandboxes: map[string]*sandbox{}, pending: map[string][]*call{}, logLimit: logLimit, now: time.Now}
}

func newSandboxID() (id, secret string) {
	b := make([]byte, 40)
	rand.Read(b)
	return hex.EncodeToString(b[:8]), hex.EncodeToString(b[8:])
}

func (d *dispatcher) newSandbox(key string, env map[string]string, code func() ([]byte, error)) *sandbox {
	id, secret := newSandboxID()
	return &sandbox{
		id: id, secret: secret, key: key, env: env, code: code,
		created: d.now(), lastUsed: d.now(),
		gone: make(chan struct{}), initLogs: &cappedBuffer{max: d.logLimit},
	}
}

func (d *dispatcher) register(sb *sandbox) {
	d.mu.Lock()
	d.sandboxes[sb.id] = sb
	d.mu.Unlock()
}

// submit queues c, handing it straight to an idle sandbox when there is one.
func (d *dispatcher) submit(c *call) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, sb := range d.sandboxes {
		if sb.key == c.key && sb.idle() && !sb.retiring {
			d.handTo(sb, c)
			return
		}
	}
	d.pending[c.key] = append(d.pending[c.key], c)
}

// handTo gives c to a sandbox blocked in /next; next() confirms it. mu held.
func (d *dispatcher) handTo(sb *sandbox, c *call) {
	sb.current = c
	ch := sb.waiting
	sb.waiting = nil
	ch <- c
}

// start records that sb has c and starts its timeout. mu held.
func (d *dispatcher) start(sb *sandbox, c *call) {
	sb.current = c
	c.sb = sb
	c.isTaken = true
	c.started = d.now()
	c.deadline = c.started.Add(c.inv.Timeout)
	if sb.initLogs.buf.Len() > 0 {
		c.logs.Write(sb.initLogs.buf.Bytes())
		sb.initLogs = &cappedBuffer{max: d.logLimit}
	}
	c.timer = time.AfterFunc(c.inv.Timeout, func() { d.timeout(c) })
	close(c.taken)
}

var errSandboxGone = errors.New("sandbox is gone")

// next blocks until there is an invocation for sb, the request is
// abandoned, or the sandbox goes away.
func (d *dispatcher) next(done <-chan struct{}, sb *sandbox) (*call, error) {
	d.mu.Lock()
	if sb.dead {
		d.mu.Unlock()
		return nil, errSandboxGone
	}
	if sb.current != nil {
		d.mu.Unlock()
		return nil, errors.New("an invocation is already in progress")
	}
	// A new /next replaces one already waiting: the runtime only sends
	// another when it has given up on the first (a dropped connection).
	if sb.waiting != nil {
		close(sb.waitCancel)
		sb.waiting = nil
	}
	sb.registered = true
	sb.lastUsed = d.now()
	if q := d.pending[sb.key]; len(q) > 0 && !sb.retiring {
		c := q[0]
		d.pending[sb.key] = q[1:]
		d.start(sb, c)
		d.mu.Unlock()
		return c, nil
	}
	ch, cancel := make(chan *call, 1), make(chan struct{})
	sb.waiting, sb.waitCancel = ch, cancel
	d.mu.Unlock()

	select {
	case c := <-ch:
		d.mu.Lock()
		defer d.mu.Unlock()
		if sb.dead || sb.current != c {
			return nil, errSandboxGone // markDead put it back
		}
		d.start(sb, c)
		return c, nil
	case <-done:
	case <-cancel:
	case <-sb.gone:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if sb.waiting == ch {
		sb.waiting = nil
	}
	// Handed an invocation just as the request went away: put it back.
	select {
	case <-ch:
		d.unreserve(sb)
	default:
	}
	return nil, errSandboxGone
}

// finish records the result of sb's current invocation. mu held; returns
// the sandbox to stop, if it is retiring.
func (d *dispatcher) finish(c *call, res *InvocationResult, err error) *sandbox {
	if c.result != nil || c.err != nil {
		return nil
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	sb := c.sb
	if res != nil {
		if c.isTaken {
			res.Duration = d.now().Sub(c.started)
		}
		res.Logs = splitLines(c.logs.String())
		if sb != nil && sb.maxRSSKB > 0 {
			res.MaxMemoryMB = (sb.maxRSSKB + 1023) / 1024
		}
	}
	c.result, c.err = res, err
	if c.result == nil && c.err == nil {
		c.err = errors.New("invocation ended without a result")
	}
	if !c.isTaken {
		d.dropPending(c)
	}
	close(c.done)
	if sb != nil && sb.current == c {
		sb.current = nil
		sb.lastUsed = d.now()
		if sb.retiring && !sb.dead {
			d.markDead(sb)
			return sb
		}
	}
	return nil
}

func (d *dispatcher) dropPending(c *call) {
	q := d.pending[c.key]
	for i, p := range q {
		if p == c {
			d.pending[c.key] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	if len(d.pending[c.key]) == 0 {
		delete(d.pending, c.key)
	}
}

// markDead takes sb out of service. mu held; the caller stops it. An
// invocation handed to it but not yet started goes back to the queue.
func (d *dispatcher) markDead(sb *sandbox) {
	if sb.dead {
		return
	}
	sb.dead = true
	delete(d.sandboxes, sb.id)
	close(sb.gone)
	d.unreserve(sb)
}

// unreserve puts back an invocation handed to sb that it never started.
// mu held.
func (d *dispatcher) unreserve(sb *sandbox) {
	c := sb.current
	if c == nil || c.isTaken {
		return
	}
	sb.current = nil
	if c.result == nil && c.err == nil {
		d.pending[c.key] = append([]*call{c}, d.pending[c.key]...)
	}
}

func stopAll(sbs ...*sandbox) {
	for _, sb := range sbs {
		if sb != nil && sb.stop != nil {
			sb.stop()
		}
	}
}

// timeout ends c as Sandbox.Timedout and kills its sandbox, which may be
// stuck in the handler.
func (d *dispatcher) timeout(c *call) {
	d.mu.Lock()
	if c.result != nil || c.err != nil {
		d.mu.Unlock()
		return
	}
	sb := c.sb
	res := functionError("Sandbox.Timedout", fmt.Sprintf("RequestId: %s Error: Task timed out after %.2f seconds", c.inv.RequestID, c.inv.Timeout.Seconds()))
	d.finish(c, res, nil)
	if sb != nil {
		d.markDead(sb)
	}
	d.mu.Unlock()
	stopAll(sb)
}

// cancel withdraws c if no sandbox has it yet. It reports whether it did.
func (d *dispatcher) cancel(c *call, err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.isTaken || c.result != nil || c.err != nil || c.sb != nil {
		return false
	}
	for _, sb := range d.sandboxes {
		if sb.current == c {
			return false // on its way to a sandbox
		}
	}
	d.finish(c, nil, err)
	return true
}

// remove takes a sandbox out of service because it died or was retired,
// failing the invocation it held. A sandbox that dies before taking any
// invocation fails the oldest one waiting for its function, so a crash at
// start-up surfaces instead of leaving callers to time out.
func (d *dispatcher) remove(sb *sandbox, why sandboxDeath) {
	d.mu.Lock()
	if sb.dead {
		d.mu.Unlock()
		stopAll(sb)
		return
	}
	d.markDead(sb)
	c := sb.current
	if c == nil && !sb.registered {
		if q := d.pending[sb.key]; len(q) > 0 && d.liveFor(sb.key) == 0 {
			c = q[0]
		}
	}
	if c != nil {
		reqID := c.inv.RequestID
		if why.Exit {
			msg := why.Reason
			if msg == "" {
				msg = "Runtime exited without providing a reason"
			}
			d.finish(c, functionError("Runtime.ExitError", fmt.Sprintf("RequestId: %s Error: %s", reqID, msg)), nil)
		} else {
			d.finish(c, nil, fmt.Errorf("the sandbox failed: %s", why.Reason))
		}
	}
	d.mu.Unlock()
	stopAll(sb)
}

// liveFor counts the live sandboxes serving key. mu held.
func (d *dispatcher) liveFor(key string) int {
	n := 0
	for _, sb := range d.sandboxes {
		if sb.key == key && !sb.dead {
			n++
		}
	}
	return n
}

// retire stops every sandbox match selects: idle ones at once, busy ones
// when their invocation ends, as Lambda lets in-flight work finish.
func (d *dispatcher) retire(match func(sb *sandbox) bool) int {
	d.mu.Lock()
	var stop []*sandbox
	n := 0
	for _, sb := range d.sandboxes {
		if !match(sb) {
			continue
		}
		n++
		if sb.current != nil && sb.current.isTaken {
			sb.retiring = true
			continue
		}
		d.markDead(sb)
		stop = append(stop, sb)
	}
	d.mu.Unlock()
	stopAll(stop...)
	return n
}

// reap retires sandboxes idle for longer than idleTTL, and those older
// than maxLife: at once if idle, else when their invocation ends.
func (d *dispatcher) reap(now time.Time, idleTTL, maxLife time.Duration) {
	d.mu.Lock()
	var stop []*sandbox
	for _, sb := range d.sandboxes {
		old := maxLife > 0 && now.Sub(sb.created) > maxLife
		switch {
		case sb.idle() && (now.Sub(sb.lastUsed) > idleTTL || old):
			d.markDead(sb)
			stop = append(stop, sb)
		case old && sb.registered:
			sb.retiring = true
		}
	}
	d.mu.Unlock()
	stopAll(stop...)
}

// keyStats counts what serves key: invocations waiting for a sandbox,
// sandboxes free for one (idle in /next, or between invocations and about
// to ask for the next), and sandboxes not yet started.
func (d *dispatcher) keyStats(key string) (pending, idle, starting int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	pending = len(d.pending[key])
	for _, sb := range d.sandboxes {
		if sb.key != key || sb.dead || sb.retiring {
			continue
		}
		switch {
		case !sb.registered:
			starting++
		case sb.current == nil:
			idle++
		}
	}
	return
}

// waiting returns, for each function version with invocations queued, the
// oldest of them.
func (d *dispatcher) waiting() map[string]*Invocation {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]*Invocation{}
	for key, q := range d.pending {
		if len(q) > 0 {
			out[key] = q[0].inv
		}
	}
	return out
}

// snapshot lists the live sandboxes.
func (d *dispatcher) snapshot() []*sandbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*sandbox, 0, len(d.sandboxes))
	for _, sb := range d.sandboxes {
		out = append(out, sb)
	}
	return out
}

func (d *dispatcher) lookup(id string) *sandbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sandboxes[id]
}

//
// HTTP
//

func (d *dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sb, ok := d.authenticate(r)
	if !ok {
		runtimeError(w, http.StatusForbidden, "AccessDenied", "Unknown or invalid runtime token.")
		return
	}
	d.mu.Lock()
	if sb.requests >= maxSandboxRequests {
		d.mu.Unlock()
		runtimeError(w, http.StatusTooManyRequests, "TooManyRequests", "Too many runtime API requests in progress.")
		return
	}
	sb.requests++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		sb.requests--
		d.mu.Unlock()
	}()
	path := strings.TrimPrefix(r.URL.Path, runtimePrefix)
	if path == r.URL.Path {
		runtimeError(w, http.StatusNotFound, "NotFound", "Unknown runtime API path.")
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "invocation/next":
		d.serveNext(w, r, sb)
	case r.Method == http.MethodPost && path == "init/error":
		d.serveInitError(w, r, sb)
	case r.Method == http.MethodPost && path == "init/logs":
		body, _ := io.ReadAll(io.LimitReader(r.Body, int64(d.logLimit)+1))
		d.mu.Lock()
		sb.initLogs.Write(body)
		d.mu.Unlock()
		accepted(w)
	case r.Method == http.MethodGet && path == "opensnack/init":
		d.mu.Lock()
		sb.registered = true
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"env": sb.env})
	case r.Method == http.MethodGet && path == "opensnack/code":
		if sb.code == nil {
			runtimeError(w, http.StatusNotFound, "NotFound", "This sandbox's code is delivered another way.")
			return
		}
		zip, err := sb.code()
		if err != nil {
			runtimeError(w, http.StatusInternalServerError, "ServiceException", "The function's code could not be read.")
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.Itoa(len(zip)))
		w.Write(zip)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "invocation/"):
		id, action, _ := strings.Cut(strings.TrimPrefix(path, "invocation/"), "/")
		d.serveResult(w, r, sb, id, action)
	default:
		runtimeError(w, http.StatusNotFound, "NotFound", "Unknown runtime API path.")
	}
}

func (d *dispatcher) authenticate(r *http.Request) (*sandbox, bool) {
	id, secret, ok := strings.Cut(r.Header.Get(runtimeTokenHeader), ".")
	if !ok {
		return nil, false
	}
	sb := d.lookup(id)
	if sb == nil || subtle.ConstantTimeCompare([]byte(secret), []byte(sb.secret)) != 1 {
		return nil, false
	}
	if !d.bindIP {
		return sb, true
	}
	d.mu.Lock()
	ip := sb.ip
	d.mu.Unlock()
	if ip == "" && d.lookupIP != nil {
		ip = d.lookupIP(sb)
		d.mu.Lock()
		sb.ip = ip
		d.mu.Unlock()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	from, want := net.ParseIP(host), net.ParseIP(ip)
	if from == nil || want == nil || !from.Equal(want) {
		return nil, false
	}
	return sb, true
}

func (d *dispatcher) serveNext(w http.ResponseWriter, r *http.Request, sb *sandbox) {
	// Long polls outlive the server's write timeout.
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	c, err := d.next(r.Context().Done(), sb)
	if err != nil {
		if errors.Is(err, errSandboxGone) {
			runtimeError(w, http.StatusGone, "SandboxGone", "This sandbox has been shut down.")
			return
		}
		runtimeError(w, http.StatusBadRequest, "InvalidStateTransition", err.Error())
		return
	}
	inv := c.inv
	h := w.Header()
	h.Set("Lambda-Runtime-Aws-Request-Id", inv.RequestID)
	h.Set("Lambda-Runtime-Deadline-Ms", strconv.FormatInt(c.deadline.UnixMilli(), 10))
	h.Set("Lambda-Runtime-Invoked-Function-Arn", inv.InvokedARN)
	h.Set("Lambda-Runtime-Trace-Id", traceID(c.started))
	if len(inv.ClientContext) > 0 {
		h.Set("Lambda-Runtime-Client-Context", string(inv.ClientContext))
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(inv.Payload)
}

func (d *dispatcher) serveResult(w http.ResponseWriter, r *http.Request, sb *sandbox, id, action string) {
	d.mu.Lock()
	c := sb.current
	d.mu.Unlock()
	if c == nil || !c.isTaken || c.inv.RequestID != id {
		runtimeError(w, http.StatusBadRequest, "InvalidRequestID", "Invalid request ID: \""+id+"\"")
		return
	}
	if rss, err := strconv.Atoi(r.Header.Get(maxRSSHeader)); err == nil && rss > 0 {
		d.mu.Lock()
		if rss > sb.maxRSSKB {
			sb.maxRSSKB = rss
		}
		d.mu.Unlock()
	}
	switch action {
	case "logs":
		body, _ := io.ReadAll(io.LimitReader(r.Body, int64(d.logLimit)+1))
		d.mu.Lock()
		c.logs.Write(body)
		d.mu.Unlock()
		accepted(w)
		return
	case "response":
		// One byte over the limit is enough for run() to report
		// Function.ResponseSizeTooLarge.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSyncPayload+1))
		if err != nil {
			runtimeError(w, http.StatusBadRequest, "InvalidPayload", "Could not read the response.")
			return
		}
		if len(body) == 0 {
			body = []byte("null")
		}
		d.complete(c, &InvocationResult{Payload: body})
	case "error":
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxErrorDoc))
		d.complete(c, &InvocationResult{Payload: errorDocument(body, r.Header.Get("Lambda-Runtime-Function-Error-Type")), FunctionError: "Unhandled"})
	default:
		runtimeError(w, http.StatusNotFound, "NotFound", "Unknown runtime API path.")
		return
	}
	accepted(w)
}

func (d *dispatcher) complete(c *call, res *InvocationResult) {
	d.mu.Lock()
	stop := d.finish(c, res, nil)
	d.mu.Unlock()
	stopAll(stop)
}

// serveInitError fails the invocation the sandbox was started for (the
// oldest one waiting) with the runtime's error, then retires the sandbox:
// Lambda re-runs init on the next invocation.
func (d *dispatcher) serveInitError(w http.ResponseWriter, r *http.Request, sb *sandbox) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxErrorDoc))
	doc := errorDocument(body, r.Header.Get("Lambda-Runtime-Function-Error-Type"))
	d.mu.Lock()
	var stop []*sandbox
	if !sb.dead {
		d.markDead(sb)
		stop = append(stop, sb)
		if q := d.pending[sb.key]; len(q) > 0 {
			c := q[0]
			c.logs.Write(sb.initLogs.buf.Bytes())
			d.finish(c, &InvocationResult{Payload: doc, FunctionError: "Unhandled"}, nil)
		}
	}
	d.mu.Unlock()
	stopAll(stop...)
	accepted(w)
}

// errorDocument is the payload Invoke returns for a runtime's error: its
// JSON as sent, or a document built around whatever it sent instead.
func errorDocument(body []byte, errType string) []byte {
	if len(body) > 0 && json.Valid(body) {
		return body
	}
	if errType == "" {
		errType = "Runtime.Unknown"
	}
	b, _ := json.Marshal(map[string]string{"errorType": errType, "errorMessage": string(body)})
	return b
}

func accepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"OK"}`))
}

func runtimeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"errorType": typ, "errorMessage": msg})
}

// traceID is an X-Ray trace header for the invocation (nothing reads it,
// but SDKs expect the variable to be well formed).
func traceID(t time.Time) string {
	b := make([]byte, 20)
	rand.Read(b)
	return fmt.Sprintf("Root=1-%08x-%s;Parent=%s;Sampled=0", t.Unix(), hex.EncodeToString(b[:12]), hex.EncodeToString(b[12:]))
}
