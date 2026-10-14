// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"opensnack/internal/awsresponses"
)

// The Query protocol: form parameters in, XML out.

const queryXMLNS = "http://queue.amazonaws.com/doc/2012-11-05/"

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

func writeQueryError(w http.ResponseWriter, e *apiError) {
	awsresponses.WriteErrorXML(w, e.Status, e.QueryCode, e.Message, "")
}

// form wraps the request's parameters (query string and body).
type form struct{ v url.Values }

func (f form) get(k string) string { return f.v.Get(k) }

func (f form) has(k string) bool { _, ok := f.v[k]; return ok }

func (f form) intp(k string) (*int, *apiError) {
	if !f.has(k) {
		return nil, nil
	}
	n, err := strconv.Atoi(f.get(k))
	if err != nil {
		return nil, errInvalidParam("Value %s for parameter %s is invalid.", f.get(k), k)
	}
	return &n, nil
}

// list reads prefix.1, prefix.2, ... until the first gap.
func (f form) list(prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		k := prefix + "." + strconv.Itoa(i)
		if !f.has(k) {
			return out
		}
		out = append(out, f.get(k))
	}
}

// pairs reads prefix.N.<key>/prefix.N.<val> into a map.
func (f form) pairs(prefix, key, val string) map[string]string {
	var out map[string]string
	for i := 1; ; i++ {
		p := prefix + "." + strconv.Itoa(i) + "."
		if !f.has(p + key) {
			return out
		}
		if out == nil {
			out = map[string]string{}
		}
		out[f.get(p+key)] = f.get(p + val)
	}
}

func (f form) messageAttributes(prefix string) (map[string]MessageAttributeValue, *apiError) {
	var out map[string]MessageAttributeValue
	for i := 1; ; i++ {
		p := prefix + "." + strconv.Itoa(i) + "."
		if !f.has(p + "Name") {
			return out, nil
		}
		v := MessageAttributeValue{
			DataType:    f.get(p + "Value.DataType"),
			StringValue: f.get(p + "Value.StringValue"),
		}
		if b := f.get(p + "Value.BinaryValue"); b != "" {
			raw, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return nil, errInvalidParam("Message attribute %s has an invalid binary value.", f.get(p+"Name"))
			}
			v.BinaryValue = raw
		}
		if out == nil {
			out = map[string]MessageAttributeValue{}
		}
		out[f.get(p+"Name")] = v
	}
}

type nameValue struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

func nameValues(m map[string]string) []nameValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]nameValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, nameValue{Name: k, Value: m[k]})
	}
	return out
}

type xmlBatchError struct {
	Id          string `xml:"Id"`
	SenderFault bool   `xml:"SenderFault"`
	Code        string `xml:"Code"`
	Message     string `xml:"Message,omitempty"`
}

func xmlBatchErrors(in []batchError) []xmlBatchError {
	out := make([]xmlBatchError, len(in))
	for i, e := range in {
		out[i] = xmlBatchError(e)
	}
	return out
}

type xmlMessageAttribute struct {
	Name  string `xml:"Name"`
	Value struct {
		StringValue string `xml:"StringValue,omitempty"`
		BinaryValue string `xml:"BinaryValue,omitempty"`
		DataType    string `xml:"DataType"`
	} `xml:"Value"`
}

type xmlMessage struct {
	MessageId              string                `xml:"MessageId"`
	ReceiptHandle          string                `xml:"ReceiptHandle"`
	MD5OfBody              string                `xml:"MD5OfBody"`
	Body                   string                `xml:"Body"`
	Attribute              []nameValue           `xml:"Attribute"`
	MD5OfMessageAttributes string                `xml:"MD5OfMessageAttributes,omitempty"`
	MessageAttribute       []xmlMessageAttribute `xml:"MessageAttribute"`
}

type xmlSendResult struct {
	Id                     string `xml:"Id,omitempty"`
	MessageId              string `xml:"MessageId"`
	MD5OfMessageBody       string `xml:"MD5OfMessageBody"`
	MD5OfMessageAttributes string `xml:"MD5OfMessageAttributes,omitempty"`
	SequenceNumber         string `xml:"SequenceNumber,omitempty"`
}

func toXMLSend(id string, o *sendMessageOutput) xmlSendResult {
	return xmlSendResult{Id: id, MessageId: o.MessageId, MD5OfMessageBody: o.MD5OfMessageBody, MD5OfMessageAttributes: o.MD5OfMessageAttributes, SequenceNumber: o.SequenceNumber}
}

func (h *Handler) dispatchQuery(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f := form{r.Form}
	action := f.get("Action")
	// Query clients post to the queue URL and may leave QueueUrl out.
	queueURL := f.get("QueueUrl")
	if queueURL == "" && len(strings.Split(strings.Trim(r.URL.Path, "/"), "/")) == 2 {
		queueURL = r.URL.Path
	}

	fail := func(e *apiError) { writeQueryError(w, e) }

	switch action {
	case "CreateQueue":
		out, aerr := h.createQueue(r, &createQueueInput{
			QueueName:  f.get("QueueName"),
			Attributes: f.pairs("Attribute", "Name", "Value"),
			Tags:       f.pairs("Tag", "Key", "Value"),
		})
		if aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, struct {
			XMLName  xml.Name `xml:"CreateQueueResult"`
			QueueUrl string   `xml:"QueueUrl"`
		}{QueueUrl: out.QueueUrl})

	case "GetQueueUrl":
		out, aerr := h.getQueueURL(r, &queueNameInput{QueueName: f.get("QueueName")})
		if aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, struct {
			XMLName  xml.Name `xml:"GetQueueUrlResult"`
			QueueUrl string   `xml:"QueueUrl"`
		}{QueueUrl: out.QueueUrl})

	case "ListQueues", "ListDeadLetterSourceQueues":
		max, aerr := f.intp("MaxResults")
		if aerr != nil {
			fail(aerr)
			return
		}
		var urls []string
		var next string
		if action == "ListQueues" {
			out, aerr := h.listQueues(r, &listQueuesInput{QueueNamePrefix: f.get("QueueNamePrefix"), MaxResults: max, NextToken: f.get("NextToken")})
			if aerr != nil {
				fail(aerr)
				return
			}
			urls, next = out.QueueUrls, out.NextToken
		} else {
			out, aerr := h.listDeadLetterSourceQueues(r, &listDLQSourcesInput{QueueUrl: queueURL, MaxResults: max, NextToken: f.get("NextToken")})
			if aerr != nil {
				fail(aerr)
				return
			}
			urls, next = out.QueueUrls, out.NextToken
		}
		writeQuery(w, action, struct {
			XMLName   xml.Name
			QueueUrl  []string `xml:"QueueUrl"`
			NextToken string   `xml:"NextToken,omitempty"`
		}{XMLName: xml.Name{Local: action + "Result"}, QueueUrl: urls, NextToken: next})

	case "DeleteQueue":
		if _, aerr := h.deleteQueue(r, &queueURLInput{QueueUrl: queueURL}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "GetQueueAttributes":
		out, aerr := h.getQueueAttributes(r, &getQueueAttributesInput{QueueUrl: queueURL, AttributeNames: f.list("AttributeName")})
		if aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, struct {
			XMLName   xml.Name    `xml:"GetQueueAttributesResult"`
			Attribute []nameValue `xml:"Attribute"`
		}{Attribute: nameValues(out.Attributes)})

	case "SetQueueAttributes":
		if _, aerr := h.setQueueAttributes(r, &setQueueAttributesInput{QueueUrl: queueURL, Attributes: f.pairs("Attribute", "Name", "Value")}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "TagQueue":
		if _, aerr := h.tagQueue(r, &tagQueueInput{QueueUrl: queueURL, Tags: f.pairs("Tag", "Key", "Value")}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "UntagQueue":
		if _, aerr := h.untagQueue(r, &untagQueueInput{QueueUrl: queueURL, TagKeys: f.list("TagKey")}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "ListQueueTags":
		out, aerr := h.listQueueTags(r, &queueURLInput{QueueUrl: queueURL})
		if aerr != nil {
			fail(aerr)
			return
		}
		type tag struct {
			Key   string `xml:"Key"`
			Value string `xml:"Value"`
		}
		var tags []tag
		for _, nv := range nameValues(out.Tags) {
			tags = append(tags, tag{Key: nv.Name, Value: nv.Value})
		}
		writeQuery(w, action, struct {
			XMLName xml.Name `xml:"ListQueueTagsResult"`
			Tag     []tag    `xml:"Tag"`
		}{Tag: tags})

	case "SendMessage":
		in, aerr := querySendInput(f, "")
		if aerr != nil {
			fail(aerr)
			return
		}
		in.QueueUrl = queueURL
		out, aerr := h.sendMessage(r, in)
		if aerr != nil {
			fail(aerr)
			return
		}
		res := toXMLSend("", out)
		writeQuery(w, action, struct {
			XMLName xml.Name `xml:"SendMessageResult"`
			xmlSendResult
		}{xmlSendResult: res})

	case "SendMessageBatch":
		in := &sendMessageBatchInput{QueueUrl: queueURL}
		for i := 1; ; i++ {
			p := "SendMessageBatchRequestEntry." + strconv.Itoa(i) + "."
			if !f.has(p + "Id") {
				break
			}
			e, aerr := querySendInput(f, p)
			if aerr != nil {
				fail(aerr)
				return
			}
			in.Entries = append(in.Entries, sendBatchEntry{Id: f.get(p + "Id"), sendMessageInput: *e})
		}
		out, aerr := h.sendMessageBatch(r, in)
		if aerr != nil {
			fail(aerr)
			return
		}
		res := struct {
			XMLName xml.Name        `xml:"SendMessageBatchResult"`
			OK      []xmlSendResult `xml:"SendMessageBatchResultEntry"`
			Failed  []xmlBatchError `xml:"BatchResultErrorEntry"`
		}{Failed: xmlBatchErrors(out.Failed)}
		for _, s := range out.Successful {
			res.OK = append(res.OK, toXMLSend(s.Id, &s.sendMessageOutput))
		}
		writeQuery(w, action, res)

	case "ReceiveMessage":
		in := &receiveMessageInput{
			QueueUrl:                    queueURL,
			AttributeNames:              f.list("AttributeName"),
			MessageSystemAttributeNames: f.list("MessageSystemAttributeName"),
			MessageAttributeNames:       f.list("MessageAttributeName"),
		}
		var aerr *apiError
		for k, p := range map[string]**int{"MaxNumberOfMessages": &in.MaxNumberOfMessages, "VisibilityTimeout": &in.VisibilityTimeout, "WaitTimeSeconds": &in.WaitTimeSeconds} {
			if *p, aerr = f.intp(k); aerr != nil {
				fail(aerr)
				return
			}
		}
		out, aerr := h.receiveMessage(r, in)
		if aerr != nil {
			fail(aerr)
			return
		}
		res := struct {
			XMLName xml.Name     `xml:"ReceiveMessageResult"`
			Message []xmlMessage `xml:"Message"`
		}{}
		for _, m := range out.Messages {
			xm := xmlMessage{
				MessageId: m.MessageId, ReceiptHandle: m.ReceiptHandle, MD5OfBody: m.MD5OfBody, Body: m.Body,
				Attribute: nameValues(m.Attributes), MD5OfMessageAttributes: m.MD5OfMessageAttributes,
			}
			names := make([]string, 0, len(m.MessageAttributes))
			for n := range m.MessageAttributes {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				v := m.MessageAttributes[n]
				a := xmlMessageAttribute{Name: n}
				a.Value.DataType = v.DataType
				a.Value.StringValue = v.StringValue
				if len(v.BinaryValue) > 0 {
					a.Value.BinaryValue = base64.StdEncoding.EncodeToString(v.BinaryValue)
				}
				xm.MessageAttribute = append(xm.MessageAttribute, a)
			}
			res.Message = append(res.Message, xm)
		}
		writeQuery(w, action, res)

	case "DeleteMessage":
		if _, aerr := h.deleteMessage(r, &deleteMessageInput{QueueUrl: queueURL, ReceiptHandle: f.get("ReceiptHandle")}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "ChangeMessageVisibility":
		vt, aerr := f.intp("VisibilityTimeout")
		if aerr != nil {
			fail(aerr)
			return
		}
		if _, aerr := h.changeMessageVisibility(r, &changeVisibilityInput{QueueUrl: queueURL, ReceiptHandle: f.get("ReceiptHandle"), VisibilityTimeout: vt}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	case "DeleteMessageBatch", "ChangeMessageVisibilityBatch":
		in := &handleBatchInput{QueueUrl: queueURL}
		for i := 1; ; i++ {
			p := action + "RequestEntry." + strconv.Itoa(i) + "."
			if !f.has(p + "Id") {
				break
			}
			vt, aerr := f.intp(p + "VisibilityTimeout")
			if aerr != nil {
				fail(aerr)
				return
			}
			in.Entries = append(in.Entries, handleBatchEntry{Id: f.get(p + "Id"), ReceiptHandle: f.get(p + "ReceiptHandle"), VisibilityTimeout: vt})
		}
		var out *handleBatchOutput
		var aerr *apiError
		if action == "DeleteMessageBatch" {
			out, aerr = h.deleteMessageBatch(r, in)
		} else {
			out, aerr = h.changeMessageVisibilityBatch(r, in)
		}
		if aerr != nil {
			fail(aerr)
			return
		}
		type okEntry struct {
			XMLName xml.Name
			Id      string `xml:"Id"`
		}
		res := struct {
			XMLName xml.Name
			OK      []okEntry
			Failed  []xmlBatchError `xml:"BatchResultErrorEntry"`
		}{XMLName: xml.Name{Local: action + "Result"}, Failed: xmlBatchErrors(out.Failed)}
		for _, s := range out.Successful {
			res.OK = append(res.OK, okEntry{XMLName: xml.Name{Local: action + "ResultEntry"}, Id: s.Id})
		}
		writeQuery(w, action, res)

	case "PurgeQueue":
		if _, aerr := h.purgeQueue(r, &queueURLInput{QueueUrl: queueURL}); aerr != nil {
			fail(aerr)
			return
		}
		writeQuery(w, action, nil)

	default:
		fail(newErr("InvalidAction", "InvalidAction", "The action %s is not valid for this endpoint.", action))
	}
}

func querySendInput(f form, prefix string) (*sendMessageInput, *apiError) {
	delay, aerr := f.intp(prefix + "DelaySeconds")
	if aerr != nil {
		return nil, aerr
	}
	attrs, aerr := f.messageAttributes(prefix + "MessageAttribute")
	if aerr != nil {
		return nil, aerr
	}
	return &sendMessageInput{
		MessageBody:            f.get(prefix + "MessageBody"),
		DelaySeconds:           delay,
		MessageAttributes:      attrs,
		MessageGroupId:         f.get(prefix + "MessageGroupId"),
		MessageDeduplicationId: f.get(prefix + "MessageDeduplicationId"),
	}, nil
}
