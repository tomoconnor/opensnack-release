// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/router"
)

// SQS and SNS messaging, end to end through the router: SQS over the JSON
// protocol (what current SDKs and CLI v2 speak), SNS over Query.

type msgClient struct {
	t *testing.T
	h http.Handler
}

func newMsgClient(t *testing.T) *msgClient {
	return &msgClient{t: t, h: router.New(resource.NewMemStore())}
}

func (c *msgClient) sqsRaw(op string, in any) (int, map[string]any) {
	c.t.Helper()
	body, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	req.Host = "sqs.us-east-1.amazonaws.com"
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+op)
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (c *msgClient) sqs(op string, in any) map[string]any {
	c.t.Helper()
	code, out := c.sqsRaw(op, in)
	if code != 200 {
		c.t.Fatalf("%s: %d %v", op, code, out)
	}
	return out
}

func (c *msgClient) query(host string, params url.Values) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(params.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (c *msgClient) sns(params url.Values) string {
	c.t.Helper()
	code, body := c.query("sns.us-east-1.amazonaws.com", params)
	if code != 200 {
		c.t.Fatalf("%s: %d %s", params.Get("Action"), code, body)
	}
	return body
}

func (c *msgClient) createQueue(name string, attrs map[string]string) string {
	c.t.Helper()
	out := c.sqs("CreateQueue", map[string]any{"QueueName": name, "Attributes": attrs})
	return out["QueueUrl"].(string)
}

func (c *msgClient) receive(queueURL string, extra map[string]any) []map[string]any {
	c.t.Helper()
	in := map[string]any{"QueueUrl": queueURL, "MessageSystemAttributeNames": []string{"All"}, "MessageAttributeNames": []string{"All"}, "MaxNumberOfMessages": 10}
	for k, v := range extra {
		in[k] = v
	}
	out := c.sqs("ReceiveMessage", in)
	var msgs []map[string]any
	if list, ok := out["Messages"].([]any); ok {
		for _, m := range list {
			msgs = append(msgs, m.(map[string]any))
		}
	}
	return msgs
}

func (c *msgClient) queueAttrs(queueURL string) map[string]any {
	c.t.Helper()
	return c.sqs("GetQueueAttributes", map[string]any{"QueueUrl": queueURL, "AttributeNames": []string{"All"}})["Attributes"].(map[string]any)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSQS_SendReceiveVisibilityDelete(t *testing.T) {
	c := newMsgClient(t)
	q := c.createQueue("work", nil)

	out := c.sqs("SendMessage", map[string]any{
		"QueueUrl":    q,
		"MessageBody": "hello world",
		"MessageAttributes": map[string]any{
			"color": map[string]any{"DataType": "String", "StringValue": "red"},
		},
	})
	if out["MD5OfMessageBody"] != md5hex("hello world") {
		t.Fatalf("MD5OfMessageBody = %v", out["MD5OfMessageBody"])
	}
	// The attribute digest, spelled out: length-prefixed name, type, a
	// transport byte (1 = string) and the length-prefixed value.
	var b []byte
	for _, s := range []string{"color", "String"} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	b = append(b, 1, 0, 0, 0, 3)
	b = append(b, "red"...)
	if out["MD5OfMessageAttributes"] != md5hex(string(b)) {
		t.Fatalf("MD5OfMessageAttributes = %v, want %s", out["MD5OfMessageAttributes"], md5hex(string(b)))
	}

	msgs := c.receive(q, map[string]any{"VisibilityTimeout": 1})
	if len(msgs) != 1 || msgs[0]["Body"] != "hello world" {
		t.Fatalf("first receive: %v", msgs)
	}
	m := msgs[0]
	attrs := m["Attributes"].(map[string]any)
	if attrs["ApproximateReceiveCount"] != "1" || attrs["SentTimestamp"] == "" {
		t.Fatalf("system attributes: %v", attrs)
	}
	if m["MD5OfMessageAttributes"] != out["MD5OfMessageAttributes"] {
		t.Fatalf("receive MD5OfMessageAttributes = %v", m["MD5OfMessageAttributes"])
	}

	if again := c.receive(q, nil); len(again) != 0 {
		t.Fatalf("message visible inside its visibility timeout: %v", again)
	}
	if a := c.queueAttrs(q); a["ApproximateNumberOfMessagesNotVisible"] != "1" || a["ApproximateNumberOfMessages"] != "0" {
		t.Fatalf("counts in flight: %v", a)
	}

	time.Sleep(1100 * time.Millisecond)
	msgs = c.receive(q, nil)
	if len(msgs) != 1 {
		t.Fatalf("message not back after its visibility timeout: %v", msgs)
	}
	if n := msgs[0]["Attributes"].(map[string]any)["ApproximateReceiveCount"]; n != "2" {
		t.Fatalf("ApproximateReceiveCount = %v", n)
	}

	c.sqs("DeleteMessage", map[string]any{"QueueUrl": q, "ReceiptHandle": msgs[0]["ReceiptHandle"]})
	a := c.queueAttrs(q)
	if a["ApproximateNumberOfMessages"] != "0" || a["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatalf("counts after delete: %v", a)
	}
	if code, _ := c.sqsRaw("DeleteMessage", map[string]any{"QueueUrl": q, "ReceiptHandle": "garbage"}); code != 400 {
		t.Fatalf("bad receipt handle: %d", code)
	}
}

// Two receivers must never get the same message inside its visibility window.
func TestSQS_ConcurrentReceiversNeverShareAMessage(t *testing.T) {
	c := newMsgClient(t)
	q := c.createQueue("busy", nil)
	const n = 60
	for i := 0; i < n; i++ {
		c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "m"})
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, out := c.sqsRaw("ReceiveMessage", map[string]any{"QueueUrl": q, "MaxNumberOfMessages": 3})
				list, _ := out["Messages"].([]any)
				if len(list) == 0 {
					return
				}
				mu.Lock()
				for _, m := range list {
					seen[m.(map[string]any)["MessageId"].(string)]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("received %d distinct messages, want %d", len(seen), n)
	}
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("message %s received %d times", id, k)
		}
	}
}

func TestSQS_RedriveToDLQ(t *testing.T) {
	c := newMsgClient(t)
	dlq := c.createQueue("dead", nil)
	dlqArn := c.queueAttrs(dlq)["QueueArn"].(string)
	q := c.createQueue("live", map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"` + dlqArn + `","maxReceiveCount":"2"}`,
	})
	// Stored and returned in AWS's normalised form.
	if rp := c.queueAttrs(q)["RedrivePolicy"]; rp != `{"deadLetterTargetArn":"`+dlqArn+`","maxReceiveCount":2}` {
		t.Fatalf("RedrivePolicy = %v", rp)
	}

	c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "poison"})
	for i := 1; i <= 2; i++ {
		if msgs := c.receive(q, map[string]any{"VisibilityTimeout": 0}); len(msgs) != 1 {
			t.Fatalf("receive %d: %v", i, msgs)
		}
	}
	// The third receive finds it has been received maxReceiveCount times.
	if msgs := c.receive(q, map[string]any{"VisibilityTimeout": 0}); len(msgs) != 0 {
		t.Fatalf("message not redriven: %v", msgs)
	}
	msgs := c.receive(dlq, nil)
	if len(msgs) != 1 || msgs[0]["Body"] != "poison" {
		t.Fatalf("DLQ: %v", msgs)
	}
	src := msgs[0]["Attributes"].(map[string]any)["DeadLetterQueueSourceArn"]
	if src != strings.Replace(dlqArn, "dead", "live", 1) {
		t.Fatalf("DeadLetterQueueSourceArn = %v", src)
	}
	if got := c.sqs("ListDeadLetterSourceQueues", map[string]any{"QueueUrl": dlq})["queueUrls"].([]any); len(got) != 1 || got[0] != q {
		t.Fatalf("ListDeadLetterSourceQueues = %v", got)
	}
}

func TestSQS_FIFOOrderingAndDeduplication(t *testing.T) {
	c := newMsgClient(t)
	q := c.createQueue("orders.fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})

	first := c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "a1", "MessageGroupId": "A"})
	c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "b1", "MessageGroupId": "B"})
	c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "a2", "MessageGroupId": "A"})
	dup := c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "a1", "MessageGroupId": "A"})
	if dup["MessageId"] != first["MessageId"] {
		t.Fatalf("content-based duplicate got a new id: %v vs %v", dup["MessageId"], first["MessageId"])
	}
	if code, _ := c.sqsRaw("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "x"}); code != 400 {
		t.Fatalf("FIFO send without MessageGroupId: %d", code)
	}

	msgs := c.receive(q, map[string]any{"MaxNumberOfMessages": 1})
	if len(msgs) != 1 || msgs[0]["Body"] != "a1" {
		t.Fatalf("first: %v", msgs)
	}
	// Group A is locked while a1 is in flight; only B is available.
	msgs = c.receive(q, nil)
	if len(msgs) != 1 || msgs[0]["Body"] != "b1" {
		t.Fatalf("while A is in flight: %v", msgs)
	}
	if g := msgs[0]["Attributes"].(map[string]any)["MessageGroupId"]; g != "B" {
		t.Fatalf("MessageGroupId = %v", g)
	}
	c.sqs("DeleteMessage", map[string]any{"QueueUrl": q, "ReceiptHandle": c.firstHandle(msgs)})
	if a := c.queueAttrs(q); a["ApproximateNumberOfMessages"] != "1" || a["FifoQueue"] != "true" || a["DeduplicationScope"] != "queue" {
		t.Fatalf("attributes: %v", a)
	}
}

func (c *msgClient) firstHandle(msgs []map[string]any) string {
	return msgs[0]["ReceiptHandle"].(string)
}

func TestSQS_LongPollingWakesOnSend(t *testing.T) {
	c := newMsgClient(t)
	q := c.createQueue("poll", nil)
	go func() {
		time.Sleep(200 * time.Millisecond)
		c.sqs("SendMessage", map[string]any{"QueueUrl": q, "MessageBody": "late"})
	}()
	start := time.Now()
	msgs := c.receive(q, map[string]any{"WaitTimeSeconds": 5})
	if len(msgs) != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("long poll returned %v after %s", msgs, time.Since(start))
	}
}

// The Query protocol reaches the same operations.
func TestSQS_QueryProtocolParity(t *testing.T) {
	c := newMsgClient(t)
	host := "sqs.us-east-1.amazonaws.com"
	code, body := c.query(host, url.Values{"Action": {"CreateQueue"}, "QueueName": {"legacy"}, "Attribute.1.Name": {"VisibilityTimeout"}, "Attribute.1.Value": {"45"}, "Tag.1.Key": {"team"}, "Tag.1.Value": {"team"}})
	if code != 200 {
		t.Fatalf("CreateQueue: %s", body)
	}
	q := "https://" + host + "/000000000000/legacy"
	code, body = c.query(host, url.Values{"Action": {"SendMessage"}, "QueueUrl": {q}, "MessageBody": {"via query"},
		"MessageAttribute.1.Name": {"n"}, "MessageAttribute.1.Value.DataType": {"Number"}, "MessageAttribute.1.Value.StringValue": {"7"}})
	if code != 200 || !strings.Contains(body, "<MD5OfMessageBody>"+md5hex("via query")+"</MD5OfMessageBody>") {
		t.Fatalf("SendMessage: %s", body)
	}
	code, body = c.query(host, url.Values{"Action": {"GetQueueAttributes"}, "QueueUrl": {q}, "AttributeName.1": {"All"}})
	for _, want := range []string{"<Name>VisibilityTimeout</Name><Value>45</Value>", "<Name>ApproximateNumberOfMessages</Name><Value>1</Value>"} {
		if !strings.Contains(strings.Join(strings.Fields(body), ""), want) {
			t.Fatalf("GetQueueAttributes missing %s: %s", want, body)
		}
	}
	code, body = c.query(host, url.Values{"Action": {"ReceiveMessage"}, "QueueUrl": {q}, "MessageAttributeName.1": {"All"}, "AttributeName.1": {"All"}})
	if code != 200 || !strings.Contains(body, "<Body>via query</Body>") || !strings.Contains(body, "<StringValue>7</StringValue>") {
		t.Fatalf("ReceiveMessage: %s", body)
	}
	code, body = c.query(host, url.Values{"Action": {"ListQueueTags"}, "QueueUrl": {q}})
	if !strings.Contains(body, "<Key>team</Key>") {
		t.Fatalf("ListQueueTags: %s", body)
	}
	code, body = c.query(host, url.Values{"Action": {"GetQueueAttributes"}, "QueueUrl": {"https://" + host + "/000000000000/nope"}})
	if code != 400 || !strings.Contains(body, "AWS.SimpleQueueService.NonExistentQueue") {
		t.Fatalf("missing queue: %d %s", code, body)
	}
}

// subscribeQueue subscribes a new queue to a topic and returns its URL.
func (c *msgClient) subscribeQueue(topicArn, name string, attrs map[string]string) string {
	c.t.Helper()
	q := c.createQueue(name, nil)
	arn := c.queueAttrs(q)["QueueArn"].(string)
	p := url.Values{"Action": {"Subscribe"}, "TopicArn": {topicArn}, "Protocol": {"sqs"}, "Endpoint": {arn}}
	i := 1
	for k, v := range attrs {
		n := "Attributes.entry." + string(rune('0'+i))
		p.Set(n+".key", k)
		p.Set(n+".value", v)
		i++
	}
	c.sns(p)
	return q
}

func (c *msgClient) createTopic(name string) string {
	c.t.Helper()
	body := c.sns(url.Values{"Action": {"CreateTopic"}, "Name": {name}})
	start := strings.Index(body, "<TopicArn>") + len("<TopicArn>")
	return body[start:strings.Index(body, "</TopicArn>")]
}

func TestSNS_PublishDeliversToSQSRawAndEnveloped(t *testing.T) {
	c := newMsgClient(t)
	topic := c.createTopic("events")
	enveloped := c.subscribeQueue(topic, "enveloped", nil)
	raw := c.subscribeQueue(topic, "raw", map[string]string{"RawMessageDelivery": "true"})

	body := c.sns(url.Values{
		"Action": {"Publish"}, "TopicArn": {topic}, "Message": {`{"asset":"crate"}`}, "Subject": {"hi"},
		"MessageAttributes.entry.1.Name": {"kind"}, "MessageAttributes.entry.1.Value.DataType": {"String"}, "MessageAttributes.entry.1.Value.StringValue": {"widget"},
	})
	if !strings.Contains(body, "<MessageId>") {
		t.Fatalf("Publish: %s", body)
	}

	msgs := c.receive(raw, nil)
	if len(msgs) != 1 || msgs[0]["Body"] != `{"asset":"crate"}` {
		t.Fatalf("raw delivery: %v", msgs)
	}
	if v := msgs[0]["MessageAttributes"].(map[string]any)["kind"].(map[string]any)["StringValue"]; v != "widget" {
		t.Fatalf("raw delivery attributes: %v", msgs[0]["MessageAttributes"])
	}

	msgs = c.receive(enveloped, nil)
	if len(msgs) != 1 {
		t.Fatalf("enveloped delivery: %v", msgs)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(msgs[0]["Body"].(string)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", msgs[0]["Body"])
	}
	if env["Type"] != "Notification" || env["TopicArn"] != topic || env["Message"] != `{"asset":"crate"}` || env["Subject"] != "hi" {
		t.Fatalf("envelope: %v", env)
	}
	if a := env["MessageAttributes"].(map[string]any)["kind"].(map[string]any); a["Type"] != "String" || a["Value"] != "widget" {
		t.Fatalf("envelope attributes: %v", env["MessageAttributes"])
	}
	if msgs[0]["MessageAttributes"] != nil {
		t.Fatalf("enveloped delivery should not carry SQS attributes: %v", msgs[0]["MessageAttributes"])
	}
}

func TestSNS_FilterPolicyDropsMessages(t *testing.T) {
	c := newMsgClient(t)
	topic := c.createTopic("filtered")
	attrs := c.subscribeQueue(topic, "reds", map[string]string{"FilterPolicy": `{"color":["red",{"prefix":"crim"}],"size":[{"numeric":[">",3]}]}`, "RawMessageDelivery": "true"})
	body := c.subscribeQueue(topic, "bodies", map[string]string{"FilterPolicy": `{"widget":{"kind":["train"]}}`, "FilterPolicyScope": "MessageBody", "RawMessageDelivery": "true"})

	publish := func(msg, color, size string) {
		c.sns(url.Values{
			"Action": {"Publish"}, "TopicArn": {topic}, "Message": {msg},
			"MessageAttributes.entry.1.Name": {"color"}, "MessageAttributes.entry.1.Value.DataType": {"String"}, "MessageAttributes.entry.1.Value.StringValue": {color},
			"MessageAttributes.entry.2.Name": {"size"}, "MessageAttributes.entry.2.Value.DataType": {"Number"}, "MessageAttributes.entry.2.Value.StringValue": {size},
		})
	}
	publish(`{"widget":{"kind":"doll"}}`, "blue", "5")    // fails color; body fails
	publish(`{"widget":{"kind":"train"}}`, "red", "1")    // fails size; body matches
	publish(`{"widget":{"kind":"doll"}}`, "crimson", "9") // matches attributes

	if msgs := c.receive(attrs, nil); len(msgs) != 1 || msgs[0]["Body"] != `{"widget":{"kind":"doll"}}` || msgs[0]["MessageAttributes"].(map[string]any)["color"].(map[string]any)["StringValue"] != "crimson" {
		t.Fatalf("attribute-filtered queue: %v", msgs)
	}
	if msgs := c.receive(body, nil); len(msgs) != 1 || msgs[0]["Body"] != `{"widget":{"kind":"train"}}` {
		t.Fatalf("body-filtered queue: %v", msgs)
	}

	// The filter policy and its scope come back from the subscription.
	list := c.sns(url.Values{"Action": {"ListSubscriptionsByTopic"}, "TopicArn": {topic}})
	start := strings.Index(list, "<SubscriptionArn>") + len("<SubscriptionArn>")
	subArn := list[start:strings.Index(list, "</SubscriptionArn>")]
	got := c.sns(url.Values{"Action": {"GetSubscriptionAttributes"}, "SubscriptionArn": {subArn}})
	if !strings.Contains(got, "<key>FilterPolicyScope</key>") || !strings.Contains(got, "<key>RawMessageDelivery</key>") {
		t.Fatalf("GetSubscriptionAttributes: %s", got)
	}
}

func TestSNS_PublishBatchAndTags(t *testing.T) {
	c := newMsgClient(t)
	topic := c.createTopic("batch")
	q := c.subscribeQueue(topic, "sink", map[string]string{"RawMessageDelivery": "true"})
	body := c.sns(url.Values{
		"Action": {"PublishBatch"}, "TopicArn": {topic},
		"PublishBatchRequestEntries.member.1.Id": {"one"}, "PublishBatchRequestEntries.member.1.Message": {"m1"},
		"PublishBatchRequestEntries.member.2.Id": {"two"}, "PublishBatchRequestEntries.member.2.Message": {"m2"},
	})
	if strings.Count(body, "<MessageId>") != 2 {
		t.Fatalf("PublishBatch: %s", body)
	}
	if msgs := c.receive(q, nil); len(msgs) != 2 {
		t.Fatalf("batch delivery: %v", msgs)
	}

	c.sns(url.Values{"Action": {"TagResource"}, "ResourceArn": {topic}, "Tags.member.1.Key": {"team"}, "Tags.member.1.Value": {"team"}})
	if got := c.sns(url.Values{"Action": {"ListTagsForResource"}, "ResourceArn": {topic}}); !strings.Contains(got, "<Key>team</Key>") {
		t.Fatalf("ListTagsForResource: %s", got)
	}
	c.sns(url.Values{"Action": {"UntagResource"}, "ResourceArn": {topic}, "TagKeys.member.1": {"team"}})
	if got := c.sns(url.Values{"Action": {"ListTagsForResource"}, "ResourceArn": {topic}}); strings.Contains(got, "<Key>team</Key>") {
		t.Fatalf("tag not removed: %s", got)
	}
}

// Regression (P0.8): Publish wrote MissingParameter and then went on to
// write a success response as well.
func TestSNS_PublishWithoutMessageFailsCleanly(t *testing.T) {
	c := newMsgClient(t)
	topic := c.createTopic("quiet")
	code, body := c.query("sns.us-east-1.amazonaws.com", url.Values{"Action": {"Publish"}, "TopicArn": {topic}})
	if code != 400 || strings.Contains(body, "<MessageId>") {
		t.Fatalf("Publish without Message: %d %s", code, body)
	}
}
