// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/api/kms"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// paramRecord is a parameter as stored: its tags plus every version, oldest
// first. The last version is the current one.
type paramRecord struct {
	Name     string         `json:"name"`
	ARN      string         `json:"arn"`
	Tags     []Tag          `json:"tags,omitempty"`
	Versions []paramVersion `json:"versions"`
}

// paramVersion is one version. A SecureString's Value is the base64 KMS
// ciphertext; it is only decrypted on a read WithDecryption.
type paramVersion struct {
	Version          int64    `json:"version"`
	Type             string   `json:"type"`
	Value            string   `json:"value"`
	Description      string   `json:"description,omitempty"`
	KeyID            string   `json:"key_id,omitempty"`
	AllowedPattern   string   `json:"allowed_pattern,omitempty"`
	Tier             string   `json:"tier"`
	DataType         string   `json:"data_type"`
	Policies         string   `json:"policies,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	LastModifiedDate float64  `json:"last_modified_date"`
	LastModifiedUser string   `json:"last_modified_user,omitempty"`
}

func (p *paramRecord) current() *paramVersion { return &p.Versions[len(p.Versions)-1] }

func (p *paramRecord) version(v int64) *paramVersion {
	for i := range p.Versions {
		if p.Versions[i].Version == v {
			return &p.Versions[i]
		}
	}
	return nil
}

func (p *paramRecord) labelled(label string) *paramVersion {
	for i := range p.Versions {
		for _, l := range p.Versions[i].Labels {
			if l == label {
				return &p.Versions[i]
			}
		}
	}
	return nil
}

func parameterArn(r *http.Request, name string) string {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return "arn:aws:ssm:" + util.Region(r) + ":" + util.AccountID(r) + ":parameter" + name
}

// nameFromID turns a parameter ARN into its name; names pass through.
func nameFromID(id string) string {
	if !strings.HasPrefix(id, "arn:") {
		return id
	}
	parts := strings.SplitN(id, ":", 6)
	if len(parts) < 6 || !strings.HasPrefix(parts[5], "parameter/") {
		return id
	}
	name := strings.TrimPrefix(parts[5], "parameter")
	// A name without a hierarchy has no leading slash of its own
	if !strings.Contains(name[1:], "/") {
		name = name[1:]
	}
	return name
}

func epoch(t time.Time) float64 { return float64(t.UnixNano()/int64(time.Millisecond)) / 1000 }

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-/]+$`)

func validateName(name string) *ssmError {
	if len(name) > 2048 || !nameRe.MatchString(name) {
		return errValidation("Parameter name: can't be prefixed with \"aws\" or \"ssm\" (case-insensitive). It must use only letters, numbers, or the following symbols: . (period), - (hyphen), _ (underscore). Special characters are not allowed. All sub-paths, if specified, must use the forward slash symbol \"/\". Valid example: /get/parameters2-/by1./path0_.")
	}
	if strings.Contains(name, "/") && !strings.HasPrefix(name, "/") {
		return errValidation("Parameter name must be a fully qualified name.")
	}
	if strings.Count(name, "/") > 15 {
		return &ssmError{"HierarchyLevelLimitExceededException", "A hierarchy can have a maximum of 15 levels."}
	}
	first := strings.ToLower(strings.TrimPrefix(name, "/"))
	if strings.HasPrefix(first, "aws") || strings.HasPrefix(first, "ssm") {
		return errValidation("No access to reserved parameter name: " + name + ".")
	}
	return nil
}

func (h *Handler) load(ns, name string) (*resource.Resource, *paramRecord, *ssmError) {
	res, err := h.Store.Get(name, "ssm", "parameter", ns)
	if err != nil {
		return nil, nil, errParamNotFound(name)
	}
	var p paramRecord
	if err := json.Unmarshal(res.Attributes, &p); err != nil || len(p.Versions) == 0 {
		return nil, nil, &ssmError{"InternalServerError", "Failed to decode parameter " + name}
	}
	return res, &p, nil
}

func (h *Handler) save(res *resource.Resource, p *paramRecord, create bool) *ssmError {
	buf, _ := json.Marshal(p)
	res.Attributes = buf
	var err error
	if create {
		err = h.Store.Create(res)
	} else {
		err = h.Store.Update(res)
	}
	if err != nil {
		return &ssmError{"InternalServerError", "Failed to store parameter: " + err.Error()}
	}
	return nil
}

func encryptionContext(arn string) map[string]string {
	return map[string]string{"PARAMETER_ARN": arn}
}

// kmsError reports a KMS failure the way SSM does
func kmsError(err error) *ssmError {
	if e, ok := err.(*kms.Error); ok {
		switch e.Type {
		case "NotFoundException", "ValidationException":
			return &ssmError{"InvalidKeyId", e.Message}
		}
		return &ssmError{"InvalidKeyId", e.Type + ": " + e.Message}
	}
	return &ssmError{"InternalServerError", err.Error()}
}

// plainValue returns a version's value, decrypting a SecureString only when
// asked; otherwise the caller sees the ciphertext, as in AWS.
func (h *Handler) plainValue(r *http.Request, p *paramRecord, v *paramVersion, decrypt bool) (string, *ssmError) {
	if v.Type != "SecureString" || !decrypt {
		return v.Value, nil
	}
	blob, err := base64.StdEncoding.DecodeString(v.Value)
	if err != nil {
		return "", &ssmError{"InternalServerError", "Corrupt SecureString value for " + p.Name}
	}
	plain, _, err := h.KMS.DecryptFor(r, blob, encryptionContext(p.ARN))
	if err != nil {
		return "", kmsError(err)
	}
	return string(plain), nil
}

// PutParameter creates a parameter, or with Overwrite adds a version. An
// overwrite keeps the description, key, data type and tier it does not name.
func (h *Handler) PutParameter(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req PutParameterInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := validateName(req.Name); e != nil {
		writeError(w, e)
		return
	}
	if req.Value == nil || *req.Value == "" {
		writeError(w, errValidation("1 validation error detected: Value '' at 'value' failed to satisfy constraint: Member must have length greater than or equal to 1"))
		return
	}
	if req.Overwrite && len(req.Tags) > 0 {
		writeError(w, errValidation("Invalid request: tags and overwrite can't be used together. To create a parameter with tags, please remove overwrite flag. To update tags on an existing parameter, please use AddTagsToResource or RemoveTagsFromResource."))
		return
	}
	switch req.Type {
	case "", "String", "StringList", "SecureString":
	default:
		writeError(w, errValidation("1 validation error detected: Value '"+req.Type+"' at 'type' failed to satisfy constraint: Member must satisfy enum value set: [SecureString, StringList, String]"))
		return
	}

	res, p, lerr := h.load(ns, req.Name)
	exists := lerr == nil
	if exists && !req.Overwrite {
		writeError(w, &ssmError{"ParameterAlreadyExists", "The parameter already exists. To overwrite this value, set the overwrite option in the request to true."})
		return
	}

	var prev paramVersion
	if exists {
		prev = *p.current()
	} else {
		if req.Type == "" {
			writeError(w, errValidation("A parameter type is required when you create a parameter."))
			return
		}
		p = &paramRecord{Name: req.Name, ARN: parameterArn(r, req.Name), Tags: req.Tags}
		res = &resource.Resource{ID: req.Name, Namespace: ns, Service: "ssm", Type: "parameter"}
	}

	v := paramVersion{
		Version:          prev.Version + 1,
		Type:             pick(req.Type, prev.Type),
		Value:            *req.Value,
		Description:      prev.Description,
		AllowedPattern:   prev.AllowedPattern,
		DataType:         pick(req.DataType, prev.DataType, "text"),
		Policies:         prev.Policies,
		LastModifiedDate: epoch(time.Now()),
		LastModifiedUser: "arn:aws:iam::" + util.AccountID(r) + ":root",
	}
	if req.Description != nil {
		v.Description = *req.Description
	}
	if req.AllowedPattern != nil {
		v.AllowedPattern = *req.AllowedPattern
	}
	if req.Policies != nil {
		v.Policies = *req.Policies
	}
	switch v.DataType {
	case "text", "aws:ec2:image", "aws:ssm:integration":
	default:
		writeError(w, errValidation("The following data type is not supported: "+v.DataType+" (Data type names are all lowercase.)"))
		return
	}

	if v.AllowedPattern != "" {
		re, err := regexp.Compile(v.AllowedPattern)
		if err != nil {
			writeError(w, &ssmError{"InvalidAllowedPatternException", "The allowed pattern is not a valid regular expression."})
			return
		}
		if !re.MatchString(v.Value) {
			writeError(w, &ssmError{"ParameterPatternMismatchException", "Parameter value, cannot be validated against allowedPattern: " + v.AllowedPattern})
			return
		}
	}

	// Tier: Intelligent-Tiering picks the cheapest tier that fits. A
	// parameter never moves from Advanced back to Standard.
	tier := pick(req.Tier, prev.Tier, "Standard")
	if tier == "Intelligent-Tiering" {
		tier = "Standard"
		if len(v.Value) > 4096 || v.Policies != "" || prev.Tier == "Advanced" {
			tier = "Advanced"
		}
	}
	switch tier {
	case "Standard":
		if prev.Tier == "Advanced" {
			writeError(w, errValidation("This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier."))
			return
		}
		if len(v.Value) > 4096 {
			writeError(w, errValidation("Standard tier parameters support a maximum parameter value of 4096 characters. To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier."))
			return
		}
	case "Advanced":
		if len(v.Value) > 8192 {
			writeError(w, errValidation("Advanced tier parameters support a maximum parameter value of 8192 characters."))
			return
		}
	default:
		writeError(w, errValidation("1 validation error detected: Value '"+tier+"' at 'tier' failed to satisfy constraint: Member must satisfy enum value set: [Advanced, Intelligent-Tiering, Standard]"))
		return
	}
	v.Tier = tier

	if v.Type == "SecureString" {
		v.KeyID = req.KeyId
		if v.KeyID == "" && prev.Type == "SecureString" {
			v.KeyID = prev.KeyID
		}
		if v.KeyID == "" {
			v.KeyID = defaultKeyID
		}
		blob, _, err := h.KMS.EncryptFor(r, v.KeyID, []byte(v.Value), encryptionContext(p.ARN))
		if err != nil {
			writeError(w, kmsError(err))
			return
		}
		v.Value = base64.StdEncoding.EncodeToString(blob)
	}

	if len(p.Versions) >= maxVersions {
		if len(p.Versions[0].Labels) > 0 {
			writeError(w, &ssmError{"ParameterMaxVersionLimitExceeded", "You attempted to create a new version of " + p.Name +
				" by calling the PutParameter API with the overwrite flag. Version 1, the oldest version, can't be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again."})
			return
		}
		p.Versions = p.Versions[1:]
	}
	p.Versions = append(p.Versions, v)

	if e := h.save(res, p, !exists); e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, PutParameterOutput{Version: v.Version, Tier: v.Tier})
}

func pick(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolve finds a parameter by name or ARN, with an optional ":version" or
// ":label" selector, and returns the version it names.
func (h *Handler) resolve(ns, ref string) (*paramRecord, *paramVersion, string, *ssmError) {
	name, selector := nameFromID(ref), ""
	if strings.HasPrefix(ref, "arn:") {
		// ARNs contain colons, so a selector can only follow the parameter part
		if i := strings.LastIndex(name, ":"); i > 0 && !strings.Contains(name[i:], "/") {
			name, selector = name[:i], name[i+1:]
		}
	} else if i := strings.LastIndex(name, ":"); i > 0 {
		name, selector = name[:i], name[i+1:]
	}
	_, p, e := h.load(ns, name)
	if e != nil {
		return nil, nil, "", e
	}
	if selector == "" {
		return p, p.current(), "", nil
	}
	if n, err := strconv.ParseInt(selector, 10, 64); err == nil {
		if v := p.version(n); v != nil {
			return p, v, ":" + selector, nil
		}
		return nil, nil, "", &ssmError{"ParameterVersionNotFound", "Systems Manager could not find version " + selector + " of " + name + ". Verify the version and try again."}
	}
	if v := p.labelled(selector); v != nil {
		return p, v, ":" + selector, nil
	}
	return nil, nil, "", &ssmError{"ParameterVersionNotFound", "Systems Manager could not find label " + selector + " of " + name + ". Verify the label and try again."}
}

func (h *Handler) parameter(r *http.Request, p *paramRecord, v *paramVersion, selector string, decrypt bool) (Parameter, *ssmError) {
	value, e := h.plainValue(r, p, v, decrypt)
	if e != nil {
		return Parameter{}, e
	}
	return Parameter{
		Name:             p.Name,
		Type:             v.Type,
		Value:            value,
		Version:          v.Version,
		Selector:         selector,
		LastModifiedDate: v.LastModifiedDate,
		ARN:              p.ARN,
		DataType:         v.DataType,
	}, nil
}

// GetParameter retrieves a parameter
func (h *Handler) GetParameter(w http.ResponseWriter, r *http.Request) {
	var req GetParameterInput
	if !decode(w, r, &req) {
		return
	}
	p, v, sel, e := h.resolve(util.NamespaceFromHeader(r), req.Name)
	if e != nil {
		writeError(w, e)
		return
	}
	param, e := h.parameter(r, p, v, sel, req.WithDecryption)
	if e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, GetParameterOutput{Parameter: param})
}

// GetParameters retrieves up to 10 parameters; unknown names come back as InvalidParameters
func (h *Handler) GetParameters(w http.ResponseWriter, r *http.Request) {
	var req GetParametersInput
	if !decode(w, r, &req) {
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 10 {
		writeError(w, errValidation("1 validation error detected: Value at 'names' failed to satisfy constraint: Member must have length less than or equal to 10, and greater than or equal to 1"))
		return
	}
	ns := util.NamespaceFromHeader(r)
	out := GetParametersOutput{Parameters: []Parameter{}, InvalidParameters: []string{}}
	for _, name := range req.Names {
		p, v, sel, e := h.resolve(ns, name)
		if e != nil {
			out.InvalidParameters = append(out.InvalidParameters, name)
			continue
		}
		param, e := h.parameter(r, p, v, sel, req.WithDecryption)
		if e != nil {
			writeError(w, e)
			return
		}
		out.Parameters = append(out.Parameters, param)
	}
	writeSSMJSON(w, http.StatusOK, out)
}

// all returns every parameter in the caller's scope, sorted by name.
func (h *Handler) all(ns string) ([]paramRecord, *ssmError) {
	rows, err := h.Store.List("ssm", "parameter", ns)
	if err != nil {
		return nil, &ssmError{"InternalServerError", "Failed to list parameters: " + err.Error()}
	}
	out := make([]paramRecord, 0, len(rows))
	for _, row := range rows {
		var p paramRecord
		if json.Unmarshal(row.Attributes, &p) == nil && len(p.Versions) > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// underPath reports whether name is in the hierarchy below path: directly
// below it, or anywhere below it when recursive.
func underPath(name, path string, recursive bool) bool {
	prefix := strings.TrimSuffix(path, "/") + "/"
	if !strings.HasPrefix(name, prefix) || name == prefix {
		return false
	}
	return recursive || !strings.Contains(name[len(prefix):], "/")
}

// matchOption applies a filter option to one value
func matchOption(option, got string, values []string) bool {
	for _, want := range values {
		switch option {
		case "BeginsWith":
			if strings.HasPrefix(got, want) {
				return true
			}
		case "Contains":
			if strings.Contains(got, want) {
				return true
			}
		default:
			if got == want {
				return true
			}
		}
	}
	return false
}

// matchFilters applies ParameterStringFilters to the current version.
func matchFilters(p *paramRecord, filters []ParameterFilter) bool {
	v := p.current()
	for _, f := range filters {
		var ok bool
		switch {
		case f.Key == "Name":
			name := p.Name
			ok = matchOption(f.Option, name, f.Values) ||
				matchOption(f.Option, strings.TrimPrefix(name, "/"), f.Values)
		case f.Key == "Type":
			ok = matchOption(f.Option, v.Type, f.Values)
		case f.Key == "KeyId":
			ok = v.Type == "SecureString" && matchOption(f.Option, v.KeyID, f.Values)
		case f.Key == "Tier":
			ok = matchOption(f.Option, v.Tier, f.Values)
		case f.Key == "DataType":
			ok = matchOption(f.Option, v.DataType, f.Values)
		case f.Key == "Label":
			for i := range p.Versions {
				for _, l := range p.Versions[i].Labels {
					ok = ok || matchOption(f.Option, l, f.Values)
				}
			}
		case f.Key == "Path":
			for _, path := range f.Values {
				ok = ok || underPath(p.Name, path, f.Option == "Recursive")
			}
		case strings.HasPrefix(f.Key, "tag:"):
			key := strings.TrimPrefix(f.Key, "tag:")
			for _, t := range p.Tags {
				if t.Key == key && (len(f.Values) == 0 || matchOption(f.Option, t.Value, f.Values)) {
					ok = true
				}
			}
		default:
			ok = true
		}
		if !ok {
			return false
		}
	}
	return true
}

func validateFilters(filters []ParameterFilter, byPath bool) *ssmError {
	for _, f := range filters {
		switch {
		case f.Key == "Name", f.Key == "Path":
			if byPath {
				return &ssmError{"InvalidFilterKey", "The following filter key is not valid: " + f.Key + ". Valid filter keys include: [Type, KeyId, Label]."}
			}
		case f.Key == "Type", f.Key == "KeyId", f.Key == "Label", f.Key == "Tier", f.Key == "DataType", strings.HasPrefix(f.Key, "tag:"):
		default:
			return &ssmError{"InvalidFilterKey", "The following filter key is not valid: " + f.Key}
		}
		switch f.Option {
		case "", "Equals", "BeginsWith", "Contains", "OneLevel", "Recursive":
		default:
			return &ssmError{"InvalidFilterOption", "The following filter option is not valid: " + f.Option}
		}
	}
	return nil
}

// page applies MaxResults/NextToken paging; the token is an offset.
func page(n int, maxResults, def, max int32, token string) (int, int, string, *ssmError) {
	if maxResults < 0 || maxResults > max {
		return 0, 0, "", errValidation("1 validation error detected: Value '" + strconv.Itoa(int(maxResults)) +
			"' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to " + strconv.Itoa(int(max)))
	}
	start := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		i, err2 := strconv.Atoi(string(b))
		if err != nil || err2 != nil || i < 0 || i > n {
			return 0, 0, "", &ssmError{"InvalidNextToken", "The specified token is not valid."}
		}
		start = i
	}
	size := int(def)
	if maxResults > 0 {
		size = int(maxResults)
	}
	end := start + size
	if end >= n {
		return start, n, "", nil
	}
	return start, end, base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end))), nil
}

// GetParametersByPath returns the parameters below a path, one level deep or
// recursively, narrowed by ParameterFilters, ten to a page by default.
func (h *Handler) GetParametersByPath(w http.ResponseWriter, r *http.Request) {
	var req GetParametersByPathInput
	if !decode(w, r, &req) {
		return
	}
	if !strings.HasPrefix(req.Path, "/") {
		writeError(w, errValidation("The parameter doesn't meet the parameter name requirements. The parameter name must begin with a forward slash \"/\"."))
		return
	}
	if e := validateFilters(req.ParameterFilters, true); e != nil {
		writeError(w, e)
		return
	}
	all, e := h.all(util.NamespaceFromHeader(r))
	if e != nil {
		writeError(w, e)
		return
	}
	var matched []paramRecord
	for _, p := range all {
		if underPath(p.Name, req.Path, req.Recursive) && matchFilters(&p, req.ParameterFilters) {
			matched = append(matched, p)
		}
	}
	start, end, next, e := page(len(matched), req.MaxResults, 10, 10, req.NextToken)
	if e != nil {
		writeError(w, e)
		return
	}
	out := GetParametersByPathOutput{Parameters: []Parameter{}, NextToken: next}
	for i := start; i < end; i++ {
		p := &matched[i]
		param, e := h.parameter(r, p, p.current(), "", req.WithDecryption)
		if e != nil {
			writeError(w, e)
			return
		}
		out.Parameters = append(out.Parameters, param)
	}
	writeSSMJSON(w, http.StatusOK, out)
}

func policies(v *paramVersion) []Policy {
	if v.Policies == "" {
		return []Policy{}
	}
	var raw []map[string]any
	if json.Unmarshal([]byte(v.Policies), &raw) != nil {
		return []Policy{}
	}
	out := make([]Policy, 0, len(raw))
	for _, p := range raw {
		text, _ := json.Marshal(p)
		typ, _ := p["Type"].(string)
		out = append(out, Policy{PolicyText: string(text), PolicyType: typ, PolicyStatus: "Pending"})
	}
	return out
}

// DescribeParameters returns parameter metadata (never values), filtered by
// the legacy Filters or ParameterFilters, fifty to a page by default.
func (h *Handler) DescribeParameters(w http.ResponseWriter, r *http.Request) {
	var req DescribeParametersInput
	if !decode(w, r, &req) {
		return
	}
	if len(req.Filters) > 0 && len(req.ParameterFilters) > 0 {
		writeError(w, errValidation("You can use either Filters or ParameterFilters in a single request."))
		return
	}
	filters := req.ParameterFilters
	for _, f := range req.Filters {
		filters = append(filters, ParameterFilter{Key: f.Key, Values: f.Values, Option: "Equals"})
	}
	if e := validateFilters(filters, false); e != nil {
		writeError(w, e)
		return
	}
	all, e := h.all(util.NamespaceFromHeader(r))
	if e != nil {
		writeError(w, e)
		return
	}
	var matched []paramRecord
	for _, p := range all {
		if matchFilters(&p, filters) {
			matched = append(matched, p)
		}
	}
	start, end, next, e := page(len(matched), req.MaxResults, 50, 50, req.NextToken)
	if e != nil {
		writeError(w, e)
		return
	}
	out := DescribeParametersOutput{Parameters: []ParameterMetadata{}, NextToken: next}
	for _, p := range matched[start:end] {
		v := p.current()
		out.Parameters = append(out.Parameters, ParameterMetadata{
			Name:             p.Name,
			ARN:              p.ARN,
			Type:             v.Type,
			KeyId:            v.KeyID,
			LastModifiedDate: v.LastModifiedDate,
			LastModifiedUser: v.LastModifiedUser,
			Description:      v.Description,
			AllowedPattern:   v.AllowedPattern,
			Version:          v.Version,
			Tier:             v.Tier,
			Policies:         policies(v),
			DataType:         v.DataType,
		})
	}
	writeSSMJSON(w, http.StatusOK, out)
}

// GetParameterHistory lists every kept version, oldest first
func (h *Handler) GetParameterHistory(w http.ResponseWriter, r *http.Request) {
	var req GetParameterHistoryInput
	if !decode(w, r, &req) {
		return
	}
	_, p, e := h.load(util.NamespaceFromHeader(r), nameFromID(req.Name))
	if e != nil {
		writeError(w, e)
		return
	}
	start, end, next, e := page(len(p.Versions), req.MaxResults, 50, 50, req.NextToken)
	if e != nil {
		writeError(w, e)
		return
	}
	out := GetParameterHistoryOutput{Parameters: []ParameterHistory{}, NextToken: next}
	for i := start; i < end; i++ {
		v := &p.Versions[i]
		value, e := h.plainValue(r, p, v, req.WithDecryption)
		if e != nil {
			writeError(w, e)
			return
		}
		labels := v.Labels
		if labels == nil {
			labels = []string{}
		}
		out.Parameters = append(out.Parameters, ParameterHistory{
			Name:             p.Name,
			Type:             v.Type,
			KeyId:            v.KeyID,
			LastModifiedDate: v.LastModifiedDate,
			LastModifiedUser: v.LastModifiedUser,
			Description:      v.Description,
			Value:            value,
			AllowedPattern:   v.AllowedPattern,
			Version:          v.Version,
			Labels:           labels,
			Tier:             v.Tier,
			Policies:         policies(v),
			DataType:         v.DataType,
		})
	}
	writeSSMJSON(w, http.StatusOK, out)
}

var labelRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,100}$`)

func validLabel(l string) bool {
	if !labelRe.MatchString(l) {
		return false
	}
	if _, err := strconv.Atoi(l); err == nil {
		return false
	}
	lower := strings.ToLower(l)
	return !strings.HasPrefix(lower, "aws") && !strings.HasPrefix(lower, "ssm")
}

// LabelParameterVersion attaches labels to a version (the latest by
// default), moving them off any other version of the parameter.
func (h *Handler) LabelParameterVersion(w http.ResponseWriter, r *http.Request) {
	var req LabelParameterVersionInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res, p, e := h.load(util.NamespaceFromHeader(r), nameFromID(req.Name))
	if e != nil {
		writeError(w, e)
		return
	}
	target := p.current()
	if req.ParameterVersion != nil {
		if target = p.version(*req.ParameterVersion); target == nil {
			writeError(w, &ssmError{"ParameterVersionNotFound", "Systems Manager could not find version " +
				strconv.FormatInt(*req.ParameterVersion, 10) + " of " + p.Name + ". Verify the version and try again."})
			return
		}
	}
	out := LabelParameterVersionOutput{InvalidLabels: []string{}, ParameterVersion: target.Version}
	for _, l := range req.Labels {
		if !validLabel(l) {
			out.InvalidLabels = append(out.InvalidLabels, l)
			continue
		}
		for i := range p.Versions {
			if p.Versions[i].Version != target.Version {
				p.Versions[i].Labels = without(p.Versions[i].Labels, l)
			}
		}
		if !contains(target.Labels, l) {
			target.Labels = append(target.Labels, l)
		}
	}
	if len(target.Labels) > 10 {
		writeError(w, &ssmError{"ParameterVersionLabelLimitExceeded", "A parameter version can have maximum 10 labels. Move one or more labels to another version and try again."})
		return
	}
	if e := h.save(res, p, false); e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, out)
}

// UnlabelParameterVersion removes labels from a version
func (h *Handler) UnlabelParameterVersion(w http.ResponseWriter, r *http.Request) {
	var req UnlabelParameterVersionInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res, p, e := h.load(util.NamespaceFromHeader(r), nameFromID(req.Name))
	if e != nil {
		writeError(w, e)
		return
	}
	v := p.version(req.ParameterVersion)
	if v == nil {
		writeError(w, &ssmError{"ParameterVersionNotFound", "Systems Manager could not find version " +
			strconv.FormatInt(req.ParameterVersion, 10) + " of " + p.Name + ". Verify the version and try again."})
		return
	}
	out := UnlabelParameterVersionOutput{InvalidLabels: []string{}, RemovedLabels: []string{}}
	for _, l := range req.Labels {
		if contains(v.Labels, l) {
			v.Labels = without(v.Labels, l)
			out.RemovedLabels = append(out.RemovedLabels, l)
		} else {
			out.InvalidLabels = append(out.InvalidLabels, l)
		}
	}
	if e := h.save(res, p, false); e != nil {
		writeError(w, e)
		return
	}
	writeSSMJSON(w, http.StatusOK, out)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func without(list []string, s string) []string {
	var out []string
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// DeleteParameter deletes a parameter and all its versions
func (h *Handler) DeleteParameter(w http.ResponseWriter, r *http.Request) {
	var req DeleteParameterInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ns := util.NamespaceFromHeader(r)
	name := nameFromID(req.Name)
	if _, _, e := h.load(ns, name); e != nil {
		writeError(w, e)
		return
	}
	if err := h.Store.Delete(name, "ssm", "parameter", ns); err != nil {
		writeError(w, &ssmError{"InternalServerError", "Failed to delete parameter: " + err.Error()})
		return
	}
	writeSSMJSON(w, http.StatusOK, struct{}{})
}

// DeleteParameters deletes up to ten parameters
func (h *Handler) DeleteParameters(w http.ResponseWriter, r *http.Request) {
	var req DeleteParametersInput
	if !decode(w, r, &req) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(req.Names) == 0 || len(req.Names) > 10 {
		writeError(w, errValidation("1 validation error detected: Value at 'names' failed to satisfy constraint: Member must have length less than or equal to 10, and greater than or equal to 1"))
		return
	}
	ns := util.NamespaceFromHeader(r)
	out := DeleteParametersOutput{DeletedParameters: []string{}, InvalidParameters: []string{}}
	for _, n := range req.Names {
		name := nameFromID(n)
		if _, _, e := h.load(ns, name); e != nil {
			out.InvalidParameters = append(out.InvalidParameters, n)
			continue
		}
		if err := h.Store.Delete(name, "ssm", "parameter", ns); err != nil {
			writeError(w, &ssmError{"InternalServerError", "Failed to delete parameter: " + err.Error()})
			return
		}
		out.DeletedParameters = append(out.DeletedParameters, n)
	}
	writeSSMJSON(w, http.StatusOK, out)
}
