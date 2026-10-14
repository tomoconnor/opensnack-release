// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// configInput is the part of CreateFunction and UpdateFunctionConfiguration
// that describes the function. Nil means "not given": Create fills the AWS
// default, Update leaves the field alone.
type configInput struct {
	Role              *string
	Handler           *string
	Description       *string
	Timeout           *int
	MemorySize        *int
	VpcConfig         *VpcConfig
	Environment       *Environment
	Runtime           *string
	DeadLetterConfig  *DeadLetterConfig
	KMSKeyArn         *string
	TracingConfig     *TracingConfig
	Layers            *[]string
	FileSystemConfigs *[]FileSystemConfig
	ImageConfig       *ImageConfig
	EphemeralStorage  *EphemeralStorage
	SnapStart         *SnapStart
	LoggingConfig     *LoggingConfig
}

type createFunctionInput struct {
	configInput
	FunctionName         string
	Code                 *FunctionCode
	Publish              bool
	PackageType          string
	Tags                 map[string]string
	CodeSigningConfigArn string
	Architectures        []string
}

var (
	rolePattern  = regexp.MustCompile(`^arn:aws[a-zA-Z-]*:iam::\d{12}:role/?[a-zA-Z_0-9+=,.@\-_/]+$`)
	layerPattern = regexp.MustCompile(`^arn:aws[a-zA-Z-]*:lambda:[a-z0-9-]+:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+$`)
)

// reservedEnvKeys are the variables Lambda sets itself and refuses to take.
var reservedEnvKeys = []string{
	"_HANDLER", "_X_AMZN_TRACE_ID", "AWS_DEFAULT_REGION", "AWS_REGION", "AWS_EXECUTION_ENV",
	"AWS_LAMBDA_FUNCTION_NAME", "AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "AWS_LAMBDA_FUNCTION_VERSION",
	"AWS_LAMBDA_INITIALIZATION_TYPE", "AWS_LAMBDA_LOG_GROUP_NAME", "AWS_LAMBDA_LOG_STREAM_NAME",
	"AWS_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_LAMBDA_RUNTIME_API", "LAMBDA_TASK_ROOT", "LAMBDA_RUNTIME_DIR",
}

// applyConfig validates in and writes the given fields into cfg, normalising
// them the way Lambda reports them back.
func applyConfig(cfg *FunctionConfiguration, in configInput) error {
	if in.Role != nil {
		if !rolePattern.MatchString(*in.Role) {
			return errValidation("1 validation error detected: Value '" + *in.Role +
				"' at 'role' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z-]*)?:iam::\\d{12}:role/?[a-zA-Z_0-9+=,.@\\-_/]+")
		}
		cfg.Role = *in.Role
	}
	if in.Handler != nil {
		cfg.Handler = *in.Handler
	}
	if in.Runtime != nil {
		cfg.Runtime = *in.Runtime
		cfg.RuntimeVersionConfig = runtimeVersion(cfg.FunctionArn, cfg.Runtime)
	}
	if in.Description != nil {
		if len(*in.Description) > 256 {
			return errValidation("1 validation error detected: Value at 'description' failed to satisfy constraint: Member must have length less than or equal to 256")
		}
		cfg.Description = *in.Description
	}
	if in.Timeout != nil {
		if *in.Timeout < 1 || *in.Timeout > 900 {
			return errValidation(fmt.Sprintf("1 validation error detected: Value '%d' at 'timeout' failed to satisfy constraint: Member must have value less than or equal to 900", *in.Timeout))
		}
		cfg.Timeout = *in.Timeout
	}
	if in.MemorySize != nil {
		if *in.MemorySize < 128 || *in.MemorySize > 10240 {
			return errValidation(fmt.Sprintf("1 validation error detected: Value '%d' at 'memorySize' failed to satisfy constraint: Member must have value between 128 and 10240", *in.MemorySize))
		}
		cfg.MemorySize = *in.MemorySize
	}
	if in.VpcConfig != nil {
		v := in.VpcConfig
		if len(v.SubnetIds) == 0 && len(v.SecurityGroupIds) == 0 {
			cfg.VpcConfig = nil
		} else {
			cfg.VpcConfig = &VpcConfig{
				SubnetIds:               sortedCopy(v.SubnetIds),
				SecurityGroupIds:        sortedCopy(v.SecurityGroupIds),
				Ipv6AllowedForDualStack: v.Ipv6AllowedForDualStack,
			}
		}
	}
	if in.Environment != nil {
		var reserved []string
		for _, k := range reservedEnvKeys {
			if _, ok := in.Environment.Variables[k]; ok {
				reserved = append(reserved, k)
			}
		}
		if len(reserved) > 0 {
			return errInvalid("Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: " + strings.Join(reserved, ", "))
		}
		if len(in.Environment.Variables) == 0 {
			cfg.Environment = nil
		} else {
			cfg.Environment = &Environment{Variables: in.Environment.Variables}
		}
	}
	if in.DeadLetterConfig != nil {
		if in.DeadLetterConfig.TargetArn == "" {
			cfg.DeadLetterConfig = nil
		} else {
			cfg.DeadLetterConfig = &DeadLetterConfig{TargetArn: in.DeadLetterConfig.TargetArn}
		}
	}
	if in.KMSKeyArn != nil {
		cfg.KMSKeyArn = *in.KMSKeyArn
	}
	if in.TracingConfig != nil {
		mode := in.TracingConfig.Mode
		if mode == "" {
			mode = "PassThrough"
		}
		if mode != "Active" && mode != "PassThrough" {
			return errValidation("1 validation error detected: Value '" + mode + "' at 'tracingConfig.mode' failed to satisfy constraint: Member must satisfy enum value set: [Active, PassThrough]")
		}
		cfg.TracingConfig = &TracingConfig{Mode: mode}
	}
	if in.Layers != nil {
		if len(*in.Layers) > 5 {
			return errInvalid("Cannot reference more than 5 layers.")
		}
		cfg.Layers = nil
		for _, arn := range *in.Layers {
			if !layerPattern.MatchString(arn) {
				return errValidation("1 validation error detected: Value '[" + arn + "]' at 'layers' failed to satisfy constraint: Member must satisfy constraint: [Member must satisfy regular expression pattern: (arn:[a-zA-Z0-9-]+:lambda:[a-zA-Z0-9-]+:\\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)]")
			}
			cfg.Layers = append(cfg.Layers, Layer{Arn: arn})
		}
	}
	if in.FileSystemConfigs != nil {
		cfg.FileSystemConfigs = *in.FileSystemConfigs
	}
	if in.ImageConfig != nil {
		cfg.ImageConfigResponse = &ImageConfigResponse{ImageConfig: in.ImageConfig}
	}
	if in.EphemeralStorage != nil {
		size := in.EphemeralStorage.Size
		if size < 512 || size > 10240 {
			return errValidation(fmt.Sprintf("1 validation error detected: Value '%d' at 'ephemeralStorage.size' failed to satisfy constraint: Member must have value between 512 and 10240", size))
		}
		cfg.EphemeralStorage = &EphemeralStorage{Size: size}
	}
	if in.SnapStart != nil {
		apply := in.SnapStart.ApplyOn
		if apply == "" {
			apply = "None"
		}
		cfg.SnapStart = &SnapStartResponse{ApplyOn: apply, OptimizationStatus: "Off"}
	}
	if in.LoggingConfig != nil {
		lc := *in.LoggingConfig
		if lc.LogFormat == "" {
			lc.LogFormat = "Text"
		}
		if lc.LogGroup == "" {
			lc.LogGroup = "/aws/lambda/" + cfg.FunctionName
		}
		if lc.LogFormat == "JSON" {
			if lc.ApplicationLogLevel == "" {
				lc.ApplicationLogLevel = "INFO"
			}
			if lc.SystemLogLevel == "" {
				lc.SystemLogLevel = "INFO"
			}
		}
		cfg.LoggingConfig = &lc
	}
	return nil
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func runtimeVersion(functionARN, runtime string) *RuntimeVersionConfig {
	if runtime == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(runtime))
	region := "us-east-1"
	if parts := strings.Split(functionARN, ":"); len(parts) > 3 {
		region = parts[3]
	}
	return &RuntimeVersionConfig{
		RuntimeVersionArn: "arn:aws:lambda:" + region + "::runtime:" + hex.EncodeToString(sum[:]),
	}
}

//
// CreateFunction  POST /2015-03-31/functions
//

func (h *Handler) CreateFunction(w http.ResponseWriter, r *http.Request) {
	var in createFunctionInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, in.FunctionName)
	if err != nil {
		writeError(w, err)
		return
	}
	if ref.Qualifier != "" {
		writeError(w, errInvalid("Unsupported function name: a function name can't include a qualifier."))
		return
	}
	if in.Role == nil {
		writeError(w, errValidation("1 validation error detected: Value null at 'role' failed to satisfy constraint: Member must not be null"))
		return
	}
	if in.Code == nil {
		writeError(w, errValidation("1 validation error detected: Value null at 'code' failed to satisfy constraint: Member must not be null"))
		return
	}
	pkg := in.PackageType
	if pkg == "" {
		pkg = "Zip"
	}
	if pkg != "Zip" && pkg != "Image" {
		writeError(w, errValidation("1 validation error detected: Value '"+pkg+"' at 'packageType' failed to satisfy constraint: Member must satisfy enum value set: [Image, Zip]"))
		return
	}
	if pkg == "Zip" && (in.Runtime == nil || *in.Runtime == "" || in.Handler == nil || *in.Handler == "") {
		writeError(w, errInvalid("Runtime and Handler are mandatory parameters for functions created with deployment packages."))
		return
	}
	archs, err := checkArchitectures(in.Architectures)
	if err != nil {
		writeError(w, err)
		return
	}

	cfg := FunctionConfiguration{
		FunctionName:     ref.Name,
		FunctionArn:      functionARN(r, ref.Name),
		Timeout:          3,
		MemorySize:       128,
		Version:          "$LATEST",
		TracingConfig:    &TracingConfig{Mode: "PassThrough"},
		State:            "Active",
		LastUpdateStatus: "Successful",
		PackageType:      pkg,
		Architectures:    archs,
		EphemeralStorage: &EphemeralStorage{Size: 512},
		SnapStart:        &SnapStartResponse{ApplyOn: "None", OptimizationStatus: "Off"},
		LoggingConfig:    &LoggingConfig{LogFormat: "Text", LogGroup: "/aws/lambda/" + ref.Name},
	}
	if err := applyConfig(&cfg, in.configInput); err != nil {
		writeError(w, err)
		return
	}
	if err := checkTags(in.Tags); err != nil {
		writeError(w, err)
		return
	}

	zip, err := h.fetchCode(r, in.Code, pkg)
	if err != nil {
		writeError(w, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.loadFunction(r, ref.Name); err == nil {
		writeError(w, errConflict("Function already exist: "+ref.Name))
		return
	}
	fn := &functionRecord{Config: cfg, Tags: in.Tags, CodeSigningConfigArn: in.CodeSigningConfigArn}
	if err := h.setCode(r, fn, zip, in.Code); err != nil {
		writeError(w, err)
		return
	}
	fn.Config.LastModified = lastModifiedNow()
	fn.Config.RevisionId = newRevisionID()

	out := fn.Config
	if in.Publish {
		v := h.publish(r, fn, "")
		out = v.Config
	}
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		removeCode(r, fn.CodeBlob)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func checkArchitectures(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{"x86_64"}, nil
	}
	if len(in) > 1 || (in[0] != "x86_64" && in[0] != "arm64") {
		return nil, errValidation("1 validation error detected: Value '[" + strings.Join(in, ", ") +
			"]' at 'architectures' failed to satisfy constraint: Member must have length less than or equal to 1, Member must satisfy enum value set: [x86_64, arm64]")
	}
	return in, nil
}

//
// GetFunction  GET /2015-03-31/functions/{name}
//

func (h *Handler) GetFunction(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := h.resolve(r, ref)
	if err != nil {
		writeError(w, err)
		return
	}
	out := map[string]any{
		"Configuration": res.config,
		"Code":          h.codeLocation(r, res),
	}
	if len(res.fn.Tags) > 0 {
		out["Tags"] = res.fn.Tags
	}
	if ref.Qualifier == "" && res.fn.Concurrency != nil {
		out["Concurrency"] = map[string]int{"ReservedConcurrentExecutions": *res.fn.Concurrency}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) codeLocation(r *http.Request, res *resolved) FunctionCodeLocation {
	if res.config.PackageType == "Image" {
		return FunctionCodeLocation{RepositoryType: "ECR", ImageUri: res.fn.ImageUri}
	}
	return FunctionCodeLocation{RepositoryType: "S3", Location: codeDownloadURL(r, res.codeBlob)}
}

//
// GetFunctionConfiguration  GET /2015-03-31/functions/{name}/configuration
//

func (h *Handler) GetFunctionConfiguration(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := h.resolve(r, ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res.config)
}

//
// UpdateFunctionConfiguration  PUT /2015-03-31/functions/{name}/configuration
//

func (h *Handler) UpdateFunctionConfiguration(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		configInput
		RevisionId string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err != nil {
		writeError(w, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if in.RevisionId != "" && in.RevisionId != fn.Config.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id"))
		return
	}
	cfg := fn.Config
	if err := applyConfig(&cfg, in.configInput); err != nil {
		writeError(w, err)
		return
	}
	if cfg.PackageType == "Zip" && (cfg.Runtime == "" || cfg.Handler == "") {
		writeError(w, errInvalid("Runtime and Handler are mandatory parameters for functions created with deployment packages."))
		return
	}
	cfg.LastModified = lastModifiedNow()
	cfg.RevisionId = newRevisionID()
	cfg.LastUpdateStatus = "Successful"
	fn.Config = cfg
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fn.Config)
}

//
// UpdateFunctionCode  PUT /2015-03-31/functions/{name}/code
//

func (h *Handler) UpdateFunctionCode(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		FunctionCode
		Publish       bool
		DryRun        bool
		RevisionId    string
		Architectures []string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	var archs []string
	if in.Architectures != nil {
		if archs, err = checkArchitectures(in.Architectures); err != nil {
			writeError(w, err)
			return
		}
	}

	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	zip, err := h.fetchCode(r, &in.FunctionCode, fn.Config.PackageType)
	if err != nil {
		writeError(w, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if fn, err = h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	if in.RevisionId != "" && in.RevisionId != fn.Config.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id"))
		return
	}
	if in.DryRun {
		cfg := fn.Config
		if zip != nil {
			cfg.CodeSha256, cfg.CodeSize = codeSha256(zip), int64(len(zip))
		}
		writeJSON(w, http.StatusOK, cfg)
		return
	}
	oldBlob := fn.CodeBlob
	if err := h.setCode(r, fn, zip, &in.FunctionCode); err != nil {
		writeError(w, err)
		return
	}
	if archs != nil {
		fn.Config.Architectures = archs
	}
	fn.Config.LastModified = lastModifiedNow()
	fn.Config.RevisionId = newRevisionID()
	fn.Config.LastUpdateStatus = "Successful"

	out := fn.Config
	if in.Publish {
		out = h.publish(r, fn, "").Config
	}
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		writeError(w, err)
		return
	}
	if oldBlob != fn.CodeBlob && !h.blobInUse(r, ref.Name, oldBlob) {
		removeCode(r, oldBlob)
	}
	writeJSON(w, http.StatusOK, out)
}

//
// DeleteFunction  DELETE /2015-03-31/functions/{name}[?Qualifier=N]
//

func (h *Handler) DeleteFunction(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	switch q := ref.Qualifier; {
	case q == "":
	case q == "$LATEST":
		writeError(w, errInvalid("$LATEST version cannot be deleted without deleting the function."))
		return
	case !isVersion(q):
		writeError(w, errInvalid("Deletion of aliases is not currently supported."))
		return
	default:
		v, err := h.loadVersion(r, ref.Name, q)
		if err != nil {
			writeError(w, err)
			return
		}
		var users []string
		for _, row := range h.listPrefixed(r, typeAlias, ref.Name) {
			var a AliasConfiguration
			if json.Unmarshal(row.Attributes, &a) == nil && (a.FunctionVersion == q || a.RoutingConfig != nil && a.RoutingConfig.AdditionalVersionWeights[q] > 0) {
				users = append(users, a.AliasArn)
			}
		}
		if len(users) > 0 {
			writeError(w, errConflict("Unable to delete version because the following aliases reference it: "+strings.Join(users, ", ")))
			return
		}
		h.del(r, typeVersion, ref.Name+":"+q)
		h.del(r, typePolicy, ref.Name+":"+q)
		h.del(r, typeEventInvoke, ref.Name+":"+q)
		if !h.blobInUse(r, ref.Name, v.CodeBlob) {
			removeCode(r, v.CodeBlob)
		}
		noContent(w)
		return
	}

	blobs := map[string]bool{fn.CodeBlob: true}
	for _, row := range h.listPrefixed(r, typeVersion, ref.Name) {
		var v versionRecord
		if json.Unmarshal(row.Attributes, &v) == nil {
			blobs[v.CodeBlob] = true
		}
		h.del(r, typeVersion, row.ID)
	}
	for _, typ := range []string{typeAlias, typePolicy, typeURL, typeEventInvoke} {
		for _, row := range h.listPrefixed(r, typ, ref.Name) {
			h.del(r, typ, row.ID)
		}
	}
	h.del(r, typeFunction, ref.Name)
	for blob := range blobs {
		removeCode(r, blob)
	}
	noContent(w)
}

//
// ListFunctions  GET /2015-03-31/functions
//

func (h *Handler) ListFunctions(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.Store.List(service, typeFunction, ns(r))
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	all := r.URL.Query().Get("FunctionVersion") == "ALL"
	var configs []FunctionConfiguration
	for _, row := range rows {
		var fn functionRecord
		if json.Unmarshal(row.Attributes, &fn) != nil {
			continue
		}
		configs = append(configs, fn.Config)
		if all {
			for _, v := range h.versions(r, row.ID) {
				configs = append(configs, v.Config)
			}
		}
	}
	start, end, next := page(r, len(configs))
	out := map[string]any{"Functions": append([]FunctionConfiguration{}, configs[start:end]...)}
	if next != "" {
		out["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

//
// Versions
//

// versions returns a function's published versions, oldest first.
func (h *Handler) versions(r *http.Request, name string) []versionRecord {
	var out []versionRecord
	for _, row := range h.listPrefixed(r, typeVersion, name) {
		var v versionRecord
		if json.Unmarshal(row.Attributes, &v) == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].Config.Version)
		b, _ := strconv.Atoi(out[j].Config.Version)
		return a < b
	})
	return out
}

// fingerprint is what decides whether $LATEST differs from a version: the
// code and the configuration, without the fields every save changes.
func fingerprint(cfg FunctionConfiguration) string {
	cfg.FunctionArn, cfg.Version, cfg.LastModified, cfg.RevisionId = "", "", "", ""
	cfg.Description, cfg.State, cfg.LastUpdateStatus = "", "", ""
	b, _ := json.Marshal(cfg)
	return string(b)
}

// publish snapshots $LATEST as a new version, unless nothing changed since
// the last one, in which case Lambda returns that version. The caller holds
// h.mu and saves fn afterwards.
func (h *Handler) publish(r *http.Request, fn *functionRecord, description string) versionRecord {
	name := fn.Config.FunctionName
	if vs := h.versions(r, name); len(vs) > 0 {
		last := vs[len(vs)-1]
		if fingerprint(last.Config) == fingerprint(fn.Config) {
			return last
		}
	}
	fn.LastVersion++
	n := strconv.Itoa(fn.LastVersion)
	v := versionRecord{Config: fn.Config, CodeBlob: fn.CodeBlob}
	v.Config.Version = n
	v.Config.FunctionArn = qualifiedARN(r, name, n)
	v.Config.RevisionId = newRevisionID()
	v.Config.LastModified = lastModifiedNow()
	if description != "" {
		v.Config.Description = description
	}
	_ = h.put(r, typeVersion, name+":"+n, v)
	return v
}

// PublishVersion  POST /2015-03-31/functions/{name}/versions
func (h *Handler) PublishVersion(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		CodeSha256  string
		Description string
		RevisionId  string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if in.RevisionId != "" && in.RevisionId != fn.Config.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id"))
		return
	}
	if in.CodeSha256 != "" && in.CodeSha256 != fn.Config.CodeSha256 {
		writeError(w, errInvalid(fmt.Sprintf("CodeSHA256 (%s) is different from current CodeSHA256 in $LATEST (%s). Please try again with the CodeSHA256 in $LATEST.", in.CodeSha256, fn.Config.CodeSha256)))
		return
	}
	v := h.publish(r, fn, in.Description)
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v.Config)
}

// ListVersionsByFunction  GET /2015-03-31/functions/{name}/versions
func (h *Handler) ListVersionsByFunction(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	latest := fn.Config
	latest.FunctionArn = qualifiedARN(r, ref.Name, "$LATEST")
	all := []FunctionConfiguration{latest}
	for _, v := range h.versions(r, ref.Name) {
		all = append(all, v.Config)
	}
	start, end, next := page(r, len(all))
	out := map[string]any{"Versions": all[start:end]}
	if next != "" {
		out["NextMarker"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

//
// Concurrency
//

const accountConcurrency, minUnreserved = 1000, 100

// PutFunctionConcurrency  PUT /2017-10-31/functions/{name}/concurrency
func (h *Handler) PutFunctionConcurrency(w http.ResponseWriter, r *http.Request, name string) {
	var in struct{ ReservedConcurrentExecutions *int }
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if in.ReservedConcurrentExecutions == nil || *in.ReservedConcurrentExecutions < 0 {
		writeError(w, errValidation("1 validation error detected: Value at 'reservedConcurrentExecutions' failed to satisfy constraint: Member must have value greater than or equal to 0"))
		return
	}
	n := *in.ReservedConcurrentExecutions

	h.mu.Lock()
	defer h.mu.Unlock()
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	reserved := n
	rows, _ := h.Store.List(service, typeFunction, ns(r))
	for _, row := range rows {
		var other functionRecord
		if row.ID != ref.Name && json.Unmarshal(row.Attributes, &other) == nil && other.Concurrency != nil {
			reserved += *other.Concurrency
		}
	}
	if accountConcurrency-reserved < minUnreserved {
		writeError(w, errInvalid(fmt.Sprintf("Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [%d].", minUnreserved)))
		return
	}
	fn.Concurrency = &n
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"ReservedConcurrentExecutions": n})
}

// GetFunctionConcurrency  GET /2019-09-30/functions/{name}/concurrency
func (h *Handler) GetFunctionConcurrency(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	out := map[string]int{}
	if fn.Concurrency != nil {
		out["ReservedConcurrentExecutions"] = *fn.Concurrency
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteFunctionConcurrency  DELETE /2017-10-31/functions/{name}/concurrency
func (h *Handler) DeleteFunctionConcurrency(w http.ResponseWriter, r *http.Request, name string) {
	h.updateFunction(w, r, name, func(fn *functionRecord) error {
		fn.Concurrency = nil
		return nil
	}, func(fn *functionRecord) { noContent(w) })
}

// updateFunction is the shared shape of the small per-function settings:
// load under the lock, change, save, answer.
func (h *Handler) updateFunction(w http.ResponseWriter, r *http.Request, name string, change func(*functionRecord) error, respond func(*functionRecord)) {
	ref, err := parseFunctionName(r, name)
	if err == nil {
		err = ref.requireUnqualified()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := change(fn); err != nil {
		writeError(w, err)
		return
	}
	if err := h.put(r, typeFunction, ref.Name, fn); err != nil {
		writeError(w, err)
		return
	}
	respond(fn)
}

//
// Code signing, recursion, runtime management: stored settings only
//

func (h *Handler) GetFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"CodeSigningConfigArn": fn.CodeSigningConfigArn,
		"FunctionName":         ref.Name,
	})
}

func (h *Handler) PutFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request, name string) {
	var in struct{ CodeSigningConfigArn string }
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	h.updateFunction(w, r, name, func(fn *functionRecord) error {
		if in.CodeSigningConfigArn == "" {
			return errValidation("1 validation error detected: Value null at 'codeSigningConfigArn' failed to satisfy constraint: Member must not be null")
		}
		fn.CodeSigningConfigArn = in.CodeSigningConfigArn
		return nil
	}, func(fn *functionRecord) {
		writeJSON(w, http.StatusOK, map[string]string{
			"CodeSigningConfigArn": fn.CodeSigningConfigArn,
			"FunctionName":         fn.Config.FunctionName,
		})
	})
}

func (h *Handler) DeleteFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request, name string) {
	h.updateFunction(w, r, name, func(fn *functionRecord) error {
		fn.CodeSigningConfigArn = ""
		return nil
	}, func(*functionRecord) { noContent(w) })
}

func (h *Handler) GetFunctionRecursionConfig(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	fn, err := h.loadFunction(r, ref.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	loop := fn.RecursiveLoop
	if loop == "" {
		loop = "Terminate"
	}
	writeJSON(w, http.StatusOK, map[string]string{"RecursiveLoop": loop})
}

func (h *Handler) PutFunctionRecursionConfig(w http.ResponseWriter, r *http.Request, name string) {
	var in struct{ RecursiveLoop string }
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	h.updateFunction(w, r, name, func(fn *functionRecord) error {
		if in.RecursiveLoop != "Allow" && in.RecursiveLoop != "Terminate" {
			return errValidation("1 validation error detected: Value '" + in.RecursiveLoop + "' at 'recursiveLoop' failed to satisfy constraint: Member must satisfy enum value set: [Terminate, Allow]")
		}
		fn.RecursiveLoop = in.RecursiveLoop
		return nil
	}, func(fn *functionRecord) {
		writeJSON(w, http.StatusOK, map[string]string{"RecursiveLoop": fn.RecursiveLoop})
	})
}

func (h *Handler) GetRuntimeManagementConfig(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := h.resolve(r, ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"UpdateRuntimeOn": "Auto",
		"FunctionArn":     res.config.FunctionArn,
	})
}
