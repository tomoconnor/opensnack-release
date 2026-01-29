// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

import "encoding/xml"

//
// COMMON
//

type ResponseMetadata struct {
	XMLName   xml.Name `xml:"ResponseMetadata"`
	RequestId string   `xml:"RequestId"`
}

//
// CreateRole
//

type CreateRoleResult struct {
	XMLName xml.Name `xml:"CreateRoleResult"`
	Role    Role     `xml:"Role"`
}

type CreateRoleResponse struct {
	XMLName          xml.Name         `xml:"CreateRoleResponse"`
	CreateRoleResult CreateRoleResult `xml:"CreateRoleResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// Role structure
//

type Role struct {
	XMLName          xml.Name `xml:"Role"`
	Path             string   `xml:"Path"`
	RoleName         string   `xml:"RoleName"`
	Arn              string   `xml:"Arn"`
	AssumeRolePolicy string   `xml:"AssumeRolePolicyDocument"`
	CreateDate       string   `xml:"CreateDate"`
}

type User struct {
	XMLName    xml.Name `xml:"User"`
	Path       string   `xml:"Path"`
	UserName   string   `xml:"UserName"`
	UserId     string   `xml:"UserId"`
	Arn        string   `xml:"Arn"`
	CreateDate string   `xml:"CreateDate"`
}

//
// GetUser
//

type GetUserResult struct {
	XMLName xml.Name `xml:"GetUserResult"`
	User    User     `xml:"User"`
}

type GetUserResponse struct {
	XMLName          xml.Name         `xml:"GetUserResponse"`
	GetUserResult    GetUserResult    `xml:"GetUserResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// CreateUser
//

type CreateUserResult struct {
	XMLName xml.Name `xml:"CreateUserResult"`
	User    User     `xml:"User"`
}

type CreateUserResponse struct {
	XMLName            xml.Name           `xml:"CreateUserResponse"`
	CreateUserResult   CreateUserResult   `xml:"CreateUserResult"`
	ResponseMetadata   ResponseMetadata   `xml:"ResponseMetadata"`
}

//
// UpdateUser
//

type UpdateUserResponse struct {
	XMLName          xml.Name         `xml:"UpdateUserResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// GetRole
//

type GetRoleResult struct {
	XMLName xml.Name `xml:"GetRoleResult"`
	Role    Role     `xml:"Role"`
}

type GetRoleResponse struct {
	XMLName          xml.Name         `xml:"GetRoleResponse"`
	GetRoleResult    GetRoleResult    `xml:"GetRoleResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// CreatePolicy
//

type CreatePolicyResult struct {
	XMLName xml.Name `xml:"CreatePolicyResult"`
	Policy  Policy   `xml:"Policy"`
}

type CreatePolicyResponse struct {
	XMLName            xml.Name           `xml:"CreatePolicyResponse"`
	CreatePolicyResult CreatePolicyResult `xml:"CreatePolicyResult"`
	ResponseMetadata   ResponseMetadata   `xml:"ResponseMetadata"`
}

//
// Policy (single version v1)
//

type Policy struct {
	XMLName          xml.Name `xml:"Policy"`
	PolicyName       string   `xml:"PolicyName"`
	PolicyId         string   `xml:"PolicyId"`
	Arn              string   `xml:"Arn"`
	Path             string   `xml:"Path"`
	DefaultVersionId string   `xml:"DefaultVersionId"`
	CreateDate       string   `xml:"CreateDate"`
	UpdateDate       string   `xml:"UpdateDate"`
	AttachmentCount  int      `xml:"AttachmentCount"`
}

//
// GetPolicy
//

type GetPolicyResult struct {
	XMLName xml.Name `xml:"GetPolicyResult"`
	Policy  Policy   `xml:"Policy"`
}

type GetPolicyResponse struct {
	XMLName          xml.Name         `xml:"GetPolicyResponse"`
	GetPolicyResult  GetPolicyResult  `xml:"GetPolicyResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// GetPolicyVersion
//

type PolicyVersion struct {
	XMLName          xml.Name `xml:"PolicyVersion"`
	VersionId        string   `xml:"VersionId"`
	IsDefaultVersion bool     `xml:"IsDefaultVersion"`
	Document         string   `xml:"Document"`
}

type GetPolicyVersionResult struct {
	XMLName       xml.Name      `xml:"GetPolicyVersionResult"`
	PolicyVersion PolicyVersion `xml:"PolicyVersion"`
}

type GetPolicyVersionResponse struct {
	XMLName                xml.Name               `xml:"GetPolicyVersionResponse"`
	GetPolicyVersionResult GetPolicyVersionResult `xml:"GetPolicyVersionResult"`
	ResponseMetadata       ResponseMetadata       `xml:"ResponseMetadata"`
}

//
// ListPolicyVersions
//

type ListPolicyVersionsResult struct {
	XMLName       xml.Name        `xml:"ListPolicyVersionsResult"`
	Versions      []PolicyVersion `xml:"Versions>member"`
	IsTruncated   bool            `xml:"IsTruncated"`
	Marker        string          `xml:"Marker,omitempty"`
}

type ListPolicyVersionsResponse struct {
	XMLName                  xml.Name                 `xml:"ListPolicyVersionsResponse"`
	ListPolicyVersionsResult ListPolicyVersionsResult `xml:"ListPolicyVersionsResult"`
	ResponseMetadata         ResponseMetadata         `xml:"ResponseMetadata"`
}

//
// AttachRolePolicy
//

type AttachRolePolicyResponse struct {
	XMLName          xml.Name         `xml:"AttachRolePolicyResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// ListAttachedRolePolicies
//

type AttachedPolicy struct {
	XMLName    xml.Name `xml:"member"`
	PolicyName string   `xml:"PolicyName"`
	PolicyArn  string   `xml:"PolicyArn"`
}

type ListAttachedRolePoliciesResult struct {
	XMLName          xml.Name         `xml:"ListAttachedRolePoliciesResult"`
	AttachedPolicies []AttachedPolicy `xml:"AttachedPolicies>member"`
}

type ListAttachedRolePoliciesResponse struct {
	XMLName                        xml.Name                       `xml:"ListAttachedRolePoliciesResponse"`
	ListAttachedRolePoliciesResult ListAttachedRolePoliciesResult `xml:"ListAttachedRolePoliciesResult"`
	ResponseMetadata               ResponseMetadata               `xml:"ResponseMetadata"`
}

//
// AttachUserPolicy
//

type AttachUserPolicyResponse struct {
	XMLName          xml.Name         `xml:"AttachUserPolicyResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// ListAttachedUserPolicies
//

type ListAttachedUserPoliciesResult struct {
	XMLName          xml.Name         `xml:"ListAttachedUserPoliciesResult"`
	AttachedPolicies []AttachedPolicy `xml:"AttachedPolicies>member"`
}

type ListAttachedUserPoliciesResponse struct {
	XMLName                        xml.Name                       `xml:"ListAttachedUserPoliciesResponse"`
	ListAttachedUserPoliciesResult ListAttachedUserPoliciesResult `xml:"ListAttachedUserPoliciesResult"`
	ResponseMetadata               ResponseMetadata               `xml:"ResponseMetadata"`
}

//
// DeleteUser
//

type DeleteUserResponse struct {
	XMLName          xml.Name         `xml:"DeleteUserResponse"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// ListUsers
//

type ListUsersResult struct {
	XMLName     xml.Name `xml:"ListUsersResult"`
	Users       []User   `xml:"Users>member"`
	IsTruncated bool     `xml:"IsTruncated"`
	Marker      string   `xml:"Marker,omitempty"`
}

type ListUsersResponse struct {
	XMLName          xml.Name         `xml:"ListUsersResponse"`
	ListUsersResult  ListUsersResult  `xml:"ListUsersResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

//
// ListRoles
//

type ListRolesResult struct {
	XMLName     xml.Name `xml:"ListRolesResult"`
	Roles       []Role   `xml:"Roles>member"`
	IsTruncated bool     `xml:"IsTruncated"`
	Marker      string   `xml:"Marker,omitempty"`
}

type ListRolesResponse struct {
	XMLName          xml.Name         `xml:"ListRolesResponse"`
	ListRolesResult  ListRolesResult  `xml:"ListRolesResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}
