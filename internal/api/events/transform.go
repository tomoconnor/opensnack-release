// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// InputTransformer is the target's InputTransformer block.
type InputTransformer struct {
	InputPathsMap map[string]string `json:"InputPathsMap,omitempty"`
	InputTemplate string            `json:"InputTemplate"`
}

// TransformContext is what the reserved <aws.events.*> variables expand to.
type TransformContext struct {
	RuleArn       string
	RuleName      string
	IngestionTime string
}

// targetPayload works out what a target receives for an event: Input
// verbatim, InputPath's slice of the event, InputTransformer's rendered
// template, or else the whole event. Slices keep the event's own bytes, so
// key order and number formatting survive.
func targetPayload(t Target, event []byte, tc TransformContext) ([]byte, error) {
	switch {
	case t.Input != nil:
		return []byte(*t.Input), nil
	case t.InputPath != nil && *t.InputPath != "":
		v, ok, err := jsonPath(event, *t.InputPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []byte("null"), nil
		}
		return compact(v), nil
	case t.InputTransformer != nil:
		return applyTransformer(*t.InputTransformer, event, tc)
	}
	return event, nil
}

func compact(raw []byte) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return raw
	}
	return buf.Bytes()
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// applyTransformer renders InputTemplate. A placeholder inside a JSON string
// gets the value's text; outside one it gets the value as JSON, so
// {"state": <state>} yields {"state": "running"}.
func applyTransformer(it InputTransformer, event []byte, tc TransformContext) ([]byte, error) {
	vars := map[string]json.RawMessage{}
	for name, path := range it.InputPathsMap {
		v, ok, err := jsonPath(event, path)
		if err != nil {
			return nil, err
		}
		if !ok {
			v = json.RawMessage("null")
		}
		vars[name] = compact(v)
	}
	str := func(s string) json.RawMessage { b, _ := marshalNoEscape(s); return b }
	reserved := map[string]json.RawMessage{
		"aws.events.rule-arn":             str(tc.RuleArn),
		"aws.events.rule-name":            str(tc.RuleName),
		"aws.events.event.ingestion-time": str(tc.IngestionTime),
		"aws.events.event":                compact(event),
		"aws.events.event.json":           compact(event),
	}

	tpl := it.InputTemplate
	var out strings.Builder
	inString := false
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		switch {
		case c == '\\' && inString && i+1 < len(tpl):
			out.WriteByte(c)
			out.WriteByte(tpl[i+1])
			i++
			continue
		case c == '"':
			inString = !inString
		case c == '<':
			end := strings.IndexByte(tpl[i+1:], '>')
			if end > 0 {
				name := tpl[i+1 : i+1+end]
				v, ok := vars[name]
				if !ok {
					v, ok = reserved[name]
				}
				if ok {
					out.WriteString(renderVar(v, inString))
					i += end + 1
					continue
				}
			}
		}
		out.WriteByte(c)
	}
	return []byte(out.String()), nil
}

// renderVar renders a raw JSON value for the template. Inside quotes a
// string contributes its text and anything else its JSON, escaped.
func renderVar(v json.RawMessage, inString bool) string {
	if !inString {
		return string(v)
	}
	text := string(v)
	if len(v) > 0 && v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			text = s
		}
	}
	q, _ := marshalNoEscape(text)
	return string(q[1 : len(q)-1])
}

var errBadPath = errors.New("invalid JSON path")

type pathStep struct {
	key   string
	index int // -1 for a key step
}

// parsePath parses the simple JSONPath EventBridge accepts: $, $.a.b,
// $.a[0], $['a'].
func parsePath(path string) ([]pathStep, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, errBadPath
	}
	var steps []pathStep
	p := path[1:]
	for p != "" {
		switch {
		case p[0] == '.':
			p = p[1:]
			end := strings.IndexAny(p, ".[")
			if end < 0 {
				end = len(p)
			}
			if end == 0 {
				return nil, errBadPath
			}
			steps = append(steps, pathStep{key: p[:end], index: -1})
			p = p[end:]
		case strings.HasPrefix(p, "['") || strings.HasPrefix(p, `["`):
			q := p[1]
			end := strings.IndexByte(p[2:], q)
			if end < 0 || !strings.HasPrefix(p[2+end:], string(q)+"]") {
				return nil, errBadPath
			}
			steps = append(steps, pathStep{key: p[2 : 2+end], index: -1})
			p = p[2+end+2:]
		case p[0] == '[':
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return nil, errBadPath
			}
			n, err := strconv.Atoi(p[1:end])
			if err != nil || n < 0 {
				return nil, errBadPath
			}
			steps = append(steps, pathStep{index: n})
			p = p[end+1:]
		default:
			return nil, errBadPath
		}
	}
	return steps, nil
}

// jsonPath returns the raw JSON at path in doc, and whether it exists.
func jsonPath(doc []byte, path string) (json.RawMessage, bool, error) {
	steps, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}
	cur := json.RawMessage(doc)
	for _, s := range steps {
		if s.index >= 0 {
			var arr []json.RawMessage
			if json.Unmarshal(cur, &arr) != nil || s.index >= len(arr) {
				return nil, false, nil
			}
			cur = arr[s.index]
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(cur, &obj) != nil {
			return nil, false, nil
		}
		next, ok := obj[s.key]
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return cur, true, nil
}
