// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

// Message storage.
//
// Queues (attributes, tags, policy) live in the resource store like every
// other resource. Messages do not: they are many, small and short-lived, and
// every receive changes them, so writing each one to Postgres would cost a
// row write per send, receive, visibility change and delete. They live in
// memory instead, in a Broker owned by the SQS handler:
//
//   - keyed by (store scope, queue name), so tenants and regions never share
//     a queue, exactly as the store keys the queue itself;
//   - guarded by one mutex, so a receive picks and hides a message in a single
//     critical section and two receivers can never get the same message
//     inside its visibility window (redrive to a DLQ is atomic for the same
//     reason);
//   - lost on restart, which is acceptable for an emulator: the queues
//     themselves survive, only in-flight messages go.
//
// Long polling waits on a per-queue channel that is closed whenever a
// message is added or made visible.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type queueKey struct {
	scope string // "namespace@region", as the store scopes the queue
	name  string
}

type message struct {
	id            string
	body          string
	md5Body       string
	attrs         map[string]MessageAttributeValue
	md5Attrs      string
	sent          time.Time
	visibleAt     time.Time
	receiveCount  int
	firstReceive  time.Time
	receiptHandle string
	groupID       string
	dedupID       string
	seq           string
	senderID      string
	dlqSourceArn  string
	deleted       bool
}

type dedupEntry struct {
	id      string
	seq     string
	expires time.Time
}

type queueState struct {
	msgs     []*message
	byID     map[string]*message
	dedup    map[string]dedupEntry
	seq      uint64
	notify   chan struct{}
	purgedAt time.Time
}

// queueConfig is what the broker needs to know about a queue's attributes.
type queueConfig struct {
	key          queueKey
	arn          string
	fifo         bool
	contentDedup bool
	groupDedup   bool
	visibility   time.Duration
	delay        time.Duration
	retention    time.Duration
	// Redrive: dlq is nil when the queue has no usable RedrivePolicy.
	dlq        *queueConfig
	maxReceive int
}

type Broker struct {
	mu     sync.Mutex
	queues map[queueKey]*queueState
	now    func() time.Time
}

func NewBroker() *Broker {
	return &Broker{queues: map[queueKey]*queueState{}, now: time.Now}
}

func (b *Broker) state(k queueKey) *queueState {
	q := b.queues[k]
	if q == nil {
		q = &queueState{byID: map[string]*message{}, dedup: map[string]dedupEntry{}, notify: make(chan struct{})}
		b.queues[k] = q
	}
	return q
}

func (q *queueState) wake() {
	close(q.notify)
	q.notify = make(chan struct{})
}

// Drop forgets a queue's messages (DeleteQueue, or CreateQueue of a name
// whose old queue is gone).
func (b *Broker) Drop(k queueKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q := b.queues[k]; q != nil {
		q.wake()
		delete(b.queues, k)
	}
}

// DropNamespace forgets every queue of a tenant, in every region. It is an
// inspection-API cleaner.
func (b *Broker) DropNamespace(namespace string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, q := range b.queues {
		if k.scope == namespace || strings.HasPrefix(k.scope, namespace+"@") {
			q.wake()
			delete(b.queues, k)
		}
	}
	return nil
}

type sendInput struct {
	body     string
	attrs    map[string]MessageAttributeValue
	delay    *int
	groupID  string
	dedupID  string
	senderID string
}

type sendResult struct {
	id       string
	md5Body  string
	md5Attrs string
	seq      string
}

func newReceiptHandle(msgID string) string {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString([]byte(msgID + ":" + hex.EncodeToString(nonce)))
}

func messageIDFromHandle(h string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil {
		return "", false
	}
	id, _, ok := strings.Cut(string(raw), ":")
	if !ok || uuid.Validate(id) != nil {
		return "", false
	}
	return id, true
}

// Send enqueues a message. On a FIFO queue a duplicate inside the five
// minute deduplication window is accepted and dropped, and the original
// message's ID is returned, as SQS does.
func (b *Broker) Send(cfg *queueConfig, in sendInput) sendResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	q := b.state(cfg.key)

	res := sendResult{md5Body: md5Hex([]byte(in.body)), md5Attrs: md5OfAttributes(in.attrs)}
	var dedupKey string
	if cfg.fifo {
		dedupID := in.dedupID
		if dedupID == "" {
			sum := sha256.Sum256([]byte(in.body))
			dedupID = hex.EncodeToString(sum[:])
		}
		in.dedupID = dedupID
		dedupKey = dedupID
		if cfg.groupDedup {
			dedupKey = in.groupID + "\x00" + dedupID
		}
		if e, ok := q.dedup[dedupKey]; ok && now.Before(e.expires) {
			res.id, res.seq = e.id, e.seq
			return res
		}
	}

	delay := cfg.delay
	if in.delay != nil && !cfg.fifo {
		delay = time.Duration(*in.delay) * time.Second
	}
	m := &message{
		id:        uuid.NewString(),
		body:      in.body,
		md5Body:   res.md5Body,
		attrs:     in.attrs,
		md5Attrs:  res.md5Attrs,
		sent:      now,
		visibleAt: now.Add(delay),
		groupID:   in.groupID,
		senderID:  in.senderID,
	}
	if cfg.fifo {
		q.seq++
		m.seq = seqNumber(q.seq)
		m.dedupID = in.dedupID
		q.dedup[dedupKey] = dedupEntry{id: m.id, seq: m.seq, expires: now.Add(5 * time.Minute)}
	}
	b.enqueue(q, m)
	res.id, res.seq = m.id, m.seq
	return res
}

func (b *Broker) enqueue(q *queueState, m *message) {
	q.msgs = append(q.msgs, m)
	q.byID[m.id] = m
	q.wake()
}

// compact drops deleted and expired messages.
func (q *queueState) compact(now time.Time, retention time.Duration) {
	kept := q.msgs[:0]
	for _, m := range q.msgs {
		if !m.deleted && retention > 0 && now.Sub(m.sent) >= retention {
			m.deleted = true
			delete(q.byID, m.id)
		}
		if !m.deleted {
			kept = append(kept, m)
		}
	}
	for i := len(kept); i < len(q.msgs); i++ {
		q.msgs[i] = nil
	}
	q.msgs = kept
	for k, e := range q.dedup {
		if !now.Before(e.expires) {
			delete(q.dedup, k)
		}
	}
}

// Receive picks up to max visible messages and hides them for visibility.
// Messages that have already been received maxReceiveCount times move to the
// dead-letter queue instead of being returned.
func (b *Broker) Receive(cfg *queueConfig, max int, visibility time.Duration) []message {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	q := b.state(cfg.key)
	q.compact(now, cfg.retention)

	// On a FIFO queue a message group with a message in flight, or with an
	// earlier message not yet visible, is blocked so order holds.
	blocked := map[string]bool{}
	if cfg.fifo {
		for _, m := range q.msgs {
			if m.visibleAt.After(now) {
				blocked[m.groupID] = true
			}
		}
	}

	var out []message
	moved := false
	for _, m := range q.msgs {
		if len(out) >= max {
			break
		}
		if m.deleted || m.visibleAt.After(now) || (cfg.fifo && blocked[m.groupID]) {
			continue
		}
		if cfg.dlq != nil && cfg.maxReceive > 0 && m.receiveCount >= cfg.maxReceive {
			b.moveToDLQ(q, m, cfg, now)
			moved = true
			continue
		}
		m.receiveCount++
		if m.firstReceive.IsZero() {
			m.firstReceive = now
		}
		m.visibleAt = now.Add(visibility)
		m.receiptHandle = newReceiptHandle(m.id)
		out = append(out, *m)
	}
	if moved {
		q.compact(now, cfg.retention)
	}
	return out
}

func (b *Broker) moveToDLQ(q *queueState, m *message, cfg *queueConfig, now time.Time) {
	m.deleted = true
	delete(q.byID, m.id)

	d := b.state(cfg.dlq.key)
	moved := &message{
		id:           m.id,
		body:         m.body,
		md5Body:      m.md5Body,
		attrs:        m.attrs,
		md5Attrs:     m.md5Attrs,
		sent:         m.sent,
		visibleAt:    now,
		groupID:      m.groupID,
		dedupID:      m.dedupID,
		senderID:     m.senderID,
		dlqSourceArn: cfg.arn,
	}
	if cfg.dlq.fifo {
		d.seq++
		moved.seq = seqNumber(d.seq)
	}
	b.enqueue(d, moved)
}

// seqNumber formats a FIFO sequence number: 20 digits, increasing per queue.
func seqNumber(n uint64) string {
	return fmt.Sprintf("1%019d", n)
}

// Delete removes the message a receipt handle names. A handle for a message
// that is already gone succeeds, as in SQS.
func (b *Broker) Delete(k queueKey, handle string) *apiError {
	id, ok := messageIDFromHandle(handle)
	if !ok {
		return errReceiptHandle(handle)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.state(k)
	if m := q.byID[id]; m != nil {
		m.deleted = true
		delete(q.byID, id)
	}
	return nil
}

func (b *Broker) ChangeVisibility(k queueKey, handle string, timeout time.Duration) *apiError {
	id, ok := messageIDFromHandle(handle)
	if !ok {
		return errReceiptHandle(handle)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	q := b.state(k)
	m := q.byID[id]
	if m == nil {
		return errReceiptHandle(handle)
	}
	if m.receiveCount == 0 || !m.visibleAt.After(now) {
		return newErr("MessageNotInflight", "AWS.SimpleQueueService.MessageNotInflight", "Value %s for parameter ReceiptHandle is invalid. Reason: Message does not exist or is not available for visibility timeout change.", handle)
	}
	m.visibleAt = now.Add(timeout)
	if timeout == 0 {
		q.wake()
	}
	return nil
}

// Purge deletes every message; SQS allows one purge a minute per queue.
func (b *Broker) Purge(cfg *queueConfig) *apiError {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	q := b.state(cfg.key)
	if !q.purgedAt.IsZero() && now.Sub(q.purgedAt) < time.Minute {
		return newErr("PurgeQueueInProgress", "AWS.SimpleQueueService.PurgeQueueInProgress", "Only one PurgeQueue operation on %s is allowed every 60 seconds.", cfg.key.name)
	}
	q.purgedAt = now
	for _, m := range q.msgs {
		m.deleted = true
	}
	q.msgs = nil
	q.byID = map[string]*message{}
	return nil
}

type queueCounts struct{ visible, inflight, delayed int }

func (b *Broker) Counts(cfg *queueConfig) queueCounts {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	q := b.state(cfg.key)
	q.compact(now, cfg.retention)
	var c queueCounts
	for _, m := range q.msgs {
		switch {
		case !m.visibleAt.After(now):
			c.visible++
		case m.receiveCount > 0:
			c.inflight++
		default:
			c.delayed++
		}
	}
	return c
}

// waitChan is closed the next time a message arrives in the queue or is made
// visible again.
func (b *Broker) waitChan(k queueKey) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state(k).notify
}
