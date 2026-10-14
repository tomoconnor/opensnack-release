// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sts

import "encoding/xml"

type ResponseMetadata struct {
	XMLName   xml.Name `xml:"ResponseMetadata"`
	RequestId string   `xml:"RequestId"`
}

type GetCallerIdentityResult struct {
	XMLName xml.Name `xml:"GetCallerIdentityResult"`
	Arn     string   `xml:"Arn"`
	UserId  string   `xml:"UserId"`
	Account string   `xml:"Account"`
}

type Credentials struct {
	AccessKeyId     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

type AssumedRoleUser struct {
	AssumedRoleId string `xml:"AssumedRoleId"`
	Arn           string `xml:"Arn"`
}

type AssumeRoleResult struct {
	Credentials     Credentials     `xml:"Credentials"`
	AssumedRoleUser AssumedRoleUser `xml:"AssumedRoleUser"`
}

type AssumeRoleResponse struct {
	XMLName          xml.Name         `xml:"https://sts.amazonaws.com/doc/2011-06-15/ AssumeRoleResponse"`
	Result           AssumeRoleResult `xml:"AssumeRoleResult"`
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

type GetSessionTokenResult struct {
	Credentials Credentials `xml:"Credentials"`
}

type GetSessionTokenResponse struct {
	XMLName          xml.Name              `xml:"https://sts.amazonaws.com/doc/2011-06-15/ GetSessionTokenResponse"`
	Result           GetSessionTokenResult `xml:"GetSessionTokenResult"`
	ResponseMetadata ResponseMetadata      `xml:"ResponseMetadata"`
}

type GetCallerIdentityResponse struct {
	XMLName                 xml.Name                `xml:"GetCallerIdentityResponse"`
	GetCallerIdentityResult GetCallerIdentityResult `xml:"GetCallerIdentityResult"`
	ResponseMetadata        ResponseMetadata        `xml:"ResponseMetadata"`
}
