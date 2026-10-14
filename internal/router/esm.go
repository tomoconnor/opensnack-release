// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"opensnack/internal/api/lambda"
	"opensnack/internal/api/sns"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"go.uber.org/zap"
)

// snsLambda delivers SNS notifications to Lambda subscriptions, as an
// asynchronous invoke with the aws:sns event.
type snsLambda struct{ lambdaInvoker }

func (s snsLambda) Deliver(ctx context.Context, d *sns.Delivery) error {
	region, _, _ := arnRegionAccount(d.Endpoint)
	var msg map[string]any
	if err := json.Unmarshal([]byte(d.Envelope), &msg); err != nil {
		return fmt.Errorf("sns→lambda: bad envelope: %w", err)
	}
	event, _ := json.Marshal(map[string]any{"Records": []any{map[string]any{
		"EventSource":          "aws:sns",
		"EventVersion":         "1.0",
		"EventSubscriptionArn": d.SubscriptionArn,
		"Sns":                  msg,
	}}})
	_, err := s.invoke(ctx, util.Identity{Namespace: d.Namespace, Region: region, AccountID: d.AccountID},
		d.Endpoint, event, true)
	return err
}

func arnRegionAccount(arn string) (region, account, resourcePart string) {
	p := strings.SplitN(arn, ":", 6)
	if len(p) < 6 {
		return "", "", ""
	}
	return p[3], p[4], p[5]
}

// ESMPoller runs Lambda event source mappings from SQS: it receives a batch
// from each enabled mapping's queue, invokes the function with the aws:sqs
// event, and deletes what succeeded. A failed batch is left to reappear
// after the visibility timeout, so retries and redrive to a DLQ behave as in
// AWS. ReportBatchItemFailures is honoured.
//
// Each mapping is worked in its own goroutine, one batch at a time, so a
// function's cold start or long run holds up only its own mapping.
type ESMPoller struct {
	insp     resource.Inspector
	lambda   *lambda.Handler
	sqs      http.Handler
	interval time.Duration

	stop chan struct{}
	done sync.WaitGroup

	mu       sync.Mutex
	inFlight map[string]bool
}

func newESMPoller(insp resource.Inspector, l *lambda.Handler, sqs http.Handler) *ESMPoller {
	return &ESMPoller{insp: insp, lambda: l, sqs: sqs, interval: time.Second, inFlight: map[string]bool{}}
}

func (p *ESMPoller) Start() {
	p.stop = make(chan struct{})
	p.done.Add(1)
	go func() {
		defer p.done.Done()
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.poll(context.Background(), false)
			}
		}
	}()
}

func (p *ESMPoller) Stop() {
	if p.stop != nil {
		close(p.stop)
		p.done.Wait()
	}
}

// Poll makes one pass over every enabled SQS mapping, in every namespace,
// waits for it, and returns how many messages it handed to functions.
func (p *ESMPoller) Poll(ctx context.Context) int {
	return p.poll(ctx, true)
}

// poll starts a batch for each enabled mapping that hasn't one in flight;
// with wait, it waits for them and counts the messages.
func (p *ESMPoller) poll(ctx context.Context, wait bool) int {
	rows, err := p.insp.Find(resource.Filter{Service: "lambda", Type: "event-source-mapping"})
	if err != nil {
		return 0
	}
	scopes := map[string]bool{}
	for _, r := range rows {
		scopes[resource.JoinScope(r.Namespace, r.Region)] = true
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for scope := range scopes {
		ns, region := resource.SplitScope(scope)
		for _, m := range p.lambda.EventSourceMappings(scope) {
			if m.State != "Enabled" || !strings.HasPrefix(m.EventSourceArn, "arn:aws:sqs:") {
				continue
			}
			p.mu.Lock()
			busy := p.inFlight[m.UUID]
			p.inFlight[m.UUID] = true
			p.mu.Unlock()
			if busy {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					p.mu.Lock()
					delete(p.inFlight, m.UUID)
					p.mu.Unlock()
				}()
				got, err := p.pollOne(ctx, ns, region, m)
				if err != nil {
					zap.L().Info("event source mapping", zap.String("uuid", m.UUID), zap.Error(err))
				}
				mu.Lock()
				n += got
				mu.Unlock()
			}()
		}
	}
	if !wait {
		return 0
	}
	wg.Wait()
	return n
}

type sqsMessage struct {
	MessageId         string
	ReceiptHandle     string
	Body              string
	MD5OfBody         string
	Attributes        map[string]string
	MessageAttributes map[string]struct {
		DataType    string
		StringValue *string `json:",omitempty"`
		BinaryValue []byte  `json:",omitempty"`
	}
}

func (p *ESMPoller) pollOne(ctx context.Context, ns, region string, m lambda.EventSourceMappingConfiguration) (int, error) {
	qRegion, account, name := arnRegionAccount(m.EventSourceArn)
	id := util.Identity{Namespace: ns, Region: qRegion, AccountID: account, SigningService: "sqs"}
	queueURL := "https://sqs." + qRegion + ".amazonaws.com/" + account + "/" + name

	batch := m.BatchSize
	if batch <= 0 || batch > 10 {
		batch = 10
	}
	var recv struct{ Messages []sqsMessage }
	if err := p.sqsCall(ctx, id, "ReceiveMessage", map[string]any{
		"QueueUrl": queueURL, "MaxNumberOfMessages": batch,
		"MessageSystemAttributeNames": []string{"All"}, "MessageAttributeNames": []string{"All"},
	}, &recv); err != nil {
		return 0, err
	}
	if len(recv.Messages) == 0 {
		return 0, nil
	}

	records := make([]any, len(recv.Messages))
	for i, msg := range recv.Messages {
		attrs := map[string]any{}
		for k, v := range msg.MessageAttributes {
			a := map[string]any{"dataType": v.DataType, "stringListValues": []string{}, "binaryListValues": []string{}}
			if v.StringValue != nil {
				a["stringValue"] = *v.StringValue
			}
			if v.BinaryValue != nil {
				a["binaryValue"] = v.BinaryValue
			}
			attrs[k] = a
		}
		records[i] = map[string]any{
			"messageId":         msg.MessageId,
			"receiptHandle":     msg.ReceiptHandle,
			"body":              msg.Body,
			"attributes":        msg.Attributes,
			"messageAttributes": attrs,
			"md5OfBody":         msg.MD5OfBody,
			"eventSource":       "aws:sqs",
			"eventSourceARN":    m.EventSourceArn,
			"awsRegion":         qRegion,
		}
	}
	event, _ := json.Marshal(map[string]any{"Records": records})

	out, err := lambdaInvoker{p.lambda}.invoke(ctx, util.Identity{Namespace: ns, Region: region, AccountID: account},
		m.FunctionArn, event, false)
	var fe *FunctionError
	if errors.As(err, &fe) {
		return len(recv.Messages), nil // the batch becomes visible again and is retried
	}
	if err != nil {
		return 0, err
	}

	// With ReportBatchItemFailures, keep only the messages the function
	// says failed.
	failed := map[string]bool{}
	for _, t := range m.FunctionResponseTypes {
		if t == "ReportBatchItemFailures" {
			var resp struct {
				BatchItemFailures []struct{ ItemIdentifier string } `json:"batchItemFailures"`
			}
			if json.Unmarshal(out, &resp) == nil {
				for _, f := range resp.BatchItemFailures {
					failed[f.ItemIdentifier] = true
				}
			}
		}
	}
	var entries []map[string]string
	for i, msg := range recv.Messages {
		if !failed[msg.MessageId] {
			entries = append(entries, map[string]string{"Id": fmt.Sprint(i), "ReceiptHandle": msg.ReceiptHandle})
		}
	}
	if len(entries) > 0 {
		if err := p.sqsCall(ctx, id, "DeleteMessageBatch", map[string]any{"QueueUrl": queueURL, "Entries": entries}, nil); err != nil {
			return len(recv.Messages), err
		}
	}
	return len(recv.Messages), nil
}

func (p *ESMPoller) sqsCall(ctx context.Context, id util.Identity, op string, in any, out any) error {
	body, _ := json.Marshal(in)
	status, _, resp := inProcess(ctx, p.sqs, id, http.MethodPost, "https://sqs."+id.Region+".amazonaws.com/",
		map[string]string{"X-Amz-Target": "AmazonSQS." + op, "Content-Type": "application/x-amz-json-1.0"}, body)
	if status >= 300 {
		return fmt.Errorf("sqs %s: %d %s", op, status, resp)
	}
	if out != nil {
		return json.Unmarshal(resp, out)
	}
	return nil
}
