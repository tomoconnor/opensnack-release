// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

import (
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"opensnack/internal/resource"
)

// Log events don't live in the resources table: a busy stream would turn
// every PutLogEvents into a JSONB rewrite and the inspection API's resource counts would
// fill up with events. With Postgres behind the store they go in their own
// append-only table, log_events (see pgEventStore), indexed for the
// (group, stream, time) reads GetLogEvents and FilterLogEvents do. Without
// Postgres (tests, the MemStore) they are kept in memory, capped at
// memStreamCap events per stream, oldest dropped first.

// Event is one stored log event.
type Event struct {
	Stream        string
	Timestamp     int64 // ms since the epoch, as the client sent it
	IngestionTime int64 // ms since the epoch
	Message       string
	// Seq orders events that share a timestamp, in ingestion order.
	Seq int64
}

func (e Event) pos() cursor { return cursor{e.Timestamp, e.Seq} }

// cursor is a position in (timestamp, seq) order; tokens encode one.
type cursor struct{ ts, seq int64 }

func (a cursor) less(b cursor) bool {
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	return a.seq < b.seq
}

// EventQuery selects events from one log group.
type EventQuery struct {
	Scope, Group string
	Streams      []string // nil means every stream
	StreamPrefix string
	Start, End   *int64 // ms; Start inclusive, End exclusive
	After        *cursor
	Before       *cursor
	Desc         bool // newest first (the result is still capped by Limit)
	Limit        int
}

// StreamStats is what DescribeLogStreams reports about a stream's events.
type StreamStats struct {
	First, Last, LastIngestion int64
	Bytes, Count               int64
}

// EventStore keeps log events outside the resource store.
type EventStore interface {
	Put(scope, group string, events []Event) error
	Query(q EventQuery) ([]Event, error)
	Stats(scope, group string) (map[string]StreamStats, error)
	DeleteGroup(scope, group string) error
	DeleteStream(scope, group, stream string) error
	// DeleteNamespace removes a tenant's events in every region.
	DeleteNamespace(namespace string) error
	// Prune drops events with a timestamp before the cutoff (retention).
	Prune(scope, group string, before int64) error
}

var eventStores sync.Map // resource.Store -> EventStore

// EventStoreFor returns the event store that goes with a resource store: a
// Postgres table when the store is database-backed, memory otherwise.
func EventStoreFor(store resource.Store) EventStore {
	if v, ok := eventStores.Load(store); ok {
		return v.(EventStore)
	}
	var es EventStore
	if g, ok := store.(interface{ DB() *gorm.DB }); ok && g.DB() != nil {
		pg, err := newPGEventStore(g.DB())
		if err != nil {
			zap.L().Error("log events: falling back to memory", zap.Error(err))
			es = newMemEventStore()
		} else {
			es = pg
		}
	} else {
		es = newMemEventStore()
	}
	v, _ := eventStores.LoadOrStore(store, es)
	return v.(EventStore)
}

// NamespaceCleaner deletes a tenant's log events, for the inspection API's
// namespace delete.
func NamespaceCleaner(store resource.Store) func(namespace string) error {
	return func(ns string) error { return EventStoreFor(store).DeleteNamespace(ns) }
}

//
// In-memory store
//

const memStreamCap = 50000

type memKey struct{ scope, group string }

type memEventStore struct {
	mu     sync.RWMutex
	seq    int64
	groups map[memKey]map[string][]Event
}

func newMemEventStore() *memEventStore {
	return &memEventStore{groups: map[memKey]map[string][]Event{}}
}

func (m *memEventStore) Put(scope, group string, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey{scope, group}
	streams := m.groups[k]
	if streams == nil {
		streams = map[string][]Event{}
		m.groups[k] = streams
	}
	touched := map[string]bool{}
	for _, e := range events {
		m.seq++
		e.Seq = m.seq
		streams[e.Stream] = append(streams[e.Stream], e)
		touched[e.Stream] = true
	}
	for s := range touched {
		evs := streams[s]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].pos().less(evs[j].pos()) })
		if len(evs) > memStreamCap {
			evs = append([]Event(nil), evs[len(evs)-memStreamCap:]...)
		}
		streams[s] = evs
	}
	return nil
}

func (m *memEventStore) Query(q EventQuery) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := map[string]bool{}
	for _, s := range q.Streams {
		want[s] = true
	}
	var out []Event
	for name, evs := range m.groups[memKey{q.Scope, q.Group}] {
		if len(want) > 0 && !want[name] {
			continue
		}
		if q.StreamPrefix != "" && !strings.HasPrefix(name, q.StreamPrefix) {
			continue
		}
		for _, e := range evs {
			if q.Start != nil && e.Timestamp < *q.Start {
				continue
			}
			if q.End != nil && e.Timestamp >= *q.End {
				continue
			}
			if q.After != nil && !q.After.less(e.pos()) {
				continue
			}
			if q.Before != nil && !e.pos().less(*q.Before) {
				continue
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if q.Desc {
			return out[j].pos().less(out[i].pos())
		}
		return out[i].pos().less(out[j].pos())
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (m *memEventStore) Stats(scope, group string) (map[string]StreamStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]StreamStats{}
	for name, evs := range m.groups[memKey{scope, group}] {
		if len(evs) == 0 {
			continue
		}
		st := StreamStats{First: evs[0].Timestamp, Last: evs[len(evs)-1].Timestamp, Count: int64(len(evs))}
		for _, e := range evs {
			if e.IngestionTime > st.LastIngestion {
				st.LastIngestion = e.IngestionTime
			}
			st.Bytes += int64(len(e.Message))
		}
		out[name] = st
	}
	return out, nil
}

func (m *memEventStore) DeleteGroup(scope, group string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, memKey{scope, group})
	return nil
}

func (m *memEventStore) DeleteStream(scope, group, stream string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups[memKey{scope, group}], stream)
	return nil
}

func (m *memEventStore) DeleteNamespace(ns string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.groups {
		if n, _ := resource.SplitScope(k.scope); n == ns {
			delete(m.groups, k)
		}
	}
	return nil
}

func (m *memEventStore) Prune(scope, group string, before int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, evs := range m.groups[memKey{scope, group}] {
		i := 0
		for i < len(evs) && evs[i].Timestamp < before {
			i++
		}
		if i > 0 {
			m.groups[memKey{scope, group}][name] = append([]Event(nil), evs[i:]...)
		}
	}
	return nil
}

//
// Postgres store
//

type pgEventStore struct {
	db *gorm.DB
}

// logEventRow is a row of log_events. id is the Seq.
type logEventRow struct {
	ID            int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Namespace     string `gorm:"column:namespace"`
	Region        string `gorm:"column:region"`
	LogGroup      string `gorm:"column:log_group"`
	LogStream     string `gorm:"column:log_stream"`
	Ts            int64  `gorm:"column:ts"`
	IngestionTime int64  `gorm:"column:ingestion_time"`
	Message       string `gorm:"column:message"`
}

func (logEventRow) TableName() string { return "log_events" }

var pgSchema = []string{
	`CREATE TABLE IF NOT EXISTS public.log_events (
		id bigserial PRIMARY KEY,
		namespace text NOT NULL,
		region text NOT NULL,
		log_group text NOT NULL,
		log_stream text NOT NULL,
		ts bigint NOT NULL,
		ingestion_time bigint NOT NULL,
		message text NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS log_events_stream_ts
	 ON public.log_events (namespace, region, log_group, log_stream, ts, id)`,
	`CREATE INDEX IF NOT EXISTS log_events_group_ts
	 ON public.log_events (namespace, region, log_group, ts, id)`,
}

func newPGEventStore(db *gorm.DB) (*pgEventStore, error) {
	for _, stmt := range pgSchema {
		if err := db.Exec(stmt).Error; err != nil {
			return nil, err
		}
	}
	return &pgEventStore{db: db}, nil
}

func (p *pgEventStore) scoped(scope, group string) *gorm.DB {
	ns, region := resource.SplitScope(scope)
	return p.db.Model(&logEventRow{}).
		Where("namespace = ? AND region = ? AND log_group = ?", ns, region, group)
}

func (p *pgEventStore) Put(scope, group string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	ns, region := resource.SplitScope(scope)
	rows := make([]logEventRow, len(events))
	for i, e := range events {
		rows[i] = logEventRow{
			Namespace: ns, Region: region, LogGroup: group, LogStream: e.Stream,
			Ts: e.Timestamp, IngestionTime: e.IngestionTime, Message: e.Message,
		}
	}
	return p.db.CreateInBatches(rows, 1000).Error
}

func (p *pgEventStore) Query(q EventQuery) ([]Event, error) {
	db := p.scoped(q.Scope, q.Group)
	if q.Streams != nil {
		db = db.Where("log_stream IN ?", q.Streams)
	}
	if q.StreamPrefix != "" {
		db = db.Where("log_stream LIKE ?", likePrefix(q.StreamPrefix))
	}
	if q.Start != nil {
		db = db.Where("ts >= ?", *q.Start)
	}
	if q.End != nil {
		db = db.Where("ts < ?", *q.End)
	}
	if q.After != nil {
		db = db.Where("(ts, id) > (?, ?)", q.After.ts, q.After.seq)
	}
	if q.Before != nil {
		db = db.Where("(ts, id) < (?, ?)", q.Before.ts, q.Before.seq)
	}
	if q.Desc {
		db = db.Order("ts DESC, id DESC")
	} else {
		db = db.Order("ts, id")
	}
	if q.Limit > 0 {
		db = db.Limit(q.Limit)
	}
	var rows []logEventRow
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Event, len(rows))
	for i, r := range rows {
		out[i] = Event{Stream: r.LogStream, Timestamp: r.Ts, IngestionTime: r.IngestionTime, Message: r.Message, Seq: r.ID}
	}
	return out, nil
}

func likePrefix(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s) + "%"
}

func (p *pgEventStore) Stats(scope, group string) (map[string]StreamStats, error) {
	var rows []struct {
		LogStream     string
		First, Last   int64
		LastIngestion int64
		Bytes, Count  int64
	}
	err := p.scoped(scope, group).
		Select("log_stream, min(ts) AS first, max(ts) AS last, max(ingestion_time) AS last_ingestion, " +
			"coalesce(sum(octet_length(message)), 0) AS bytes, count(*) AS count").
		Group("log_stream").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[string]StreamStats{}
	for _, r := range rows {
		out[r.LogStream] = StreamStats{First: r.First, Last: r.Last, LastIngestion: r.LastIngestion, Bytes: r.Bytes, Count: r.Count}
	}
	return out, nil
}

func (p *pgEventStore) DeleteGroup(scope, group string) error {
	return p.scoped(scope, group).Delete(&logEventRow{}).Error
}

func (p *pgEventStore) DeleteStream(scope, group, stream string) error {
	return p.scoped(scope, group).Where("log_stream = ?", stream).Delete(&logEventRow{}).Error
}

func (p *pgEventStore) DeleteNamespace(ns string) error {
	return p.db.Where("namespace = ?", ns).Delete(&logEventRow{}).Error
}

func (p *pgEventStore) Prune(scope, group string, before int64) error {
	return p.scoped(scope, group).Where("ts < ?", before).Delete(&logEventRow{}).Error
}
