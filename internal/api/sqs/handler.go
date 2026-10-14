// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const APIVersion = "2012-11-05"

type Handler struct {
	Store  resource.Store
	Broker *Broker
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store, Broker: NewBroker()}
}

// DeleteNamespaceMessages drops a tenant's in-memory messages; the router
// registers it with the inspection API's namespace delete.
func (h *Handler) DeleteNamespaceMessages(namespace string) error {
	return h.Broker.DropNamespace(namespace)
}

// buildQueueURL returns the queue URL on whatever host the client used, so it
// is https://sqs.{region}.amazonaws.com/{account}/{name} in the sandboxes and
// http://localhost:4566/{account}/{name} locally.
func buildQueueURL(r *http.Request, name string) string {
	return util.BaseURL(r) + "/" + util.AccountID(r) + "/" + name
}

func queueArn(r *http.Request, name string) string {
	return "arn:aws:sqs:" + util.Region(r) + ":" + util.AccountID(r) + ":" + name
}

// Dispatch serves both wire protocols: JSON (X-Amz-Target: AmazonSQS.*),
// which current SDKs and AWS CLI v2 use, and the older Query protocol.
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		h.dispatchJSON(w, r, strings.TrimPrefix(target, "AmazonSQS."))
		return
	}
	h.dispatchQuery(w, r)
}

// queueRecord is a queue as kept in the resource store.
type queueRecord struct {
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Created    int64             `json:"created_ts,omitempty"`
	Modified   int64             `json:"modified_ts,omitempty"`
}

func (q *queueRecord) fifo() bool { return q.Attributes["FifoQueue"] == "true" }

// queue is a queue resolved for one request.
type queue struct {
	res   *resource.Resource
	rec   *queueRecord
	scope string
	arn   string
	url   string
}

func (q *queue) key() queueKey { return queueKey{scope: q.scope, name: q.rec.Name} }

func decodeQueue(res *resource.Resource) *queueRecord {
	rec := &queueRecord{}
	_ = json.Unmarshal(res.Attributes, rec)
	if rec.Name == "" {
		rec.Name = res.ID
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]string{}
	}
	if rec.Tags == nil {
		rec.Tags = map[string]string{}
	}
	if rec.Created == 0 {
		rec.Created = res.CreatedAt.Unix()
		if res.CreatedAt.IsZero() {
			rec.Created = time.Now().Unix()
		}
	}
	if rec.Modified == 0 {
		rec.Modified = rec.Created
	}
	return rec
}

func (h *Handler) saveQueue(q *queue) *apiError {
	buf, _ := json.Marshal(q.rec)
	q.res.Attributes = buf
	if err := h.Store.Update(q.res); err != nil {
		return errInternal(err)
	}
	return nil
}

// queueNameFromURL takes the last path element of a queue URL. Query
// clients may also post to the queue URL itself and leave QueueUrl out.
func queueNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.Trim(u.Path, "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}

func (h *Handler) lookup(r *http.Request, name string) (*queue, *apiError) {
	scope := util.NamespaceFromHeader(r)
	if name == "" {
		return nil, errNoQueue()
	}
	res, err := h.Store.Get(name, "sqs", "queue", scope)
	if err != nil || res == nil {
		return nil, errNoQueue()
	}
	rec := decodeQueue(res)
	return &queue{res: res, rec: rec, scope: scope, arn: queueArn(r, rec.Name), url: buildQueueURL(r, rec.Name)}, nil
}

func (h *Handler) queueFromURL(r *http.Request, queueURL string) (*queue, *apiError) {
	if queueURL == "" {
		return nil, errMissing("QueueUrl")
	}
	return h.lookup(r, queueNameFromURL(queueURL))
}

// parseQueueArn splits arn:aws:sqs:{region}:{account}:{name}.
func parseQueueArn(arn string) (region, account, name string, ok bool) {
	parts := strings.Split(arn, ":")
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sqs" {
		return "", "", "", false
	}
	return parts[3], parts[4], parts[5], true
}

// queueByArn finds a queue of the request's tenant by ARN, in whichever
// region the ARN names.
func (h *Handler) queueByArn(namespace, arn string) (*resource.Resource, string, bool) {
	region, _, name, ok := parseQueueArn(arn)
	if !ok {
		return nil, "", false
	}
	scope := util.RegionalScope(namespace, region)
	res, err := h.Store.Get(name, "sqs", "queue", scope)
	if err != nil || res == nil {
		return nil, "", false
	}
	return res, scope, true
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// config turns a queue's attributes into what the broker needs, resolving
// the dead-letter queue when a RedrivePolicy names one that exists.
func (h *Handler) config(r *http.Request, q *queue) *queueConfig {
	return h.configFor(util.IdentityFrom(r).Namespace, q.scope, q.arn, q.rec, true)
}

func (h *Handler) configFor(namespace, scope, arn string, rec *queueRecord, withDLQ bool) *queueConfig {
	attrs := effectiveAttrs(rec)
	cfg := &queueConfig{
		key:          queueKey{scope: scope, name: rec.Name},
		arn:          arn,
		fifo:         rec.fifo(),
		contentDedup: attrs["ContentBasedDeduplication"] == "true",
		groupDedup:   attrs["DeduplicationScope"] == "messageGroup",
		visibility:   time.Duration(atoiDefault(attrs["VisibilityTimeout"], 30)) * time.Second,
		delay:        time.Duration(atoiDefault(attrs["DelaySeconds"], 0)) * time.Second,
		retention:    time.Duration(atoiDefault(attrs["MessageRetentionPeriod"], 345600)) * time.Second,
	}
	if !withDLQ || attrs["RedrivePolicy"] == "" {
		return cfg
	}
	p, aerr := parseRedrivePolicy(attrs["RedrivePolicy"])
	if aerr != nil {
		return cfg
	}
	res, dscope, ok := h.queueByArn(namespace, p.DeadLetterTargetArn)
	if !ok {
		return cfg
	}
	cfg.dlq = h.configFor(namespace, dscope, p.DeadLetterTargetArn, decodeQueue(res), false)
	cfg.maxReceive = p.MaxReceiveCount
	return cfg
}
