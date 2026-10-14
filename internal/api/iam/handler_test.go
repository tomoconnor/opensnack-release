// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam_test

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"opensnack/internal/api/iam"
	"opensnack/internal/resource"
)

type client struct {
	t *testing.T
	h *iam.Handler
}

func newClient(t *testing.T) *client {
	return &client{t: t, h: iam.NewHandler(resource.NewMemStore())}
}

// call sends a form-encoded IAM request; kv alternates names and values.
func (c *client) call(action string, kv ...string) (int, string) {
	c.t.Helper()
	form := url.Values{"Action": {action}, "Version": {iam.APIVersion}}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()
	c.h.Dispatch(rec, req)
	return rec.Code, rec.Body.String()
}

func (c *client) ok(action string, kv ...string) string {
	c.t.Helper()
	code, body := c.call(action, kv...)
	if code != 200 {
		c.t.Fatalf("%s: %d %s", action, code, body)
	}
	return body
}

func (c *client) fails(code int, errCode, action string, kv ...string) {
	c.t.Helper()
	got, body := c.call(action, kv...)
	if got != code || !strings.Contains(body, "<Code>"+errCode+"</Code>") {
		c.t.Fatalf("%s: want %d %s, got %d %s", action, code, errCode, got, body)
	}
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in %s", w, body)
		}
	}
}

const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
const doc = `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::a+b/*"}]}`

func TestRoleRoundTrip(t *testing.T) {
	c := newClient(t)
	body := c.ok("CreateRole", "RoleName", "r1", "AssumeRolePolicyDocument", trust,
		"Description", "first", "Tags.member.1.Key", "team", "Tags.member.1.Value", "team")
	contains(t, body, "<CreateRoleResponse", "arn:aws:iam::000000000000:role/r1", "<RoleId>AROA")
	c.fails(409, "EntityAlreadyExists", "CreateRole", "RoleName", "r1", "AssumeRolePolicyDocument", trust)

	body = c.ok("GetRole", "RoleName", "r1")
	m := regexp.MustCompile(`<AssumeRolePolicyDocument>(.*)</AssumeRolePolicyDocument>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no document: %s", body)
	}
	if got, _ := url.QueryUnescape(m[1]); got != trust {
		t.Fatalf("document did not round-trip: %s", got)
	}
	contains(t, body, "<Description>first</Description>", "<MaxSessionDuration>3600</MaxSessionDuration>",
		"<Key>team</Key><Value>team</Value>")

	c.ok("UpdateRole", "RoleName", "r1", "Description", "second", "MaxSessionDuration", "7200")
	c.ok("UntagRole", "RoleName", "r1", "TagKeys.member.1", "team")
	c.ok("TagRole", "RoleName", "r1", "Tags.member.1.Key", "k", "Tags.member.1.Value", "v")
	contains(t, c.ok("GetRole", "RoleName", "r1"), "<Description>second</Description>",
		"<MaxSessionDuration>7200</MaxSessionDuration>", "<Key>k</Key>")
	contains(t, c.ok("ListRoleTags", "RoleName", "r1"), "<Key>k</Key><Value>v</Value>")
	c.fails(404, "NoSuchEntity", "GetRole", "RoleName", "ghost")
}

// The provider deletes a role only after detaching everything; DeleteRole
// itself refuses while anything is still attached.
func TestDeleteRoleConflicts(t *testing.T) {
	c := newClient(t)
	c.ok("CreateRole", "RoleName", "r", "AssumeRolePolicyDocument", trust)
	c.ok("CreatePolicy", "PolicyName", "p", "PolicyDocument", doc)
	pa := "arn:aws:iam::000000000000:policy/p"

	c.ok("AttachRolePolicy", "RoleName", "r", "PolicyArn", pa)
	contains(t, c.ok("ListAttachedRolePolicies", "RoleName", "r"), "<PolicyArn>"+pa+"</PolicyArn>")
	contains(t, c.ok("GetPolicy", "PolicyArn", pa), "<AttachmentCount>1</AttachmentCount>")
	c.fails(409, "DeleteConflict", "DeleteRole", "RoleName", "r")
	c.fails(409, "DeleteConflict", "DeletePolicy", "PolicyArn", pa)
	c.ok("DetachRolePolicy", "RoleName", "r", "PolicyArn", pa)
	c.fails(404, "NoSuchEntity", "DetachRolePolicy", "RoleName", "r", "PolicyArn", pa)

	c.ok("PutRolePolicy", "RoleName", "r", "PolicyName", "inline", "PolicyDocument", doc)
	contains(t, c.ok("ListRolePolicies", "RoleName", "r"), "<member>inline</member>")
	contains(t, c.ok("GetRolePolicy", "RoleName", "r", "PolicyName", "inline"),
		"<RoleName>r</RoleName>", "<PolicyDocument>"+strings.ReplaceAll(url.QueryEscape(doc), "+", "%20")+"</PolicyDocument>")
	c.fails(409, "DeleteConflict", "DeleteRole", "RoleName", "r")
	c.ok("DeleteRolePolicy", "RoleName", "r", "PolicyName", "inline")

	c.ok("CreateInstanceProfile", "InstanceProfileName", "ip")
	c.ok("AddRoleToInstanceProfile", "InstanceProfileName", "ip", "RoleName", "r")
	c.fails(409, "LimitExceeded", "AddRoleToInstanceProfile", "InstanceProfileName", "ip", "RoleName", "r")
	contains(t, c.ok("ListInstanceProfilesForRole", "RoleName", "r"), "<InstanceProfileName>ip</InstanceProfileName>")
	contains(t, c.ok("GetInstanceProfile", "InstanceProfileName", "ip"), "<RoleName>r</RoleName>",
		"arn:aws:iam::000000000000:instance-profile/ip")
	c.fails(409, "DeleteConflict", "DeleteRole", "RoleName", "r")
	c.fails(409, "DeleteConflict", "DeleteInstanceProfile", "InstanceProfileName", "ip")
	c.ok("RemoveRoleFromInstanceProfile", "InstanceProfileName", "ip", "RoleName", "r")
	c.ok("DeleteInstanceProfile", "InstanceProfileName", "ip")

	c.ok("DeleteRole", "RoleName", "r")
	c.ok("DeletePolicy", "PolicyArn", pa)
	c.fails(404, "NoSuchEntity", "GetRole", "RoleName", "r")
}

func TestPolicyVersions(t *testing.T) {
	c := newClient(t)
	body := c.ok("CreatePolicy", "PolicyName", "p", "Path", "/team/", "PolicyDocument", doc,
		"Description", "d", "Tags.member.1.Key", "a", "Tags.member.1.Value", "b")
	pa := "arn:aws:iam::000000000000:policy/team/p"
	contains(t, body, "<Arn>"+pa+"</Arn>", "<DefaultVersionId>v1</DefaultVersionId>", "<IsAttachable>true</IsAttachable>")
	c.fails(404, "NoSuchEntity", "GetPolicy", "PolicyArn", "arn:aws:iam::000000000000:policy/p")

	for i := 2; i <= 5; i++ {
		c.ok("CreatePolicyVersion", "PolicyArn", pa, "PolicyDocument", `{"v":`+string(rune('0'+i))+`}`, "SetAsDefault", "true")
	}
	c.fails(409, "LimitExceeded", "CreatePolicyVersion", "PolicyArn", pa, "PolicyDocument", `{}`)
	contains(t, c.ok("GetPolicy", "PolicyArn", pa), "<DefaultVersionId>v5</DefaultVersionId>", "<Key>a</Key>")
	contains(t, c.ok("GetPolicyVersion", "PolicyArn", pa, "VersionId", "v5"),
		"<Document>%7B%22v%22%3A5%7D</Document>", "<IsDefaultVersion>true</IsDefaultVersion>")
	c.fails(409, "DeleteConflict", "DeletePolicyVersion", "PolicyArn", pa, "VersionId", "v5")
	c.ok("DeletePolicyVersion", "PolicyArn", pa, "VersionId", "v1")
	c.fails(404, "NoSuchEntity", "GetPolicyVersion", "PolicyArn", pa, "VersionId", "v1")

	// Version IDs are never reused.
	contains(t, c.ok("CreatePolicyVersion", "PolicyArn", pa, "PolicyDocument", `{}`), "<VersionId>v6</VersionId>",
		"<IsDefaultVersion>false</IsDefaultVersion>")
	c.ok("SetDefaultPolicyVersion", "PolicyArn", pa, "VersionId", "v6")
	body = c.ok("ListPolicyVersions", "PolicyArn", pa)
	if n := strings.Count(body, "<VersionId>"); n != 5 {
		t.Fatalf("want 5 versions, got %d: %s", n, body)
	}
	contains(t, c.ok("ListPolicies", "Scope", "Local"), "<PolicyName>p</PolicyName>")

	c.fails(409, "DeleteConflict", "DeletePolicy", "PolicyArn", pa)
	for _, v := range []string{"v2", "v3", "v4", "v5"} {
		c.ok("DeletePolicyVersion", "PolicyArn", pa, "VersionId", v)
	}
	c.ok("DeletePolicy", "PolicyArn", pa)
	if body := c.ok("ListPolicies"); strings.Contains(body, "<PolicyName>") {
		t.Fatalf("policy still listed: %s", body)
	}
}

func TestUsersGroupsAndKeys(t *testing.T) {
	c := newClient(t)
	c.ok("CreateUser", "UserName", "u", "Tags.member.1.Key", "a", "Tags.member.1.Value", "b")
	c.ok("CreateGroup", "GroupName", "g", "Path", "/team/")
	c.ok("AddUserToGroup", "GroupName", "g", "UserName", "u")
	contains(t, c.ok("GetGroup", "GroupName", "g"), "arn:aws:iam::000000000000:group/team/g", "<UserName>u</UserName>")
	contains(t, c.ok("ListGroupsForUser", "UserName", "u"), "<GroupName>g</GroupName>")
	c.fails(409, "DeleteConflict", "DeleteGroup", "GroupName", "g")

	c.ok("PutGroupPolicy", "GroupName", "g", "PolicyName", "gp", "PolicyDocument", doc)
	c.ok("PutUserPolicy", "UserName", "u", "PolicyName", "up", "PolicyDocument", doc)
	contains(t, c.ok("GetUserPolicy", "UserName", "u", "PolicyName", "up"), "<UserName>u</UserName>")

	body := c.ok("CreateAccessKey", "UserName", "u")
	contains(t, body, "<AccessKeyId>AKIA", "<SecretAccessKey>", "<Status>Active</Status>")
	akid := regexp.MustCompile(`<AccessKeyId>(\w+)</AccessKeyId>`).FindStringSubmatch(body)[1]
	c.ok("UpdateAccessKey", "UserName", "u", "AccessKeyId", akid, "Status", "Inactive")
	body = c.ok("ListAccessKeys", "UserName", "u")
	contains(t, body, "<AccessKeyId>"+akid+"</AccessKeyId>", "<Status>Inactive</Status>")
	if strings.Contains(body, "SecretAccessKey") {
		t.Fatalf("ListAccessKeys leaked the secret: %s", body)
	}

	c.fails(409, "DeleteConflict", "DeleteUser", "UserName", "u")
	c.ok("DeleteAccessKey", "UserName", "u", "AccessKeyId", akid)
	c.ok("DeleteUserPolicy", "UserName", "u", "PolicyName", "up")
	c.fails(409, "DeleteConflict", "DeleteUser", "UserName", "u")
	c.ok("RemoveUserFromGroup", "GroupName", "g", "UserName", "u")
	c.ok("DeleteUser", "UserName", "u")
	c.fails(409, "DeleteConflict", "DeleteGroup", "GroupName", "g")
	c.ok("DeleteGroupPolicy", "GroupName", "g", "PolicyName", "gp")
	c.ok("DeleteGroup", "GroupName", "g")
}

func TestRenameUserMovesRelations(t *testing.T) {
	c := newClient(t)
	c.ok("CreateUser", "UserName", "old")
	c.ok("CreateGroup", "GroupName", "g")
	c.ok("AddUserToGroup", "GroupName", "g", "UserName", "old")
	c.ok("PutUserPolicy", "UserName", "old", "PolicyName", "up", "PolicyDocument", doc)
	c.ok("UpdateUser", "UserName", "old", "NewUserName", "new")
	c.fails(404, "NoSuchEntity", "GetUser", "UserName", "old")
	contains(t, c.ok("GetGroup", "GroupName", "g"), "<UserName>new</UserName>")
	c.ok("GetUserPolicy", "UserName", "new", "PolicyName", "up")
}

// Regression: these handlers wrote a 404 but carried on to dereference the nil
// resource, panicking the connection instead of returning NoSuchEntity.
func TestMissingPolicyDoesNotPanic(t *testing.T) {
	arn := "arn:aws:iam::000000000000:policy/ghost"
	c := newClient(t)
	c.fails(404, "NoSuchEntity", "GetPolicy", "PolicyArn", arn)
	c.fails(404, "NoSuchEntity", "GetPolicyVersion", "PolicyArn", arn, "VersionId", "v1")
	c.fails(404, "NoSuchEntity", "ListPolicyVersions", "PolicyArn", arn)
	c.fails(400, "InvalidAction", "Bogus")
}
