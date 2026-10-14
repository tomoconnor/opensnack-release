// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"opensnack/internal/util"
)

//
// EXECUTION
//
// Invoke hands the function to an Executor. Function code may be untrusted,
// so a shared deployment must run it in a sandbox, with no network but
// opensnack. LocalExecutor (executor_local.go) is for development only.
//

// Executor runs one invocation to completion.
//
// It returns an error only when it could not run the function at all
// (ErrUnsupportedRuntime, or the sandbox failed). Everything the function
// itself does wrong, including timing out, is a result with FunctionError
// set and the AWS error document as the payload.
type Executor interface {
	Invoke(ctx context.Context, inv *Invocation) (*InvocationResult, error)
}

// Invocation is everything an executor needs, resolved from the function's
// configuration at the invoked qualifier.
type Invocation struct {
	RequestID string
	// Namespace and Region scope the tenant. A sandbox should only reach
	// opensnack as this tenant.
	Namespace string
	Region    string
	AccountID string

	FunctionName string
	// InvokedARN is the ARN the caller used (qualified by an alias, a
	// version, or not at all), as context.invoked_function_arn reports it.
	InvokedARN   string
	Version      string
	Runtime      string
	Handler      string
	Architecture string
	MemoryMB     int
	Timeout      time.Duration
	Environment  map[string]string
	LogGroup     string
	LogStream    string
	// Role is the execution role, whose credentials the function gets.
	Role string
	// ReservedConcurrency is the function's ReservedConcurrentExecutions,
	// nil when it has none.
	ReservedConcurrency *int

	// LoadCode reads the deployment package, a zip. CodeSha256 identifies
	// it, so a warm sandbox can tell whether it still has the right code.
	LoadCode   func() ([]byte, error)
	CodeSha256 string

	Payload []byte
	// ClientContext is the decoded X-Amz-Client-Context, if any.
	ClientContext []byte
}

// InvocationResult is what the function produced.
type InvocationResult struct {
	Payload []byte
	// FunctionError is "Unhandled" when the function failed, "" otherwise.
	FunctionError string
	// Logs are the lines the function wrote to stdout and stderr.
	Logs []string
	// Duration is how long the handler ran.
	Duration time.Duration
	// MaxMemoryMB is the peak memory use, 0 if unknown.
	MaxMemoryMB int
	// LogStream, when set, replaces Invocation.LogStream: a warm sandbox
	// keeps writing to its own stream, as in Lambda.
	LogStream string
}

// ErrUnsupportedRuntime is returned by an executor that can't run the
// function's runtime.
var ErrUnsupportedRuntime = errors.New("unsupported runtime")

// LogEvent is one CloudWatch Logs event.
type LogEvent struct {
	Timestamp time.Time
	Message   string
}

// LogSink receives each invocation's log lines: the START/END/REPORT lines
// plus what the function printed. The router wires it to CloudWatch Logs.
// scope is the regional store scope (util.NamespaceFromHeader); accountID
// is the function's account, for the ARNs of groups created on the way.
type LogSink interface {
	PutLogEvents(ctx context.Context, scope, accountID, group, stream string, events []LogEvent) error
}

const (
	maxSyncPayload  = 6 << 20
	maxAsyncPayload = 1 << 20
	maxTail         = 4096
)

// maxLogBytes is OPENSNACK_LAMBDA_MAX_LOG_BYTES, the most output kept from
// one invocation (default 256 KiB).
func maxLogBytes() int {
	if v, err := strconv.Atoi(os.Getenv("OPENSNACK_LAMBDA_MAX_LOG_BYTES")); err == nil && v > 0 {
		return v
	}
	return 256 << 10
}

// executorFromEnv picks the executor from OPENSNACK_LAMBDA_EXECUTOR. Only
// "local" exists here; anything else leaves Invoke disabled.
func executorFromEnv() Executor {
	switch v := os.Getenv("OPENSNACK_LAMBDA_EXECUTOR"); v {
	case "":
		return nil
	case "local":
		zap.L().Warn("OPENSNACK_LAMBDA_EXECUTOR=local: Lambda functions run as plain processes on this host, " +
			"with no sandbox. This is for development only; never enable it where untrusted users can upload code.")
		return &LocalExecutor{}
	default:
		zap.L().Error("OPENSNACK_LAMBDA_EXECUTOR: unknown executor, Invoke is disabled", zap.String("value", v))
		return nil
	}
}

// Invoke  POST /2015-03-31/functions/{name}/invocations
func (h *Handler) Invoke(w http.ResponseWriter, r *http.Request, name string) {
	invType := r.Header.Get("X-Amz-Invocation-Type")
	if invType == "" {
		invType = "RequestResponse"
	}
	if invType != "RequestResponse" && invType != "Event" && invType != "DryRun" {
		writeError(w, errValidation("1 validation error detected: Value '"+invType+"' at 'invocationType' failed to satisfy constraint: Member must satisfy enum value set: [Event, RequestResponse, DryRun]"))
		return
	}
	logType := r.Header.Get("X-Amz-Log-Type")
	if logType != "" && logType != "None" && logType != "Tail" {
		writeError(w, errValidation("1 validation error detected: Value '"+logType+"' at 'logType' failed to satisfy constraint: Member must satisfy enum value set: [None, Tail]"))
		return
	}
	limit := maxSyncPayload
	if invType == "Event" {
		limit = maxAsyncPayload
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		writeError(w, errInvalid("could not read the request body"))
		return
	}
	if len(payload) > limit {
		writeError(w, &apiError{http.StatusRequestEntityTooLarge, "RequestTooLargeException",
			fmt.Sprintf("%d byte payload is too large for the %s invocation type (limit %d bytes)", len(payload), invType, limit)})
		return
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = []byte("{}")
	}
	if !json.Valid(payload) {
		writeError(w, &apiError{http.StatusBadRequest, "InvalidRequestContentException", "Could not parse request body into json: Unexpected character"})
		return
	}
	var clientContext []byte
	if cc := r.Header.Get("X-Amz-Client-Context"); cc != "" {
		if clientContext, err = base64.StdEncoding.DecodeString(cc); err != nil {
			writeError(w, &apiError{http.StatusBadRequest, "InvalidRequestContentException", "Client context must be a valid Base64-encoded JSON object."})
			return
		}
	}

	inv, err := h.prepareInvocation(r, name, payload)
	if err != nil {
		writeError(w, err)
		return
	}
	inv.ClientContext = clientContext
	w.Header().Set("X-Amz-Executed-Version", inv.Version)

	if invType == "DryRun" {
		noContent(w)
		return
	}
	if h.Executor == nil {
		writeError(w, &apiError{http.StatusNotImplemented, "ServiceException",
			"Lambda invocation is not available on this server: no executor is configured. " +
				"For development only, set OPENSNACK_LAMBDA_EXECUTOR=local."})
		return
	}

	if invType == "Event" {
		if !h.queueAsync(inv, h.asyncPolicy(r, name)) {
			writeError(w, throttled("ConcurrentInvocationLimitExceeded"))
			return
		}
		w.Header().Set("X-Amzn-RequestId", inv.RequestID)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// The function may run far longer than the server's write timeout.
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(inv.Timeout + 5*time.Minute))
	res, err := h.run(r.Context(), inv)
	if err != nil {
		var te *throttleError
		var ae *apiError
		switch {
		case errors.As(err, &te), errors.As(err, &ae):
			writeError(w, err)
		case errors.Is(err, ErrThrottled):
			writeError(w, throttled("ConcurrentInvocationLimitExceeded"))
		case errors.Is(err, ErrUnsupportedRuntime):
			writeError(w, &apiError{http.StatusBadGateway, "InvalidRuntimeException",
				"The runtime parameter of " + inv.Runtime + " is not supported by this server's executor."})
		default:
			writeError(w, &apiError{http.StatusInternalServerError, "ServiceException", err.Error()})
		}
		return
	}
	if res.FunctionError != "" {
		w.Header().Set("X-Amz-Function-Error", res.FunctionError)
	}
	if logType == "Tail" {
		tail := strings.Join(res.Logs, "")
		if len(tail) > maxTail {
			tail = tail[len(tail)-maxTail:]
		}
		w.Header().Set("X-Amz-Log-Result", base64.StdEncoding.EncodeToString([]byte(tail)))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-RequestId", inv.RequestID)
	w.WriteHeader(http.StatusOK)
	w.Write(res.Payload)
}

// InvokeAsync  POST /2014-11-13/functions/{name}/invoke-async (deprecated)
func (h *Handler) InvokeAsync(w http.ResponseWriter, r *http.Request, name string) {
	r.Header.Set("X-Amz-Invocation-Type", "Event")
	rec := &statusRecorder{ResponseWriter: w}
	h.Invoke(rec, r, name)
	if rec.status == http.StatusAccepted && !rec.wrote {
		writeJSON(w, http.StatusAccepted, map[string]int{"Status": 202})
	}
}

// statusRecorder holds back a bare 202 so InvokeAsync can add its body.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	if code != http.StatusAccepted {
		s.wrote = true
		s.ResponseWriter.WriteHeader(code)
	}
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
		s.ResponseWriter.WriteHeader(s.status)
	}
	return s.ResponseWriter.Write(b)
}

// prepareInvocation resolves the function at its qualifier (an alias with
// weighted routing picks a version) and loads its code.
func (h *Handler) prepareInvocation(r *http.Request, name string, payload []byte) (*Invocation, error) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		return nil, err
	}
	invokedARN := qualifiedARN(r, ref.Name, ref.Qualifier)
	if ref.Qualifier != "" && ref.Qualifier != "$LATEST" && !isVersion(ref.Qualifier) {
		a, err := h.loadAlias(r, ref.Name, ref.Qualifier)
		if err != nil {
			return nil, errFunctionNotFound(invokedARN)
		}
		ref.Qualifier = a.FunctionVersion
		if a.RoutingConfig != nil {
			roll := rand.Float64()
			for v, weight := range a.RoutingConfig.AdditionalVersionWeights {
				if roll < weight {
					ref.Qualifier = v
					break
				}
				roll -= weight
			}
		}
	}
	res, err := h.resolve(r, ref)
	if err != nil {
		return nil, err
	}
	cfg := res.config
	if cfg.State != "Active" {
		return nil, &apiError{http.StatusConflict, "ResourceNotReadyException", "The function is currently in the following state: " + cfg.State}
	}
	if cfg.PackageType == "Image" {
		return nil, &apiError{http.StatusBadGateway, "InvalidRuntimeException", "Container image functions can't be invoked on this server."}
	}
	codeNS, codeBlob := util.GlobalNamespace(r), res.codeBlob
	if !blobName.MatchString(codeBlob) {
		return nil, &apiError{http.StatusInternalServerError, "ServiceException", "The function's code could not be read."}
	}

	requestID := uuid.NewString()
	logGroup := "/aws/lambda/" + ref.Name
	if cfg.LoggingConfig != nil && cfg.LoggingConfig.LogGroup != "" {
		logGroup = cfg.LoggingConfig.LogGroup
	}
	env := map[string]string{}
	if cfg.Environment != nil {
		for k, v := range cfg.Environment.Variables {
			env[k] = v
		}
	}
	arch := "x86_64"
	if len(cfg.Architectures) > 0 {
		arch = cfg.Architectures[0]
	}
	return &Invocation{
		RequestID:    requestID,
		Namespace:    util.IdentityFrom(r).Namespace,
		Region:       util.Region(r),
		AccountID:    util.AccountID(r),
		FunctionName: ref.Name,
		InvokedARN:   invokedARN,
		Version:      cfg.Version,
		Runtime:      cfg.Runtime,
		Handler:      cfg.Handler,
		Architecture: arch,
		MemoryMB:     cfg.MemorySize,
		Timeout:      time.Duration(cfg.Timeout) * time.Second,
		Environment:  env,
		LogGroup:     logGroup,
		LogStream:    newLogStream(cfg.Version),
		Role:         cfg.Role,
		LoadCode: func() ([]byte, error) {
			b, err := ReadCode(codeNS, codeBlob)
			if err != nil {
				return nil, fmt.Errorf("the function's code could not be read: %w", err)
			}
			return b, nil
		},
		ReservedConcurrency: res.fn.Concurrency,
		CodeSha256:          cfg.CodeSha256,
		Payload:             payload,
	}, nil
}

func newLogStream(version string) string {
	return time.Now().UTC().Format("2006/01/02") + "/[" + version + "]" + util.RandomHex(16)
}

// asyncPolicy reads the function's event invoke config for the invoked
// qualifier (falling back to the unqualified one).
func (h *Handler) asyncPolicy(r *http.Request, name string) asyncPolicy {
	p := asyncPolicy{MaxRetries: 2, MaxAge: 6 * time.Hour}
	ref, err := parseFunctionName(r, name)
	if err != nil {
		return p
	}
	var cfg FunctionEventInvokeConfig
	if !h.get(r, typeEventInvoke, policyID(ref), &cfg) {
		ref.Qualifier = ""
		if !h.get(r, typeEventInvoke, policyID(ref), &cfg) {
			return p
		}
	}
	if cfg.MaximumRetryAttempts != nil {
		p.MaxRetries = *cfg.MaximumRetryAttempts
	}
	if cfg.MaximumEventAgeInSeconds != nil {
		p.MaxAge = time.Duration(*cfg.MaximumEventAgeInSeconds) * time.Second
	}
	return p
}

// queueAsync starts an Event invocation, unless its namespace already has
// as many waiting as it may.
func (h *Handler) queueAsync(inv *Invocation, policy asyncPolicy) bool {
	h.asyncMu.Lock()
	if h.asyncQueued[inv.Namespace] >= h.asyncLimit {
		h.asyncMu.Unlock()
		return false
	}
	h.asyncQueued[inv.Namespace]++
	h.asyncMu.Unlock()
	h.invokes.Add(1)
	go func() {
		defer h.invokes.Done()
		defer func() {
			h.asyncMu.Lock()
			if h.asyncQueued[inv.Namespace]--; h.asyncQueued[inv.Namespace] <= 0 {
				delete(h.asyncQueued, inv.Namespace)
			}
			h.asyncMu.Unlock()
		}()
		h.runAsync(inv, policy, h.stop)
	}()
	return true
}

// run executes the invocation and ships its logs, framed by the START, END
// and REPORT lines Lambda writes. It admits the invocation against the
// concurrency caps first.
func (h *Handler) run(ctx context.Context, inv *Invocation) (*InvocationResult, error) {
	release, te := h.concurrency.acquire(inv)
	if te != nil {
		return nil, te
	}
	defer release()
	start := time.Now()
	res, err := h.Executor.Invoke(ctx, inv)
	if err != nil {
		return nil, err
	}
	if res.LogStream != "" {
		inv.LogStream = res.LogStream
	}
	if len(res.Payload) > maxSyncPayload {
		res.FunctionError = "Unhandled"
		res.Payload, _ = json.Marshal(map[string]string{
			"errorType":    "Function.ResponseSizeTooLarge",
			"errorMessage": fmt.Sprintf("Response payload size exceeded maximum allowed payload size (%d bytes).", maxSyncPayload),
		})
	}
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	ms := float64(res.Duration.Microseconds()) / 1000
	lines := []string{fmt.Sprintf("START RequestId: %s Version: %s\n", inv.RequestID, inv.Version)}
	for _, l := range res.Logs {
		if !strings.HasSuffix(l, "\n") {
			l += "\n"
		}
		lines = append(lines, l)
	}
	lines = append(lines,
		fmt.Sprintf("END RequestId: %s\n", inv.RequestID),
		fmt.Sprintf("REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB\tMax Memory Used: %d MB\t\n",
			inv.RequestID, ms, int(ms)+1, inv.MemoryMB, res.MaxMemoryMB))
	res.Logs = lines

	if h.Logs != nil {
		now := time.Now()
		events := make([]LogEvent, len(lines))
		for i, l := range lines {
			events[i] = LogEvent{Timestamp: now, Message: l}
		}
		scope := util.RegionalScope(inv.Namespace, inv.Region)
		if err := h.Logs.PutLogEvents(context.WithoutCancel(ctx), scope, inv.AccountID, inv.LogGroup, inv.LogStream, events); err != nil {
			zap.L().Warn("lambda log delivery failed", zap.String("group", inv.LogGroup), zap.Error(err))
		}
	}
	return res, nil
}
