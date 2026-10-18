// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package lambda is the Lambda control plane (functions, versions, aliases,
// policies, tags, concurrency, URLs, event source mappings, event invoke
// configs) on the REST-JSON API, plus Invoke through a pluggable Executor.
package lambda

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const service = "lambda"

// Store types.
const (
	typeFunction    = "function"
	typeVersion     = "version"
	typeAlias       = "alias"
	typePolicy      = "policy"
	typeURL         = "url"
	typeEventInvoke = "event-invoke-config"
	typeESM         = "event-source-mapping"
)

type Handler struct {
	Store resource.Store
	// Executor runs Invoke. Nil means invocation is not available (see
	// NewHandler for OPENSNACK_LAMBDA_EXECUTOR).
	Executor Executor
	// Logs receives each invocation's log lines. Nil drops them.
	Logs LogSink
	// AsyncTiming paces retries of Event invocations.
	AsyncTiming AsyncTiming

	// mu serialises read-modify-write of a function and the rows hanging off
	// it, so the provider's parallel calls (alias and permission at once,
	// say) can't lose each other's updates.
	mu sync.Mutex
	// invokes tracks Event invocations still running, so tests and shutdown
	// can wait for them.
	invokes sync.WaitGroup
	// concurrency caps invocations in flight; asyncQueued counts Event
	// invocations waiting, by namespace.
	concurrency *concurrency
	asyncMu     sync.Mutex
	asyncQueued map[string]int
	asyncLimit  int
	stop        chan struct{}
	stopOnce    sync.Once
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{
		Store:       store,
		Executor:    executorFromEnv(),
		AsyncTiming: defaultAsyncTiming,
		concurrency: newConcurrency(concurrencyFromEnv()),
		asyncQueued: map[string]int{},
		asyncLimit:  asyncQueueFromEnv(),
		stop:        make(chan struct{}),
	}
}

// Start starts the executor's background work (KubernetesExecutor's
// Runtime API listener, pod reaper and orphan collection), if it has any.
func (h *Handler) Start() error {
	if s, ok := h.Executor.(interface{ Start() error }); ok {
		return s.Start()
	}
	return nil
}

// Stop abandons queued Event invocations and stops the executor.
func (h *Handler) Stop() {
	h.stopOnce.Do(func() { close(h.stop) })
	if s, ok := h.Executor.(interface{ Stop() }); ok {
		s.Stop()
	}
}

// SetCredentials gives the executor a way to mint credentials for each
// function's execution role.
func (h *Handler) SetCredentials(c CredentialIssuer) {
	if s, ok := h.Executor.(interface{ SetCredentialIssuer(CredentialIssuer) }); ok {
		s.SetCredentialIssuer(c)
	}
}

// retirer is an executor that keeps sandboxes warm and must drop them when
// the function changes or goes away.
type retirer interface {
	RetireFunction(namespace, region, function string)
	RetireNamespace(namespace string)
}

func (h *Handler) retire(r *http.Request, function string) {
	if rt, ok := h.Executor.(retirer); ok {
		rt.RetireFunction(util.GlobalNamespace(r), util.Region(r), function)
	}
}

// RetireNamespace stops every sandbox a namespace has. It is a
// NamespaceCleaner for the inspection API.
func (h *Handler) RetireNamespace(namespace string) error {
	if rt, ok := h.Executor.(retirer); ok {
		rt.RetireNamespace(namespace)
	}
	return nil
}

// WaitAsync blocks until every Event (async) invocation started so far has
// finished.
func (h *Handler) WaitAsync() { h.invokes.Wait() }

//
// Errors (REST-JSON: status, X-Amzn-ErrorType, {"Type","Message"}). The
// message goes under both spellings: Lambda's error shapes differ (most
// have "Message", TooManyRequestsException has "message"), and older SDKs
// read only their own.
//

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errNotFound(msg string) *apiError {
	return &apiError{http.StatusNotFound, "ResourceNotFoundException", msg}
}

func errConflict(msg string) *apiError {
	return &apiError{http.StatusConflict, "ResourceConflictException", msg}
}

func errInvalid(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "InvalidParameterValueException", msg}
}

func errValidation(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "ValidationException", msg}
}

func errPrecondition(msg string) *apiError {
	return &apiError{http.StatusPreconditionFailed, "PreconditionFailedException", msg}
}

func errFunctionNotFound(arn string) *apiError {
	return errNotFound("Function not found: " + arn)
}

func writeError(w http.ResponseWriter, err error) {
	var te *throttleError
	if errors.As(err, &te) {
		writeThrottle(w, te)
		return
	}
	var e *apiError
	if !errors.As(err, &e) {
		e = &apiError{http.StatusInternalServerError, "ServiceException", err.Error()}
	}
	w.Header().Set("X-Amzn-ErrorType", e.Code)
	writeJSON(w, e.Status, map[string]string{"Type": "User", "Message": e.Message, "message": e.Message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-RequestId", awsresponses.NextRequestID())
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

func noContent(w http.ResponseWriter) {
	w.Header().Set("X-Amzn-RequestId", awsresponses.NextRequestID())
	w.WriteHeader(http.StatusNoContent)
}

// maxBody bounds a request body: a 50 MB zip, base64-encoded, plus JSON.
const maxBody = 70 << 20

func decodeBody(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return errInvalid("could not read the request body: " + err.Error())
	}
	if len(data) > maxBody {
		return &apiError{http.StatusRequestEntityTooLarge, "RequestTooLargeException", "Request must be smaller than 70167211 bytes for the CreateFunction operation"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errInvalid("could not parse request body into json: " + err.Error())
	}
	return nil
}

//
// Routing
//

var apiVersion = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ServeREST routes the Lambda REST API. Paths are /{api-version}/..., where
// the version is the date the operation was added (2015-03-31 for most,
// 2017-03-31 for tags, 2021-10-31 for URLs...). The legacy "/lambda" prefix
// of the path-routed endpoint is accepted too.
func (h *Handler) ServeREST(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if p, ok := strings.CutPrefix(path, "/lambda"); ok && (p == "" || p[0] == '/') {
		path = p
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 || !apiVersion.MatchString(segs[0]) {
		unknownOperation(w, r)
		return
	}
	m := r.Method
	s := segs[1:]

	switch s[0] {
	case "functions":
		if len(s) == 1 {
			switch m {
			case http.MethodGet:
				h.ListFunctions(w, r)
			case http.MethodPost:
				h.CreateFunction(w, r)
			default:
				unknownOperation(w, r)
			}
			return
		}
		h.serveFunction(w, r, s[1], s[2:])
	case "event-source-mappings":
		switch {
		case len(s) == 1 && m == http.MethodGet:
			h.ListEventSourceMappings(w, r)
		case len(s) == 1 && m == http.MethodPost:
			h.CreateEventSourceMapping(w, r)
		case len(s) == 2 && m == http.MethodGet:
			h.GetEventSourceMapping(w, r, s[1])
		case len(s) == 2 && m == http.MethodPut:
			h.UpdateEventSourceMapping(w, r, s[1])
		case len(s) == 2 && m == http.MethodDelete:
			h.DeleteEventSourceMapping(w, r, s[1])
		default:
			unknownOperation(w, r)
		}
	case "tags":
		arn := strings.Join(s[1:], "/")
		switch m {
		case http.MethodGet:
			h.ListTags(w, r, arn)
		case http.MethodPost:
			h.TagResource(w, r, arn)
		case http.MethodDelete:
			h.UntagResource(w, r, arn)
		default:
			unknownOperation(w, r)
		}
	case "account-settings":
		if m != http.MethodGet {
			unknownOperation(w, r)
			return
		}
		h.GetAccountSettings(w, r)
	default:
		unknownOperation(w, r)
	}
}

// serveFunction routes /{version}/functions/{name}/{sub...}.
func (h *Handler) serveFunction(w http.ResponseWriter, r *http.Request, name string, sub []string) {
	m := r.Method
	op := ""
	if len(sub) > 0 {
		op = sub[0]
	}
	switch {
	case op == "" && m == http.MethodGet:
		h.GetFunction(w, r, name)
	case op == "" && m == http.MethodDelete:
		h.DeleteFunction(w, r, name)
	case op == "configuration" && m == http.MethodGet:
		h.GetFunctionConfiguration(w, r, name)
	case op == "configuration" && m == http.MethodPut:
		h.UpdateFunctionConfiguration(w, r, name)
	case op == "code" && m == http.MethodPut:
		h.UpdateFunctionCode(w, r, name)
	case op == "versions" && m == http.MethodGet:
		h.ListVersionsByFunction(w, r, name)
	case op == "versions" && m == http.MethodPost:
		h.PublishVersion(w, r, name)
	case op == "aliases" && len(sub) == 1 && m == http.MethodGet:
		h.ListAliases(w, r, name)
	case op == "aliases" && len(sub) == 1 && m == http.MethodPost:
		h.CreateAlias(w, r, name)
	case op == "aliases" && len(sub) == 2 && m == http.MethodGet:
		h.GetAlias(w, r, name, sub[1])
	case op == "aliases" && len(sub) == 2 && m == http.MethodPut:
		h.UpdateAlias(w, r, name, sub[1])
	case op == "aliases" && len(sub) == 2 && m == http.MethodDelete:
		h.DeleteAlias(w, r, name, sub[1])
	case op == "policy" && len(sub) == 1 && m == http.MethodGet:
		h.GetPolicy(w, r, name)
	case op == "policy" && len(sub) == 1 && m == http.MethodPost:
		h.AddPermission(w, r, name)
	case op == "policy" && len(sub) == 2 && m == http.MethodDelete:
		h.RemovePermission(w, r, name, sub[1])
	case op == "invocations" && m == http.MethodPost:
		h.Invoke(w, r, name)
	case op == "invoke-async" && m == http.MethodPost:
		h.InvokeAsync(w, r, name)
	case op == "concurrency" && m == http.MethodGet:
		h.GetFunctionConcurrency(w, r, name)
	case op == "concurrency" && m == http.MethodPut:
		h.PutFunctionConcurrency(w, r, name)
	case op == "concurrency" && m == http.MethodDelete:
		h.DeleteFunctionConcurrency(w, r, name)
	case op == "event-invoke-config" && len(sub) == 2 && sub[1] == "list" && m == http.MethodGet:
		h.ListFunctionEventInvokeConfigs(w, r, name)
	case op == "event-invoke-config" && len(sub) == 1:
		switch m {
		case http.MethodGet:
			h.GetFunctionEventInvokeConfig(w, r, name)
		case http.MethodPut:
			h.PutFunctionEventInvokeConfig(w, r, name, false)
		case http.MethodPost:
			h.PutFunctionEventInvokeConfig(w, r, name, true)
		case http.MethodDelete:
			h.DeleteFunctionEventInvokeConfig(w, r, name)
		default:
			unknownOperation(w, r)
		}
	case op == "code-signing-config":
		switch m {
		case http.MethodGet:
			h.GetFunctionCodeSigningConfig(w, r, name)
		case http.MethodPut:
			h.PutFunctionCodeSigningConfig(w, r, name)
		case http.MethodDelete:
			h.DeleteFunctionCodeSigningConfig(w, r, name)
		default:
			unknownOperation(w, r)
		}
	case op == "recursion-config" && m == http.MethodGet:
		h.GetFunctionRecursionConfig(w, r, name)
	case op == "recursion-config" && m == http.MethodPut:
		h.PutFunctionRecursionConfig(w, r, name)
	case op == "runtime-management-config" && m == http.MethodGet:
		h.GetRuntimeManagementConfig(w, r, name)
	case op == "url" && len(sub) == 1:
		switch m {
		case http.MethodGet:
			h.GetFunctionUrlConfig(w, r, name)
		case http.MethodPost:
			h.CreateFunctionUrlConfig(w, r, name)
		case http.MethodPut:
			h.UpdateFunctionUrlConfig(w, r, name)
		case http.MethodDelete:
			h.DeleteFunctionUrlConfig(w, r, name)
		default:
			unknownOperation(w, r)
		}
	case op == "urls" && m == http.MethodGet:
		h.ListFunctionUrlConfigs(w, r, name)
	default:
		unknownOperation(w, r)
	}
}

func unknownOperation(w http.ResponseWriter, r *http.Request) {
	writeError(w, &apiError{http.StatusNotFound, "UnknownOperationException",
		"Unknown operation " + r.Method + " " + r.URL.Path})
}

//
// Function names
//

// fnRef is a function name from a path or body, resolved against the caller.
type fnRef struct {
	Name      string
	Qualifier string
}

// functionNamePattern accepts the forms AWS does: a name, a partial ARN
// (123456789012:function:name) or a full ARN, each optionally ending in
// ":qualifier" (a version, $LATEST or an alias name).
var functionNamePattern = regexp.MustCompile(
	`^(?:arn:aws[a-zA-Z-]*:lambda:([a-z0-9-]+):(\d{12}):function:|(\d{12}):function:)?([a-zA-Z0-9_-]{1,140})(?::(\$LATEST|[a-zA-Z0-9_-]{1,128}))?$`)

// parseFunctionName resolves raw (a path segment or a FunctionName member)
// and merges it with the Qualifier query parameter. An ARN for another
// account or region names a function this caller can't see.
func parseFunctionName(r *http.Request, raw string) (fnRef, error) {
	m := functionNamePattern.FindStringSubmatch(raw)
	if m == nil {
		return fnRef{}, errValidation("1 validation error detected: Value '" + raw +
			"' at 'functionName' failed to satisfy constraint: Member must satisfy regular expression pattern: " +
			`(arn:(aws[a-zA-Z-]*)?:lambda:)?((eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:)?(\d{12}:)?(function:)?([a-zA-Z0-9-_\.]+)(:(\$LATEST|[a-zA-Z0-9-_]+))?`)
	}
	region, account := m[1], m[2]
	if account == "" {
		account = m[3]
	}
	if (region != "" && region != util.Region(r)) || (account != "" && account != util.AccountID(r)) {
		return fnRef{}, errFunctionNotFound(raw)
	}
	ref := fnRef{Name: m[4], Qualifier: m[5]}
	if q := r.URL.Query().Get("Qualifier"); q != "" {
		if ref.Qualifier != "" && ref.Qualifier != q {
			return fnRef{}, errInvalid("The derived qualifier from the function name does not match the specified qualifier.")
		}
		ref.Qualifier = q
	}
	return ref, nil
}

// requireUnqualified is for operations on the function as a whole.
func (ref fnRef) requireUnqualified() error {
	if ref.Qualifier != "" {
		return errInvalid("This operation doesn't support a qualified function name or the Qualifier parameter.")
	}
	return nil
}

func functionARN(r *http.Request, name string) string {
	return "arn:aws:lambda:" + util.Region(r) + ":" + util.AccountID(r) + ":function:" + name
}

func qualifiedARN(r *http.Request, name, qualifier string) string {
	if qualifier == "" {
		return functionARN(r, name)
	}
	return functionARN(r, name) + ":" + qualifier
}

func isVersion(q string) bool {
	if q == "" {
		return false
	}
	for _, c := range q {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

//
// Store helpers
//

func ns(r *http.Request) string { return util.NamespaceFromHeader(r) }

func (h *Handler) get(r *http.Request, typ, id string, v any) bool {
	res, err := h.Store.Get(id, service, typ, ns(r))
	if err != nil || res == nil {
		return false
	}
	return json.Unmarshal(res.Attributes, v) == nil
}

func (h *Handler) put(r *http.Request, typ, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: id, Namespace: ns(r), Service: service, Type: typ, Attributes: b}
	if _, err := h.Store.Get(id, service, typ, ns(r)); err == nil {
		return h.Store.Update(res)
	}
	return h.Store.Create(res)
}

func (h *Handler) del(r *http.Request, typ, id string) {
	_ = h.Store.Delete(id, service, typ, ns(r))
}

// listPrefixed returns the rows of a type whose ID is prefix + ":" + ...
// (a function's versions, aliases, per-qualifier policies...).
func (h *Handler) listPrefixed(r *http.Request, typ, name string) []resource.Resource {
	rows, _ := h.Store.List(service, typ, ns(r))
	var out []resource.Resource
	for _, row := range rows {
		if row.ID == name || strings.HasPrefix(row.ID, name+":") {
			out = append(out, row)
		}
	}
	return out
}

func (h *Handler) loadFunction(r *http.Request, name string) (*functionRecord, error) {
	var fn functionRecord
	if !h.get(r, typeFunction, name, &fn) {
		return nil, errFunctionNotFound(functionARN(r, name))
	}
	return &fn, nil
}

func (h *Handler) loadVersion(r *http.Request, name, version string) (*versionRecord, error) {
	var v versionRecord
	if !h.get(r, typeVersion, name+":"+version, &v) {
		return nil, errFunctionNotFound(qualifiedARN(r, name, version))
	}
	return &v, nil
}

func (h *Handler) loadAlias(r *http.Request, name, alias string) (*AliasConfiguration, error) {
	var a AliasConfiguration
	if !h.get(r, typeAlias, name+":"+alias, &a) {
		return nil, errNotFound("Alias not found: " + qualifiedARN(r, name, alias))
	}
	return &a, nil
}

// checkQualifier fails unless q is "", $LATEST, a published version or an
// alias of the function.
func (h *Handler) checkQualifier(r *http.Request, name, q string) error {
	switch {
	case q == "" || q == "$LATEST":
		return nil
	case isVersion(q):
		_, err := h.loadVersion(r, name, q)
		return err
	default:
		if _, err := h.loadAlias(r, name, q); err != nil {
			return errFunctionNotFound(qualifiedARN(r, name, q))
		}
		return nil
	}
}

// resolved is a function at a qualifier: the configuration to report and
// the code it runs.
type resolved struct {
	fn       *functionRecord
	config   FunctionConfiguration
	codeBlob string
}

// resolve loads the function and the configuration its qualifier names.
// An alias resolves to its version, reported under the alias ARN.
func (h *Handler) resolve(r *http.Request, ref fnRef) (*resolved, error) {
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		return nil, err
	}
	out := &resolved{fn: fn, config: fn.Config, codeBlob: fn.CodeBlob}
	q := ref.Qualifier
	switch {
	case q == "":
	case q == "$LATEST":
		out.config.FunctionArn = qualifiedARN(r, ref.Name, "$LATEST")
	case isVersion(q):
		v, err := h.loadVersion(r, ref.Name, q)
		if err != nil {
			return nil, err
		}
		out.config, out.codeBlob = v.Config, v.CodeBlob
	default:
		a, err := h.loadAlias(r, ref.Name, q)
		if err != nil {
			return nil, errFunctionNotFound(qualifiedARN(r, ref.Name, q))
		}
		if a.FunctionVersion == "$LATEST" {
			out.config.Version = "$LATEST"
		} else {
			v, err := h.loadVersion(r, ref.Name, a.FunctionVersion)
			if err != nil {
				return nil, err
			}
			out.config, out.codeBlob = v.Config, v.CodeBlob
		}
		out.config.FunctionArn = qualifiedARN(r, ref.Name, q)
	}
	return out, nil
}

//
// Small shared bits
//

func lastModifiedNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000-0700")
}

func epochNow() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

func newRevisionID() string {
	b := util.RandomHex(16)
	return b[0:8] + "-" + b[8:12] + "-" + b[12:16] + "-" + b[16:20] + "-" + b[20:32]
}

// page applies Marker/MaxItems pagination to n items, returning the slice
// bounds and the next marker ("" at the end).
func page(r *http.Request, n int) (int, int, string) {
	start, _ := strconv.Atoi(r.URL.Query().Get("Marker"))
	if start < 0 || start > n {
		start = n
	}
	max, _ := strconv.Atoi(r.URL.Query().Get("MaxItems"))
	if max <= 0 || max > 10000 {
		max = 50
	}
	end := min(start+max, n)
	next := ""
	if end < n {
		next = strconv.Itoa(end)
	}
	return start, end, next
}

func (h *Handler) GetAccountSettings(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.Store.List(service, typeFunction, ns(r))
	var codeSize int64
	for _, row := range rows {
		var fn functionRecord
		if json.Unmarshal(row.Attributes, &fn) == nil {
			codeSize += fn.Config.CodeSize
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"AccountLimit": map[string]any{
			"TotalCodeSize":                  80530636800,
			"CodeSizeUnzipped":               262144000,
			"CodeSizeZipped":                 52428800,
			"ConcurrentExecutions":           1000,
			"UnreservedConcurrentExecutions": 1000,
		},
		"AccountUsage": map[string]any{
			"TotalCodeSize": codeSize,
			"FunctionCount": len(rows),
		},
	})
}
