// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"opensnack/internal/util"

	"go.uber.org/zap"
)

// Scope is the tenant an event belongs to.
type Scope struct {
	Namespace string
	Region    string
	AccountID string
}

// Delivery is one event on its way to one target.
type Delivery struct {
	TargetArn string
	// Payload is what the target receives, after Input, InputPath or
	// InputTransformer.
	Payload []byte
	Target  Target
	EventID string
	RuleArn string
}

// Deliverer hands an event to one kind of target (SQS, SNS, Lambda, ...).
type Deliverer interface {
	Deliver(ctx context.Context, scope Scope, d Delivery) error
}

// ErrSkipped marks a delivery the deliverer chose not to make, such as a
// target type opensnack can't run.
var ErrSkipped = errors.New("delivery skipped")

// Registry maps an ARN's service ("sqs", "sns", "lambda") to its deliverer.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Deliverer
}

func NewRegistry() *Registry { return &Registry{m: map[string]Deliverer{}} }

func (r *Registry) Set(service string, d Deliverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[service] = d
}

func (r *Registry) For(arn string) (Deliverer, string) {
	svc := arnService(arn)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[svc], svc
}

func arnService(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return ""
	}
	return parts[2]
}

// arnParts splits arn:partition:service:region:account:resource.
func arnParts(arn string) (region, account, res string, ok bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return "", "", "", false
	}
	return parts[3], parts[4], parts[5], true
}

// Attempt is one recorded delivery, kept for debugging.
type Attempt struct {
	Time      time.Time `json:"time"`
	Namespace string    `json:"namespace"`
	Region    string    `json:"region"`
	RuleArn   string    `json:"rule_arn"`
	TargetID  string    `json:"target_id"`
	TargetArn string    `json:"target_arn"`
	EventID   string    `json:"event_id"`
	Status    string    `json:"status"` // delivered, failed, skipped
	Error     string    `json:"error,omitempty"`
	Payload   string    `json:"payload"`
}

// DeliveryLog keeps the most recent attempts per namespace in memory.
type DeliveryLog struct {
	mu   sync.Mutex
	max  int
	byNS map[string][]Attempt
}

func NewDeliveryLog(max int) *DeliveryLog {
	return &DeliveryLog{max: max, byNS: map[string][]Attempt{}}
}

func (l *DeliveryLog) Add(a Attempt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := append(l.byNS[a.Namespace], a)
	if len(list) > l.max {
		list = list[len(list)-l.max:]
	}
	l.byNS[a.Namespace] = list
}

func (l *DeliveryLog) List(namespace string) []Attempt {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Attempt(nil), l.byNS[namespace]...)
}

// Handler serves GET ?namespace=ns with that namespace's recent attempts,
// behind the same bearer token as the inspection API.
func (l *DeliveryLog) Handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, `{"error":"missing or wrong bearer token"}`, http.StatusUnauthorized)
			return
		}
		ns := r.URL.Query().Get("namespace")
		if ns == "" {
			http.Error(w, `{"error":"namespace is required"}`, http.StatusBadRequest)
			return
		}
		list := l.List(ns)
		if list == nil {
			list = []Attempt{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"deliveries": list})
	})
}

func (h *Handler) deliver(ctx context.Context, scope Scope, d Delivery) {
	a := Attempt{
		Time: h.now(), Namespace: scope.Namespace, Region: scope.Region,
		RuleArn: d.RuleArn, TargetID: d.Target.Id, TargetArn: d.TargetArn,
		EventID: d.EventID, Status: "delivered", Payload: truncate(string(d.Payload), 2048),
	}
	dl, svc := h.Deliverers.For(d.TargetArn)
	var err error
	if dl == nil {
		err = fmt.Errorf("%w: no deliverer for %q targets", ErrSkipped, svc)
	} else {
		err = dl.Deliver(ctx, scope, d)
	}
	switch {
	case errors.Is(err, ErrSkipped):
		a.Status, a.Error = "skipped", err.Error()
	case err != nil:
		a.Status, a.Error = "failed", err.Error()
	}
	h.Log.Add(a)
	zap.L().Info("events delivery",
		zap.String("namespace", scope.Namespace), zap.String("region", scope.Region),
		zap.String("rule", d.RuleArn), zap.String("target", d.TargetArn),
		zap.String("event_id", d.EventID), zap.String("status", a.Status), zap.String("error", a.Error))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// roundTrip calls an in-process handler as if a client signed for service
// in region had sent the request.
func roundTrip(ctx context.Context, h http.Handler, scope Scope, service, region, contentType string, headers map[string]string, body []byte) (int, []byte) {
	if region == "" {
		region = scope.Region
	}
	host := service + "." + region + ".amazonaws.com"
	ctx = util.WithIdentity(ctx, &util.Identity{
		Namespace:      scope.Namespace,
		AccountID:      scope.AccountID,
		Region:         region,
		SigningService: service,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", bytes.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	return rec.status, rec.body.Bytes()
}

type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

// SQSDeliverer sends to a queue through the SQS JSON API (GetQueueUrl, then
// SendMessage), so it works whatever the SQS handler does inside.
type SQSDeliverer struct{ Handler http.Handler }

func (s SQSDeliverer) Deliver(ctx context.Context, scope Scope, d Delivery) error {
	region, account, name, ok := arnParts(d.TargetArn)
	if !ok || name == "" {
		return fmt.Errorf("bad SQS ARN %q", d.TargetArn)
	}
	if account != "" {
		scope.AccountID = account
	}
	call := func(op string, in any) (map[string]any, error) {
		body, _ := json.Marshal(in)
		status, out := roundTrip(ctx, s.Handler, scope, "sqs", region, "application/x-amz-json-1.0",
			map[string]string{"X-Amz-Target": "AmazonSQS." + op}, body)
		var m map[string]any
		json.Unmarshal(out, &m)
		if status/100 != 2 {
			return nil, fmt.Errorf("sqs %s: HTTP %d: %s", op, status, truncate(strings.TrimSpace(string(out)), 300))
		}
		return m, nil
	}
	out, err := call("GetQueueUrl", map[string]string{"QueueName": name, "QueueOwnerAWSAccountId": scope.AccountID})
	if err != nil {
		return err
	}
	queueURL, _ := out["QueueUrl"].(string)
	if queueURL == "" {
		return fmt.Errorf("sqs GetQueueUrl returned no QueueUrl for %s", name)
	}
	in := map[string]any{"QueueUrl": queueURL, "MessageBody": string(d.Payload)}
	if p := d.Target.SqsParameters; p != nil && p.MessageGroupId != nil {
		in["MessageGroupId"] = *p.MessageGroupId
	}
	if strings.HasSuffix(name, ".fifo") {
		in["MessageDeduplicationId"] = d.EventID
	}
	_, err = call("SendMessage", in)
	return err
}

// SNSDeliverer publishes to a topic through the SNS Query API.
type SNSDeliverer struct{ Handler http.Handler }

func (s SNSDeliverer) Deliver(ctx context.Context, scope Scope, d Delivery) error {
	region, account, _, ok := arnParts(d.TargetArn)
	if !ok {
		return fmt.Errorf("bad SNS ARN %q", d.TargetArn)
	}
	if account != "" {
		scope.AccountID = account
	}
	form := url.Values{
		"Action":   {"Publish"},
		"Version":  {"2010-03-31"},
		"TopicArn": {d.TargetArn},
		"Message":  {string(d.Payload)},
	}
	if strings.HasSuffix(d.TargetArn, ".fifo") {
		form.Set("MessageGroupId", d.RuleArn)
		form.Set("MessageDeduplicationId", d.EventID)
	}
	status, out := roundTrip(ctx, s.Handler, scope, "sns", region, "application/x-www-form-urlencoded", nil, []byte(form.Encode()))
	if status/100 != 2 {
		return fmt.Errorf("sns Publish: HTTP %d: %s", status, truncate(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// LogDeliverer records the event and delivers nothing; it stands in for
// target types opensnack can't run yet.
type LogDeliverer struct{ Reason string }

func (l LogDeliverer) Deliver(ctx context.Context, scope Scope, d Delivery) error {
	return fmt.Errorf("%w: %s", ErrSkipped, l.Reason)
}
