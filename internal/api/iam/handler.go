// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const (
	APIVersion = "2010-05-08"
	iamAccount = "000000000000"
	iamRegion  = "us-east-1"
)

//
// Construct ARNs
//

func roleArn(name string) string {
	return "arn:aws:iam::" + iamAccount + ":role/" + name
}

func policyArn(name string) string {
	return "arn:aws:iam::" + iamAccount + ":policy/" + name
}

//
// Helper: Count policy attachments
//

func (h *Handler) countPolicyAttachments(policyName, ns string) int {
	count := 0

	// Count role attachments
	roleAttachments, _ := h.Store.List("iam", "attachment", ns)
	for _, att := range roleAttachments {
		var attr map[string]any
		if err := json.Unmarshal(att.Attributes, &attr); err != nil {
			continue
		}
		if policy, ok := attr["policy"].(string); ok && policy == policyName {
			count++
		}
	}

	// Count user attachments
	userAttachments, _ := h.Store.List("iam", "user_attachment", ns)
	for _, att := range userAttachments {
		var attr map[string]any
		if err := json.Unmarshal(att.Attributes, &attr); err != nil {
			continue
		}
		if policy, ok := attr["policy"].(string); ok && policy == policyName {
			count++
		}
	}

	return count
}

//
// Handler
//

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

//
// Stubbed AWS account details
//

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	userName := r.FormValue("UserName")
	if userName == "" {
		userName = r.URL.Query().Get("UserName")
	}

	// UserName is required - look up the user from the database
	if userName == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
		return
	}

	res, err := h.Store.Get(userName, "iam", "user", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "User does not exist", userName)
		return
	}

	var attr map[string]any
	if err := json.Unmarshal(res.Attributes, &attr); err != nil {
		awsresponses.WriteErrorXML(w, 500, "InternalFailure", "Failed to parse user attributes", userName)
		return
	}

	// Safely extract attributes with defaults
	path, _ := attr["path"].(string)
	if path == "" {
		path = "/"
	}
	userID, _ := attr["user_id"].(string)
	createdAt, _ := attr["created_at"].(string)

	user := User{
		Path:       path,
		UserName:   userName,
		UserId:     userID,
		Arn:        userArn(userName),
		CreateDate: createdAt,
	}

	resp := GetUserResponse{
		GetUserResult: GetUserResult{
			User: user,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// CreateUser
//

func userArn(name string) string {
	return "arn:aws:iam::" + iamAccount + ":user/" + name
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	name := r.FormValue("UserName")
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}

	if name == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
		return
	}

	// Idempotent: return existing user if present
	if existing, err := h.Store.Get(name, "iam", "user", ns); err == nil {
		var attr map[string]any
		if err := json.Unmarshal(existing.Attributes, &attr); err != nil {
			awsresponses.WriteErrorXML(w, 500, "InternalFailure", "Failed to parse user attributes", name)
			return
		}

		// Safely extract attributes with defaults
		path, _ := attr["path"].(string)
		if path == "" {
			path = "/"
		}
		userID, _ := attr["user_id"].(string)
		createdAt, _ := attr["created_at"].(string)

		resp := CreateUserResponse{
			CreateUserResult: CreateUserResult{
				User: User{
					Path:       path,
					UserName:   name,
					UserId:     userID,
					Arn:        userArn(name),
					CreateDate: createdAt,
				},
			},
			ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	userId := name + "-" + time.Now().Format("20060102150405")
	entry := map[string]any{
		"name":       name,
		"path":       path,
		"user_id":    userId,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}

	buf, _ := json.Marshal(entry)
	err := h.Store.Create(&resource.Resource{
		ID:         name,
		Namespace:  ns,
		Service:    "iam",
		Type:       "user",
		Attributes: buf,
	})
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
		return
	}

	resp := CreateUserResponse{
		CreateUserResult: CreateUserResult{
			User: User{
				Path:       path,
				UserName:   name,
				UserId:     userId,
				Arn:        userArn(name),
				CreateDate: entry["created_at"].(string),
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

//
// UpdateUser
//

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	name := r.FormValue("UserName")
	if name == "" {
		name = r.URL.Query().Get("UserName")
	}
	newPath := r.FormValue("NewPath")
	if newPath == "" {
		newPath = r.URL.Query().Get("NewPath")
	}
	newUserName := r.FormValue("NewUserName")
	if newUserName == "" {
		newUserName = r.URL.Query().Get("NewUserName")
	}

	if name == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
		return
	}

	// If no update parameters provided, return success (idempotent)
	if newPath == "" && newUserName == "" {
		resp := UpdateUserResponse{
			ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	// Get existing user
	res, err := h.Store.Get(name, "iam", "user", ns)
	if err != nil {
		// If user doesn't exist, be lenient:
		// - If not renaming (newUserName is empty), return success (idempotent)
		// - This handles cases where Terraform calls UpdateUser to ensure defaults are set
		// - or when there's a timing issue between CreateUser and UpdateUser
		if newUserName == "" {
			resp := UpdateUserResponse{
				ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
			}
			awsresponses.WriteXML(w, resp)
			return
		}
		// If renaming and user doesn't exist, check if new name already exists
		// (this handles the case where the user was already renamed)
		if _, err2 := h.Store.Get(newUserName, "iam", "user", ns); err2 == nil {
			// New name already exists, treat as success (idempotent)
			resp := UpdateUserResponse{
				ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
			}
			awsresponses.WriteXML(w, resp)
			return
		}
		// Can't rename a user that doesn't exist
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "User does not exist", name)
		return
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	// Handle rename: need to change resource ID
	if newUserName != "" && newUserName != name {
		// Check if new name already exists
		if existing, err := h.Store.Get(newUserName, "iam", "user", ns); err == nil {
			// New name already exists, just update its attributes
			var existingAttr map[string]any
			json.Unmarshal(existing.Attributes, &existingAttr)

			// Update path if provided
			if newPath != "" {
				existingAttr["path"] = newPath
			}
			existingAttr["name"] = newUserName

			buf, _ := json.Marshal(existingAttr)
			updated := &resource.Resource{
				ID:         newUserName,
				Namespace:  ns,
				Service:    "iam",
				Type:       "user",
				Attributes: buf,
			}
			err = h.Store.Update(updated)
			if err != nil {
				awsresponses.WriteJSON(w, 500, err.Error())
			}

			// Delete old user if it's different
			if name != newUserName {
				_ = h.Store.Delete(name, "iam", "user", ns)
			}
		} else {
			// Create new user with new name
			// Update path if provided
			if newPath != "" {
				attr["path"] = newPath
			}
			attr["name"] = newUserName

			buf, _ := json.Marshal(attr)
			newUser := &resource.Resource{
				ID:         newUserName,
				Namespace:  ns,
				Service:    "iam",
				Type:       "user",
				Attributes: buf,
			}
			err = h.Store.Create(newUser)
			if err != nil {
				awsresponses.WriteJSON(w, 500, err.Error())
			}

			// Delete old user
			_ = h.Store.Delete(name, "iam", "user", ns)
		}
	} else {
		// No rename, just update attributes
		needsUpdate := false

		// Update path if provided
		if newPath != "" {
			currentPath, _ := attr["path"].(string)
			if currentPath != newPath {
				attr["path"] = newPath
				needsUpdate = true
			}
		}

		// Only update if there are actual changes
		if needsUpdate {
			buf, _ := json.Marshal(attr)
			updated := &resource.Resource{
				ID:         res.ID,
				Namespace:  res.Namespace,
				Service:    res.Service,
				Type:       res.Type,
				Attributes: buf,
			}

			err = h.Store.Update(updated)
			if err != nil {
				awsresponses.WriteJSON(w, 500, err.Error())
			}
		}
	}

	resp := UpdateUserResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// DeleteUser
//

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	name := r.FormValue("UserName")
	if name == "" {
		name = r.URL.Query().Get("UserName")
	}

	if name == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
	}

	_ = h.Store.Delete(name, "iam", "user", ns)

	resp := DeleteUserResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// ListUsers
//

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("iam", "user", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	var users []User
	for _, it := range items {
		var attr map[string]any
		if err := json.Unmarshal(it.Attributes, &attr); err != nil {
			continue
		}

		path, _ := attr["path"].(string)
		if path == "" {
			path = "/"
		}
		userID, _ := attr["user_id"].(string)
		createdAt, _ := attr["created_at"].(string)

		users = append(users, User{
			Path:       path,
			UserName:   it.ID,
			UserId:     userID,
			Arn:        userArn(it.ID),
			CreateDate: createdAt,
		})
	}

	resp := ListUsersResponse{
		ListUsersResult: ListUsersResult{
			Users:       users,
			IsTruncated: false,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	items, err := h.Store.List("iam", "role", ns)
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	var roles []Role
	for _, it := range items {
		var attr map[string]any
		json.Unmarshal(it.Attributes, &attr)

		assume, _ := attr["assume_role_policy"].(string)
		created, _ := attr["created_at"].(string)

		roles = append(roles, Role{
			Path:             "/",
			RoleName:         it.ID,
			Arn:              roleArn(it.ID),
			AssumeRolePolicy: assume,
			CreateDate:       created,
		})
	}

	resp := ListRolesResponse{
		ListRolesResult: ListRolesResult{
			Roles:       roles,
			IsTruncated: false,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// AWS Query Dispatch
//

func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	action := r.FormValue("Action")

	switch action {
	case "GetUser":
		h.GetUser(w, r)
	case "CreateUser":
		h.CreateUser(w, r)
	case "UpdateUser":
		h.UpdateUser(w, r)
	case "DeleteUser":
		h.DeleteUser(w, r)
	case "ListUsers":
		h.ListUsers(w, r)
	case "ListRoles":
		h.ListRoles(w, r)
	case "CreateRole":
		h.CreateRole(w, r)
	case "GetRole":
		h.GetRole(w, r)
	case "DeleteRole":
		h.DeleteRole(w, r)

	case "CreatePolicy":
		h.CreatePolicy(w, r)
	case "GetPolicy":
		h.GetPolicy(w, r)
	case "GetPolicyVersion":
		h.GetPolicyVersion(w, r)
	case "ListPolicyVersions":
		h.ListPolicyVersions(w, r)
	case "DeletePolicy":
		h.DeletePolicy(w, r)

	case "AttachRolePolicy":
		h.AttachRolePolicy(w, r)
	case "DetachRolePolicy":
		h.DetachRolePolicy(w, r)
	case "ListAttachedRolePolicies":
		h.ListAttachedRolePolicies(w, r)

	case "AttachUserPolicy":
		h.AttachUserPolicy(w, r)
	case "DetachUserPolicy":
		h.DetachUserPolicy(w, r)
	case "ListAttachedUserPolicies":
		h.ListAttachedUserPolicies(w, r)

	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown IAM Action",
			action,
		)
	}
}

//
// CreateRole
//

func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	name := r.FormValue("RoleName")
	policyDoc := r.FormValue("AssumeRolePolicyDocument")

	if name == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "RoleName is required", "")
	}

	// Idempotent: return existing role if present
	if existing, err := h.Store.Get(name, "iam", "role", ns); err == nil {
		var attr map[string]any
		if err := json.Unmarshal(existing.Attributes, &attr); err != nil {
			awsresponses.WriteErrorXML(w, 500, "InternalFailure", "Failed to parse role attributes", name)
			return
		}

		assumePolicy, _ := attr["assume_role_policy"].(string)
		createdAt, _ := attr["created_at"].(string)

		resp := CreateRoleResponse{
			CreateRoleResult: CreateRoleResult{
				Role: Role{
					Path:             "/",
					RoleName:         name,
					Arn:              roleArn(name),
					AssumeRolePolicy: assumePolicy,
					CreateDate:       createdAt,
				},
			},
			ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	entry := map[string]any{
		"name":               name,
		"assume_role_policy": policyDoc,
		"created_at":         time.Now().UTC().Format(time.RFC3339),
	}

	buf, _ := json.Marshal(entry)
	err := h.Store.Create(&resource.Resource{
		ID:         name,
		Namespace:  ns,
		Service:    "iam",
		Type:       "role",
		Attributes: buf,
	})
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	resp := CreateRoleResponse{
		CreateRoleResult: CreateRoleResult{
			Role: Role{
				Path:             "/",
				RoleName:         name,
				Arn:              roleArn(name),
				AssumeRolePolicy: policyDoc,
				CreateDate:       entry["created_at"].(string),
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

//
// GetRole
//

func (h *Handler) GetRole(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	name := r.URL.Query().Get("RoleName")

	res, err := h.Store.Get(name, "iam", "role", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "Role does not exist", name)
		return
	}

	var attr map[string]any
	if err := json.Unmarshal(res.Attributes, &attr); err != nil {
		awsresponses.WriteErrorXML(w, 500, "InternalFailure", "Failed to parse role attributes", name)
		return
	}

	assumePolicy, _ := attr["assume_role_policy"].(string)
	createdAt, _ := attr["created_at"].(string)

	resp := GetRoleResponse{
		GetRoleResult: GetRoleResult{
			Role: Role{
				Path:             "/",
				RoleName:         name,
				Arn:              roleArn(name),
				AssumeRolePolicy: assumePolicy,
				CreateDate:       createdAt,
			},
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// DeleteRole
//

func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	name := r.URL.Query().Get("RoleName")

	_ = h.Store.Delete(name, "iam", "role", ns)

	awsresponses.WriteEmpty200(w, nil)
}

//
// CreatePolicy
//

func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	name := r.FormValue("PolicyName")
	doc := r.FormValue("PolicyDocument")
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}

	if name == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyName required", "")
	}

	// Generate a stable PolicyId (deterministic hash of name + namespace)
	hash := sha256.Sum256([]byte(name + ":" + ns))
	policyId := "A" + strings.ToUpper(hex.EncodeToString(hash[:]))[:20]

	// Idempotent
	if existing, err := h.Store.Get(name, "iam", "policy", ns); err == nil {
		var attr map[string]any
		json.Unmarshal(existing.Attributes, &attr)

		createdAt, _ := attr["created_at"].(string)
		updateDate, _ := attr["updated_at"].(string)
		if updateDate == "" {
			updateDate = createdAt
		}

		// Count attachments
		attachmentCount := h.countPolicyAttachments(name, ns)

		// Safely extract attributes with defaults
		policyId, _ := attr["policy_id"].(string)
		if policyId == "" {
			// Generate a stable PolicyId if missing (for backward compatibility)
			hash := sha256.Sum256([]byte(name + ":" + ns))
			policyId = "A" + strings.ToUpper(hex.EncodeToString(hash[:]))[:20]
		}
		path, _ := attr["path"].(string)
		if path == "" {
			path = "/"
		}

		resp := CreatePolicyResponse{
			CreatePolicyResult: CreatePolicyResult{
				Policy: Policy{
					PolicyName:       name,
					PolicyId:         policyId,
					Arn:              policyArn(name),
					Path:             path,
					DefaultVersionId: "v1",
					CreateDate:       createdAt,
					UpdateDate:       updateDate,
					AttachmentCount:  attachmentCount,
				},
			},
			ResponseMetadata: ResponseMetadata{
				RequestId: awsresponses.NextRequestID(),
			},
		}
		awsresponses.WriteXML(w, resp)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := map[string]any{
		"name":       name,
		"document":   doc,
		"path":       path,
		"policy_id":  policyId,
		"version":    "v1",
		"created_at": now,
		"updated_at": now,
	}

	buf, _ := json.Marshal(entry)

	err := h.Store.Create(&resource.Resource{
		ID:         name,
		Namespace:  ns,
		Service:    "iam",
		Type:       "policy",
		Attributes: buf,
	})
	if err != nil {
		awsresponses.WriteJSON(w, 500, err.Error())
	}

	resp := CreatePolicyResponse{
		CreatePolicyResult: CreatePolicyResult{
			Policy: Policy{
				PolicyName:       name,
				PolicyId:         policyId,
				Arn:              policyArn(name),
				Path:             path,
				DefaultVersionId: "v1",
				CreateDate:       now,
				UpdateDate:       now,
				AttachmentCount:  0,
			},
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// GetPolicy
//

func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("PolicyArn")
	if arn == "" {
		arn = r.URL.Query().Get("PolicyArn")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyArn required", "")
	}

	name := arn[strings.LastIndex(arn, "/")+1:]

	res, err := h.Store.Get(name, "iam", "policy", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "Policy does not exist", name)
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	createdAt, _ := attr["created_at"].(string)
	updateDate, _ := attr["updated_at"].(string)
	if updateDate == "" {
		updateDate = createdAt
	}
	policyId, _ := attr["policy_id"].(string)
	path, _ := attr["path"].(string)
	if path == "" {
		path = "/"
	}

	// Count attachments
	attachmentCount := h.countPolicyAttachments(name, ns)

	resp := GetPolicyResponse{
		GetPolicyResult: GetPolicyResult{
			Policy: Policy{
				PolicyName:       name,
				PolicyId:         policyId,
				Arn:              arn,
				Path:             path,
				DefaultVersionId: "v1",
				CreateDate:       createdAt,
				UpdateDate:       updateDate,
				AttachmentCount:  attachmentCount,
			},
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// GetPolicyVersion
//

func (h *Handler) GetPolicyVersion(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("PolicyArn")
	if arn == "" {
		arn = r.URL.Query().Get("PolicyArn")
	}
	r.ParseForm()
	version := r.FormValue("VersionId")
	if version == "" {
		version = r.URL.Query().Get("VersionId")
	}
	if version == "" {
		version = "v1" // Default to v1 if not specified
	}

	if arn == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyArn required", "")
	}

	name := arn[strings.LastIndex(arn, "/")+1:]

	res, err := h.Store.Get(name, "iam", "policy", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "Policy missing", arn)
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	if version != "v1" {
		awsresponses.WriteErrorXML(w, 400, "NoSuchEntity", "Only version v1 exists", version)
	}

	doc, _ := attr["document"].(string)

	resp := GetPolicyVersionResponse{
		GetPolicyVersionResult: GetPolicyVersionResult{
			PolicyVersion: PolicyVersion{
				VersionId:        "v1",
				IsDefaultVersion: true,
				Document:         doc,
			},
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// ListPolicyVersions
//

func (h *Handler) ListPolicyVersions(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	arn := r.FormValue("PolicyArn")
	if arn == "" {
		arn = r.URL.Query().Get("PolicyArn")
	}

	if arn == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyArn required", "")
	}

	name := arn[strings.LastIndex(arn, "/")+1:]

	res, err := h.Store.Get(name, "iam", "policy", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, 404, "NoSuchEntity", "Policy does not exist", name)
	}

	var attr map[string]any
	json.Unmarshal(res.Attributes, &attr)

	doc, _ := attr["document"].(string)

	// We only support version v1
	versions := []PolicyVersion{
		{
			VersionId:        "v1",
			IsDefaultVersion: true,
			Document:         doc,
		},
	}

	resp := ListPolicyVersionsResponse{
		ListPolicyVersionsResult: ListPolicyVersionsResult{
			Versions:    versions,
			IsTruncated: false,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// DeletePolicy
//

func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	arn := r.URL.Query().Get("PolicyArn")

	name := arn[strings.LastIndex(arn, "/")+1:]

	_ = h.Store.Delete(name, "iam", "policy", ns)

	awsresponses.WriteEmpty200(w, nil)
}

//
// AttachRolePolicy
//

func (h *Handler) AttachRolePolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	role := r.URL.Query().Get("RoleName")
	arn := r.URL.Query().Get("PolicyArn")

	name := arn[strings.LastIndex(arn, "/")+1:]

	id := role + ":" + name

	entry := map[string]any{
		"role":       role,
		"policy":     name,
		"policy_arn": arn,
	}

	buf, _ := json.Marshal(entry)

	_ = h.Store.Create(&resource.Resource{
		ID:         id,
		Namespace:  ns,
		Service:    "iam",
		Type:       "attachment",
		Attributes: buf,
	})

	resp := AttachRolePolicyResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// DetachRolePolicy
//

func (h *Handler) DetachRolePolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	role := r.URL.Query().Get("RoleName")
	arn := r.URL.Query().Get("PolicyArn")

	name := arn[strings.LastIndex(arn, "/")+1:]

	id := role + ":" + name

	_ = h.Store.Delete(id, "iam", "attachment", ns)

	resp := AttachRolePolicyResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// ListAttachedRolePolicies
//

func (h *Handler) ListAttachedRolePolicies(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	role := r.URL.Query().Get("RoleName")

	items, _ := h.Store.List("iam", "attachment", ns)

	var list []AttachedPolicy
	for _, it := range items {
		var attr map[string]any
		if err := json.Unmarshal(it.Attributes, &attr); err != nil {
			continue
		}

		roleName, _ := attr["role"].(string)
		if roleName == role {
			policyName, _ := attr["policy"].(string)
			policyArn, _ := attr["policy_arn"].(string)
			if policyName != "" && policyArn != "" {
				list = append(list, AttachedPolicy{
					PolicyName: policyName,
					PolicyArn:  policyArn,
				})
			}
		}
	}

	resp := ListAttachedRolePoliciesResponse{
		ListAttachedRolePoliciesResult: ListAttachedRolePoliciesResult{
			AttachedPolicies: list,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// AttachUserPolicy
//

func (h *Handler) AttachUserPolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	userName := r.FormValue("UserName")
	if userName == "" {
		userName = r.URL.Query().Get("UserName")
	}
	r.ParseForm()
	arn := r.FormValue("PolicyArn")
	if arn == "" {
		arn = r.URL.Query().Get("PolicyArn")
	}

	if userName == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
	}
	if arn == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyArn is required", "")
	}

	name := arn[strings.LastIndex(arn, "/")+1:]
	id := "user:" + userName + ":" + name

	entry := map[string]any{
		"user":       userName,
		"policy":     name,
		"policy_arn": arn,
	}

	buf, _ := json.Marshal(entry)

	_ = h.Store.Create(&resource.Resource{
		ID:         id,
		Namespace:  ns,
		Service:    "iam",
		Type:       "user_attachment",
		Attributes: buf,
	})

	resp := AttachUserPolicyResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// DetachUserPolicy
//

func (h *Handler) DetachUserPolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	userName := r.FormValue("UserName")
	if userName == "" {
		userName = r.URL.Query().Get("UserName")
	}
	r.ParseForm()
	arn := r.FormValue("PolicyArn")
	if arn == "" {
		arn = r.URL.Query().Get("PolicyArn")
	}

	if userName == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
	}
	if arn == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "PolicyArn is required", "")
	}

	name := arn[strings.LastIndex(arn, "/")+1:]
	id := "user:" + userName + ":" + name

	_ = h.Store.Delete(id, "iam", "user_attachment", ns)

	resp := AttachUserPolicyResponse{
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}

//
// ListAttachedUserPolicies
//

func (h *Handler) ListAttachedUserPolicies(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)
	r.ParseForm()
	userName := r.FormValue("UserName")
	if userName == "" {
		userName = r.URL.Query().Get("UserName")
	}

	if userName == "" {
		awsresponses.WriteErrorXML(w, 400, "MissingParameter", "UserName is required", "")
	}

	items, _ := h.Store.List("iam", "user_attachment", ns)

	var list []AttachedPolicy
	for _, it := range items {
		var attr map[string]any
		if err := json.Unmarshal(it.Attributes, &attr); err != nil {
			continue
		}

		user, _ := attr["user"].(string)
		if user == userName {
			policyName, _ := attr["policy"].(string)
			policyArn, _ := attr["policy_arn"].(string)
			if policyName != "" && policyArn != "" {
				list = append(list, AttachedPolicy{
					PolicyName: policyName,
					PolicyArn:  policyArn,
				})
			}
		}
	}

	resp := ListAttachedUserPoliciesResponse{
		ListAttachedUserPoliciesResult: ListAttachedUserPoliciesResult{
			AttachedPolicies: list,
		},
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	}

	awsresponses.WriteXML(w, resp)
}
