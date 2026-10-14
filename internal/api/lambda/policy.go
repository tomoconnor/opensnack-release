// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var (
	statementIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-_.]{1,100}$`)
	actionPattern      = regexp.MustCompile(`^(lambda:[*]|lambda:[a-zA-Z]+|[*])$`)
)

func policyID(ref fnRef) string {
	if ref.Qualifier == "" {
		return ref.Name
	}
	return ref.Name + ":" + ref.Qualifier
}

// principalOf is how Lambda writes a principal into the policy: services
// under "Service", accounts and ARNs under "AWS", "*" as is.
func principalOf(p string) any {
	switch {
	case p == "*":
		return "*"
	case strings.HasSuffix(p, ".amazonaws.com") || strings.HasSuffix(p, ".amazon.com") || strings.HasSuffix(p, ".aws.internal"):
		return map[string]string{"Service": p}
	default:
		return map[string]string{"AWS": p}
	}
}

// AddPermission  POST /2015-03-31/functions/{name}/policy
func (h *Handler) AddPermission(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		StatementId           string
		Action                string
		Principal             string
		SourceArn             string
		SourceAccount         string
		EventSourceToken      string
		PrincipalOrgID        string
		FunctionUrlAuthType   string
		InvokedViaFunctionUrl bool
		RevisionId            string
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	switch {
	case !statementIDPattern.MatchString(in.StatementId):
		err = errValidation("1 validation error detected: Value '" + in.StatementId + "' at 'statementId' failed to satisfy constraint: Member must satisfy regular expression pattern: ([a-zA-Z0-9-_.]+)")
	case !actionPattern.MatchString(in.Action):
		err = errValidation("1 validation error detected: Value '" + in.Action + "' at 'action' failed to satisfy constraint: Member must satisfy regular expression pattern: (lambda:[*]|lambda:[a-zA-Z]+|[*])")
	case in.Principal == "" || strings.ContainsAny(in.Principal, " \t\n"):
		err = errValidation("1 validation error detected: Value '" + in.Principal + "' at 'principal' failed to satisfy constraint: Member must satisfy regular expression pattern: [^\\s]+")
	case in.FunctionUrlAuthType != "" && in.FunctionUrlAuthType != "NONE" && in.FunctionUrlAuthType != "AWS_IAM":
		err = errValidation("1 validation error detected: Value '" + in.FunctionUrlAuthType + "' at 'functionUrlAuthType' failed to satisfy constraint: Member must satisfy enum value set: [AWS_IAM, NONE]")
	}
	if err != nil {
		writeError(w, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := h.checkQualifier(r, ref.Name, ref.Qualifier); err != nil {
		writeError(w, err)
		return
	}
	var pol policyRecord
	h.get(r, typePolicy, policyID(ref), &pol)
	if in.RevisionId != "" && in.RevisionId != pol.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetPolicy API to retrieve the latest Revision Id"))
		return
	}
	for _, s := range pol.Statements {
		if s.Sid == in.StatementId {
			writeError(w, errConflict("The statement id ("+in.StatementId+") provided already exists. Please provide a new statement id, or remove the existing statement."))
			return
		}
	}

	st := policyStatement{
		Sid:       in.StatementId,
		Effect:    "Allow",
		Principal: principalOf(in.Principal),
		Action:    in.Action,
		Resource:  qualifiedARN(r, ref.Name, ref.Qualifier),
	}
	cond := map[string]map[string]string{}
	add := func(op, key, value string) {
		if value == "" {
			return
		}
		if cond[op] == nil {
			cond[op] = map[string]string{}
		}
		cond[op][key] = value
	}
	add("StringEquals", "AWS:SourceAccount", in.SourceAccount)
	add("StringEquals", "lambda:EventSourceToken", in.EventSourceToken)
	add("StringEquals", "aws:PrincipalOrgID", in.PrincipalOrgID)
	add("StringEquals", "lambda:FunctionUrlAuthType", in.FunctionUrlAuthType)
	add("ArnLike", "AWS:SourceArn", in.SourceArn)
	if in.InvokedViaFunctionUrl {
		add("Bool", "lambda:InvokedViaFunctionUrl", "true")
	}
	if len(cond) > 0 {
		st.Condition = cond
	}

	pol.Statements = append(pol.Statements, st)
	pol.RevisionId = newRevisionID()
	if err := h.put(r, typePolicy, policyID(ref), pol); err != nil {
		writeError(w, err)
		return
	}
	b, _ := json.Marshal(st)
	writeJSON(w, http.StatusCreated, map[string]string{"Statement": string(b)})
}

// GetPolicy  GET /2015-03-31/functions/{name}/policy
func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request, name string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := h.checkQualifier(r, ref.Name, ref.Qualifier); err != nil {
		writeError(w, err)
		return
	}
	var pol policyRecord
	if !h.get(r, typePolicy, policyID(ref), &pol) || len(pol.Statements) == 0 {
		writeError(w, errNotFound("The resource you requested does not exist."))
		return
	}
	doc, _ := json.Marshal(map[string]any{
		"Version":   "2012-10-17",
		"Id":        "default",
		"Statement": pol.Statements,
	})
	writeJSON(w, http.StatusOK, map[string]string{"Policy": string(doc), "RevisionId": pol.RevisionId})
}

// RemovePermission  DELETE /2015-03-31/functions/{name}/policy/{sid}
func (h *Handler) RemovePermission(w http.ResponseWriter, r *http.Request, name, sid string) {
	ref, err := parseFunctionName(r, name)
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.loadFunction(r, ref.Name); err != nil {
		writeError(w, err)
		return
	}
	var pol policyRecord
	if !h.get(r, typePolicy, policyID(ref), &pol) {
		writeError(w, errNotFound("No policy is associated with the given resource."))
		return
	}
	if rev := r.URL.Query().Get("RevisionId"); rev != "" && rev != pol.RevisionId {
		writeError(w, errPrecondition("The Revision Id provided does not match the latest Revision Id. Call the GetPolicy API to retrieve the latest Revision Id"))
		return
	}
	kept := pol.Statements[:0]
	found := false
	for _, s := range pol.Statements {
		if s.Sid == sid {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		writeError(w, errNotFound("Statement "+sid+" is not found in resource policy."))
		return
	}
	if len(kept) == 0 {
		h.del(r, typePolicy, policyID(ref))
	} else {
		pol.Statements = kept
		pol.RevisionId = newRevisionID()
		if err := h.put(r, typePolicy, policyID(ref), pol); err != nil {
			writeError(w, err)
			return
		}
	}
	noContent(w)
}
