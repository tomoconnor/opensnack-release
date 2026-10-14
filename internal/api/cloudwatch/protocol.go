// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudwatch

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	smithycbor "github.com/aws/smithy-go/encoding/cbor"

	"opensnack/internal/awsresponses"
)

// CloudWatch speaks three wire protocols, all with the same shapes:
//
//   - Smithy RPCv2 CBOR, POST /service/GraniteServiceVersion20100801/operation/{Op}.
//     aws-sdk-go-v2 (so Terraform provider v6) has used this since 2025.
//   - awsJson1_0, X-Amz-Target: GraniteServiceVersion20100801.{Op}.
//   - awsQuery, Action={Op}&Version=2010-08-01, which older SDKs and CLIs send.
//
// Requests are decoded into generic maps and responses written from them,
// so each operation is written once. In request maps numbers are float64 (or
// strings, from Query), and timestamps are time.Time (CBOR), epoch seconds
// (JSON) or ISO 8601 strings (Query); the accessors below accept all of them.

const (
	APIVersion   = "2010-08-01"
	targetPrefix = "GraniteServiceVersion20100801."
	cborPrefix   = "/service/GraniteServiceVersion20100801/operation/"
	xmlNS        = "http://monitoring.amazonaws.com/doc/2010-08-01/"
)

type protocol int

const (
	protoCBOR protocol = iota
	protoJSON
	protoQuery
)

// apiError is a CloudWatch error. Sender errors are 400, except the
// not-found ones.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func errInvalid(format string, args ...any) *apiError {
	return &apiError{http.StatusBadRequest, "InvalidParameterValue", fmt.Sprintf(format, args...)}
}

func errMissing(param string) *apiError {
	return &apiError{http.StatusBadRequest, "MissingParameter", "The parameter " + param + " is required."}
}

func errCombination(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "InvalidParameterCombination", msg}
}

func errNotFound(code, msg string) *apiError {
	return &apiError{http.StatusNotFound, code, msg}
}

type request struct {
	proto protocol
	op    string
	in    map[string]any
}

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(zr)
	}
	return body, nil
}

func parseRequest(r *http.Request) (*request, *apiError) {
	body, err := readBody(r)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "SerializationException", err.Error()}
	}
	switch {
	case strings.HasPrefix(r.URL.Path, cborPrefix):
		req := &request{proto: protoCBOR, op: strings.TrimPrefix(r.URL.Path, cborPrefix), in: map[string]any{}}
		if len(body) > 0 {
			v, err := smithycbor.Decode(body)
			if err != nil {
				return req, &apiError{http.StatusBadRequest, "SerializationException", err.Error()}
			}
			if m, ok := fromCBOR(v).(map[string]any); ok {
				req.in = m
			}
		}
		return req, nil
	case strings.HasPrefix(r.Header.Get("X-Amz-Target"), targetPrefix):
		req := &request{proto: protoJSON, op: strings.TrimPrefix(r.Header.Get("X-Amz-Target"), targetPrefix), in: map[string]any{}}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req.in); err != nil {
				return req, &apiError{http.StatusBadRequest, "SerializationException", err.Error()}
			}
		}
		return req, nil
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return &request{proto: protoQuery}, &apiError{http.StatusBadRequest, "MalformedQueryString", err.Error()}
	}
	for k, v := range r.URL.Query() {
		if _, ok := form[k]; !ok {
			form[k] = v
		}
	}
	req := &request{proto: protoQuery, op: form.Get("Action"), in: fromQuery(form)}
	return req, nil
}

func fromCBOR(v smithycbor.Value) any {
	switch x := v.(type) {
	case smithycbor.Map:
		m := make(map[string]any, len(x))
		for k, e := range x {
			if _, isNil := e.(*smithycbor.Nil); isNil {
				continue
			}
			m[k] = fromCBOR(e)
		}
		return m
	case smithycbor.List:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = fromCBOR(e)
		}
		return l
	case smithycbor.String:
		return string(x)
	case smithycbor.Uint:
		return float64(x)
	case smithycbor.NegInt:
		return -float64(x)
	case smithycbor.Float32:
		return float64(x)
	case smithycbor.Float64:
		return float64(x)
	case smithycbor.Bool:
		return bool(x)
	case smithycbor.Slice:
		return []byte(x)
	case *smithycbor.Tag:
		if t, err := smithycbor.AsTime(x); err == nil {
			return t
		}
		return fromCBOR(x.Value)
	}
	return nil
}

// fromQuery turns flattened Query parameters (Dimensions.member.1.Name=...)
// into nested maps and lists.
func fromQuery(form url.Values) map[string]any {
	root := map[string]any{}
	for key, vals := range form {
		if key == "Action" || key == "Version" || len(vals) == 0 {
			continue
		}
		parts := strings.Split(key, ".")
		cur := root
		for i, p := range parts {
			if i == len(parts)-1 {
				cur[p] = vals[0]
				break
			}
			next, ok := cur[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[p] = next
			}
			cur = next
		}
	}
	return fixQueryLists(root).(map[string]any)
}

func fixQueryLists(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if members, ok := m["member"].(map[string]any); ok && len(m) == 1 {
		type item struct {
			idx int
			v   any
		}
		var items []item
		for k, e := range members {
			n, _ := strconv.Atoi(k)
			items = append(items, item{n, fixQueryLists(e)})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].idx < items[j].idx })
		l := make([]any, len(items))
		for i, it := range items {
			l[i] = it.v
		}
		return l
	}
	for k, e := range m {
		m[k] = fixQueryLists(e)
	}
	return m
}

//
// Accessors over request maps
//

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func num(m map[string]any, k string) (float64, bool) {
	return toNum(m[k])
}

func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func boolean(m map[string]any, k string) (bool, bool) {
	switch v := m[k].(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(v)
		return b, err == nil
	}
	return false, false
}

func list(m map[string]any, k string) []any {
	l, _ := m[k].([]any)
	return l
}

func strList(m map[string]any, k string) []string {
	var out []string
	for _, v := range list(m, k) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func timestamp(m map[string]any, k string) (time.Time, bool) {
	switch v := m[k].(type) {
	case time.Time:
		return v, true
	case float64:
		sec, frac := math.Modf(v)
		return time.Unix(int64(sec), int64(frac*1e9)), true
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Unix(int64(f), 0), true
		}
	}
	return time.Time{}, false
}

//
// Responses
//

func writeResult(w http.ResponseWriter, req *request, out map[string]any) {
	if out == nil {
		out = map[string]any{}
	}
	w.Header().Set("X-Amzn-Requestid", awsresponses.NextRequestID())
	switch req.proto {
	case protoCBOR:
		w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
		w.Header().Set("Content-Type", "application/cbor")
		w.WriteHeader(http.StatusOK)
		w.Write(smithycbor.Encode(toCBOR(out)))
	case protoJSON:
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(toJSON(out))
	default:
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		var b strings.Builder
		b.WriteString(`<` + req.op + `Response xmlns="` + xmlNS + `">`)
		if len(out) > 0 {
			b.WriteString(`<` + req.op + `Result>`)
			writeXMLValue(&b, out)
			b.WriteString(`</` + req.op + `Result>`)
		}
		b.WriteString(`<ResponseMetadata><RequestId>` + awsresponses.NextRequestID() + `</RequestId></ResponseMetadata>`)
		b.WriteString(`</` + req.op + `Response>`)
		io.WriteString(w, b.String())
	}
}

func writeError(w http.ResponseWriter, proto protocol, e *apiError) {
	fault := "Sender"
	if e.status >= 500 {
		fault = "Receiver"
	}
	w.Header().Set("X-Amzn-Query-Error", e.code+";"+fault)
	switch proto {
	case protoCBOR:
		w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
		w.Header().Set("Content-Type", "application/cbor")
		w.WriteHeader(e.status)
		w.Write(smithycbor.Encode(smithycbor.Map{
			"__type":  smithycbor.String("com.amazonaws.cloudwatch#" + e.code),
			"message": smithycbor.String(e.message),
		}))
	case protoJSON:
		awsresponses.WriteJSON(w, e.status, map[string]any{"__type": e.code, "message": e.message})
	default:
		awsresponses.WriteErrorXML(w, e.status, e.code, e.message, "")
	}
}

func toCBOR(v any) smithycbor.Value {
	switch x := v.(type) {
	case map[string]any:
		m := smithycbor.Map{}
		for k, e := range x {
			if e == nil {
				continue
			}
			m[k] = toCBOR(e)
		}
		return m
	case []any:
		l := make(smithycbor.List, len(x))
		for i, e := range x {
			l[i] = toCBOR(e)
		}
		return l
	case []string:
		l := make(smithycbor.List, len(x))
		for i, e := range x {
			l[i] = smithycbor.String(e)
		}
		return l
	case []map[string]any:
		l := make(smithycbor.List, len(x))
		for i, e := range x {
			l[i] = toCBOR(e)
		}
		return l
	case []float64:
		l := make(smithycbor.List, len(x))
		for i, e := range x {
			l[i] = cborNumber(e)
		}
		return l
	case []time.Time:
		l := make(smithycbor.List, len(x))
		for i, e := range x {
			l[i] = toCBOR(e)
		}
		return l
	case string:
		return smithycbor.String(x)
	case bool:
		return smithycbor.Bool(x)
	case float64:
		return cborNumber(x)
	case int:
		return cborNumber(float64(x))
	case int64:
		return cborNumber(float64(x))
	case time.Time:
		return &smithycbor.Tag{ID: 1, Value: smithycbor.Float64(float64(x.UnixMilli()) / 1e3)}
	}
	return &smithycbor.Nil{}
}

// cborNumber writes whole numbers as CBOR integers: the SDK reads integer
// members only from integer types, and double members from either.
func cborNumber(f float64) smithycbor.Value {
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		if f >= 0 {
			return smithycbor.Uint(uint64(f))
		}
		return smithycbor.NegInt(uint64(-f))
	}
	return smithycbor.Float64(f)
}

func toJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			if e != nil {
				m[k] = toJSON(e)
			}
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = toJSON(e)
		}
		return l
	case []map[string]any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = toJSON(e)
		}
		return l
	case []time.Time:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = toJSON(e)
		}
		return l
	case time.Time:
		return float64(x.UnixMilli()) / 1e3
	}
	return v
}

func writeXMLValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if x[k] == nil {
				continue
			}
			b.WriteString("<" + k + ">")
			writeXMLValue(b, x[k])
			b.WriteString("</" + k + ">")
		}
	case []any:
		for _, e := range x {
			b.WriteString("<member>")
			writeXMLValue(b, e)
			b.WriteString("</member>")
		}
	case []string:
		for _, e := range x {
			b.WriteString("<member>")
			writeXMLValue(b, e)
			b.WriteString("</member>")
		}
	case []map[string]any:
		for _, e := range x {
			b.WriteString("<member>")
			writeXMLValue(b, e)
			b.WriteString("</member>")
		}
	case []float64:
		for _, e := range x {
			b.WriteString("<member>")
			writeXMLValue(b, e)
			b.WriteString("</member>")
		}
	case []time.Time:
		for _, e := range x {
			b.WriteString("<member>")
			writeXMLValue(b, e)
			b.WriteString("</member>")
		}
	case string:
		xml.EscapeText(stringWriter{b}, []byte(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case time.Time:
		b.WriteString(x.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
}

type stringWriter struct{ b *strings.Builder }

func (s stringWriter) Write(p []byte) (int, error) { return s.b.Write(p) }
