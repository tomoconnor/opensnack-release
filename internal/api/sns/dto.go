// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sns

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"opensnack/internal/awsresponses"
)

const queryXMLNS = "http://sns.amazonaws.com/doc/2010-03-31/"

type ResponseMetadata struct {
	RequestId string `xml:"RequestId"`
}

type queryResponse struct {
	XMLName          xml.Name
	Xmlns            string `xml:"xmlns,attr"`
	Result           any
	ResponseMetadata ResponseMetadata `xml:"ResponseMetadata"`
}

func writeQuery(w http.ResponseWriter, action string, result any) {
	awsresponses.WriteXML(w, queryResponse{
		XMLName:          xml.Name{Local: action + "Response"},
		Xmlns:            queryXMLNS,
		Result:           result,
		ResponseMetadata: ResponseMetadata{RequestId: awsresponses.NextRequestID()},
	})
}

// apiError is an SNS Query-protocol error.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func writeError(w http.ResponseWriter, e *apiError) {
	awsresponses.WriteErrorXML(w, e.Status, e.Code, e.Message, "")
}

func errInvalidParam(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "InvalidParameter", "Invalid parameter: " + msg}
}

func errNotFound(msg string) *apiError {
	return &apiError{http.StatusNotFound, "NotFound", msg}
}

func errInternal(err error) *apiError {
	return &apiError{http.StatusInternalServerError, "InternalError", err.Error()}
}

// form wraps the request's parameters (query string and body).
type form struct{ v url.Values }

func (f form) get(k string) string { return f.v.Get(k) }

func (f form) has(k string) bool { _, ok := f.v[k]; return ok }

// entries reads a Query map: prefix.entry.N.<key>/<val>.
func (f form) entries(prefix, key, val string) map[string]string {
	var out map[string]string
	for i := 1; ; i++ {
		p := prefix + ".entry." + strconv.Itoa(i) + "."
		if !f.has(p + key) {
			return out
		}
		if out == nil {
			out = map[string]string{}
		}
		out[f.get(p+key)] = f.get(p + val)
	}
}

// members reads a Query list: prefix.member.N.
func (f form) members(prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		k := prefix + ".member." + strconv.Itoa(i)
		if !f.has(k) {
			return out
		}
		out = append(out, f.get(k))
	}
}

func (f form) tags(prefix string) map[string]string {
	var out map[string]string
	for i := 1; ; i++ {
		p := prefix + ".member." + strconv.Itoa(i) + "."
		if !f.has(p + "Key") {
			return out
		}
		if out == nil {
			out = map[string]string{}
		}
		out[f.get(p+"Key")] = f.get(p + "Value")
	}
}

func (f form) messageAttributes(prefix string) (map[string]MessageAttribute, *apiError) {
	var out map[string]MessageAttribute
	for i := 1; ; i++ {
		p := prefix + ".entry." + strconv.Itoa(i) + "."
		if !f.has(p + "Name") {
			return out, nil
		}
		name := f.get(p + "Name")
		v := MessageAttribute{DataType: f.get(p + "Value.DataType"), StringValue: f.get(p + "Value.StringValue")}
		if b := f.get(p + "Value.BinaryValue"); b != "" {
			raw, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return nil, errInvalidParam("MessageAttributes: binary value of " + name + " is not base64")
			}
			v.BinaryValue = raw
		}
		if out == nil {
			out = map[string]MessageAttribute{}
		}
		out[name] = v
	}
}

type xmlEntry struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

func xmlEntries(m map[string]string) []xmlEntry {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]xmlEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, xmlEntry{Key: k, Value: m[k]})
	}
	return out
}

type xmlAttributes struct {
	Entry []xmlEntry `xml:"Attributes>entry"`
}

type xmlTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type xmlSubscription struct {
	SubscriptionArn string `xml:"SubscriptionArn"`
	Owner           string `xml:"Owner"`
	Protocol        string `xml:"Protocol"`
	Endpoint        string `xml:"Endpoint"`
	TopicArn        string `xml:"TopicArn"`
}
