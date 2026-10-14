// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"opensnack/internal/resource"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Scheduler fires scheduled rules in every namespace. Each Tick lists the
// enabled rules that have a ScheduleExpression (one indexed query across
// all tenants), and fires those whose next time has come.
//
// A rule's first fire is one period after the scheduler first sees it, so
// rate(5 minutes) fires five minutes after it is created, as on AWS.
type Scheduler struct {
	Handler   *Handler
	Inspector resource.Inspector
	// Now is the clock; tests inject a fake one and call Tick.
	Now      func() time.Time
	Interval time.Duration

	mu     sync.Mutex
	next   map[string]time.Time
	cancel context.CancelFunc
	done   chan struct{}
}

func NewScheduler(h *Handler, insp resource.Inspector) *Scheduler {
	return &Scheduler{Handler: h, Inspector: insp, Now: time.Now, Interval: 5 * time.Second, next: map[string]time.Time{}}
}

// Start runs the loop in the background until Stop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.Inspector == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(s.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Tick(ctx)
			}
		}
	}()
}

// Stop ends the loop and waits for an in-flight tick.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Tick fires every scheduled rule that is due at Now and returns how many
// fired.
func (s *Scheduler) Tick(ctx context.Context) int {
	now := s.Now().UTC()
	rows, err := s.Inspector.Find(resource.Filter{Service: service, Type: typeRule, HasAttr: scheduleAttr})
	if err != nil {
		zap.L().Warn("events scheduler: listing scheduled rules", zap.Error(err))
		return 0
	}
	type due struct {
		scope Scope
		rule  ruleDoc
		at    time.Time
	}
	var fire []due

	s.mu.Lock()
	seen := map[string]bool{}
	for _, row := range rows {
		var d ruleDoc
		if json.Unmarshal(row.Attributes, &d) != nil || d.ScheduleExpression == nil || d.State == stateDisabled {
			continue
		}
		sched, err := ParseSchedule(*d.ScheduleExpression)
		if err != nil {
			continue
		}
		ns, region := resource.SplitScope(row.Namespace)
		if region == "" {
			region = row.Region
		}
		key := ns + "\x00" + region + "\x00" + row.ID + "\x00" + *d.ScheduleExpression
		seen[key] = true
		next, ok := s.next[key]
		if !ok {
			s.next[key] = sched.Next(now)
			continue
		}
		if now.Before(next) {
			continue
		}
		_, account, _, _ := arnParts(d.Arn)
		fire = append(fire, due{Scope{Namespace: ns, Region: region, AccountID: account}, d, next})
		// Missed periods collapse into this one fire.
		for !next.IsZero() && !next.After(now) {
			next = sched.Next(next)
		}
		s.next[key] = next
	}
	for k := range s.next {
		if !seen[k] {
			delete(s.next, k)
		}
	}
	s.mu.Unlock()

	for _, f := range fire {
		env := Envelope{
			Version: "0", ID: uuid.NewString(), DetailType: "Scheduled Event", Source: "aws.events",
			Account: f.scope.AccountID, Time: f.at.Format(time.RFC3339), Region: f.scope.Region,
			Resources: []string{f.rule.Arn}, Detail: json.RawMessage("{}"),
		}
		s.Handler.fanOut(ctx, f.scope, []ruleDoc{f.rule}, env, true)
	}
	return len(fire)
}
