// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package iam

// Wire shapes for the IAM Query API. Results are written inside
// <{Action}Response><{Action}Result> by writeResult, so none of these carry
// an XMLName.

type Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type PermissionsBoundary struct {
	PermissionsBoundaryType string `xml:"PermissionsBoundaryType"`
	PermissionsBoundaryArn  string `xml:"PermissionsBoundaryArn"`
}

type Role struct {
	Path                     string               `xml:"Path"`
	RoleName                 string               `xml:"RoleName"`
	RoleId                   string               `xml:"RoleId"`
	Arn                      string               `xml:"Arn"`
	CreateDate               string               `xml:"CreateDate"`
	AssumeRolePolicyDocument string               `xml:"AssumeRolePolicyDocument"`
	Description              string               `xml:"Description,omitempty"`
	MaxSessionDuration       int                  `xml:"MaxSessionDuration"`
	PermissionsBoundary      *PermissionsBoundary `xml:"PermissionsBoundary,omitempty"`
	Tags                     []Tag                `xml:"Tags>member,omitempty"`
}

type User struct {
	Path                string               `xml:"Path"`
	UserName            string               `xml:"UserName"`
	UserId              string               `xml:"UserId"`
	Arn                 string               `xml:"Arn"`
	CreateDate          string               `xml:"CreateDate"`
	PermissionsBoundary *PermissionsBoundary `xml:"PermissionsBoundary,omitempty"`
	Tags                []Tag                `xml:"Tags>member,omitempty"`
}

type Group struct {
	Path       string `xml:"Path"`
	GroupName  string `xml:"GroupName"`
	GroupId    string `xml:"GroupId"`
	Arn        string `xml:"Arn"`
	CreateDate string `xml:"CreateDate"`
}

type Policy struct {
	PolicyName                    string `xml:"PolicyName"`
	PolicyId                      string `xml:"PolicyId"`
	Arn                           string `xml:"Arn"`
	Path                          string `xml:"Path"`
	DefaultVersionId              string `xml:"DefaultVersionId"`
	AttachmentCount               int    `xml:"AttachmentCount"`
	PermissionsBoundaryUsageCount int    `xml:"PermissionsBoundaryUsageCount"`
	IsAttachable                  bool   `xml:"IsAttachable"`
	Description                   string `xml:"Description,omitempty"`
	CreateDate                    string `xml:"CreateDate"`
	UpdateDate                    string `xml:"UpdateDate"`
	Tags                          []Tag  `xml:"Tags>member,omitempty"`
}

type PolicyVersion struct {
	Document         string `xml:"Document,omitempty"`
	VersionId        string `xml:"VersionId"`
	IsDefaultVersion bool   `xml:"IsDefaultVersion"`
	CreateDate       string `xml:"CreateDate"`
}

type AttachedPolicy struct {
	PolicyName string `xml:"PolicyName"`
	PolicyArn  string `xml:"PolicyArn"`
}

type InstanceProfile struct {
	Path                string `xml:"Path"`
	InstanceProfileName string `xml:"InstanceProfileName"`
	InstanceProfileId   string `xml:"InstanceProfileId"`
	Arn                 string `xml:"Arn"`
	CreateDate          string `xml:"CreateDate"`
	Roles               []Role `xml:"Roles>member"`
	Tags                []Tag  `xml:"Tags>member,omitempty"`
}

type AccessKey struct {
	UserName        string `xml:"UserName"`
	AccessKeyId     string `xml:"AccessKeyId"`
	Status          string `xml:"Status"`
	SecretAccessKey string `xml:"SecretAccessKey,omitempty"`
	CreateDate      string `xml:"CreateDate"`
}

type PolicyRole struct {
	RoleName string `xml:"RoleName"`
	RoleId   string `xml:"RoleId"`
}

type PolicyUser struct {
	UserName string `xml:"UserName"`
	UserId   string `xml:"UserId"`
}

type PolicyGroup struct {
	GroupName string `xml:"GroupName"`
	GroupId   string `xml:"GroupId"`
}
