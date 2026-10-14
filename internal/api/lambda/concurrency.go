// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
)

//
// CONCURRENCY
//
// Invocations in flight are capped per tenant namespace
// (OPENSNACK_LAMBDA_CONCURRENCY_PER_NAMESPACE, default 2; 0 for no cap)
// and per function by its ReservedConcurrentExecutions. An executor may
// also run out of room (KubernetesExecutor's cap on sandbox pods) and
// return ErrThrottled. Either way a synchronous caller gets
// TooManyRequestsException; asynchronous invocations are retried with
// backoff, as Lambda's internal queue does.
//

// ErrThrottled is returned by an executor with no capacity for another
// sandbox.
var ErrThrottled = errors.New("no capacity to run the function")

// throttleError is TooManyRequestsException, with the Reason Lambda gives.
type throttleError struct {
	Reason  string
	Message string
}

func (e *throttleError) Error() string { return "TooManyRequestsException: " + e.Message }

func writeThrottle(w http.ResponseWriter, e *throttleError) {
	w.Header().Set("X-Amzn-ErrorType", "TooManyRequestsException")
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"Type": "User", "message": e.Message, "Reason": e.Reason})
}

func throttled(reason string) *throttleError {
	return &throttleError{Reason: reason, Message: "Rate Exceeded."}
}

// concurrency counts invocations in flight.
type concurrency struct {
	mu           sync.Mutex
	perNamespace int
	byNamespace  map[string]int
	byFunction   map[string]int
}

func newConcurrency(perNamespace int) *concurrency {
	return &concurrency{perNamespace: perNamespace, byNamespace: map[string]int{}, byFunction: map[string]int{}}
}

func concurrencyFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv("OPENSNACK_LAMBDA_CONCURRENCY_PER_NAMESPACE")); err == nil && v >= 0 {
		return v
	}
	return 2
}

// acquire admits inv or says why not. release must be called once the
// invocation is over.
func (c *concurrency) acquire(inv *Invocation) (release func(), err *throttleError) {
	fnKey := inv.Namespace + "\x00" + inv.Region + "\x00" + inv.FunctionName
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := inv.ReservedConcurrency; r != nil && c.byFunction[fnKey] >= *r {
		return nil, throttled("ReservedFunctionConcurrentInvocationLimitExceeded")
	}
	if c.perNamespace > 0 && c.byNamespace[inv.Namespace] >= c.perNamespace {
		return nil, throttled("ConcurrentInvocationLimitExceeded")
	}
	c.byNamespace[inv.Namespace]++
	c.byFunction[fnKey]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.byNamespace[inv.Namespace]--; c.byNamespace[inv.Namespace] <= 0 {
				delete(c.byNamespace, inv.Namespace)
			}
			if c.byFunction[fnKey]--; c.byFunction[fnKey] <= 0 {
				delete(c.byFunction, fnKey)
			}
		})
	}, nil
}

//
// ASYNCHRONOUS INVOCATION
//
// An Event invocation is queued in memory. Throttles and executor failures
// are retried with backoff until the event is MaximumEventAgeInSeconds old
// (default 6 hours); a function error is retried MaximumRetryAttempts times
// (default 2), a minute and then two minutes apart, as Lambda does. The
// queue is per process: a restart loses it. OPENSNACK_LAMBDA_ASYNC_QUEUE_PER_NAMESPACE
// (default 100) caps the events a namespace can have waiting.
//

type asyncPolicy struct {
	MaxRetries int
	MaxAge     time.Duration
}

// AsyncTiming sets the asynchronous retry delays. Tests shorten them.
type AsyncTiming struct {
	// RetryDelay is the wait before the first retry of a function error;
	// each later retry waits twice as long.
	RetryDelay time.Duration
	// ThrottleBackoff is the first wait after a throttle, doubling up to
	// MaxThrottleBackoff.
	ThrottleBackoff    time.Duration
	MaxThrottleBackoff time.Duration
}

var defaultAsyncTiming = AsyncTiming{RetryDelay: time.Minute, ThrottleBackoff: time.Second, MaxThrottleBackoff: 5 * time.Minute}

func asyncQueueFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv("OPENSNACK_LAMBDA_ASYNC_QUEUE_PER_NAMESPACE")); err == nil && v > 0 {
		return v
	}
	return 100
}

// runAsync works an Event invocation through to a result or gives up.
func (h *Handler) runAsync(inv *Invocation, policy asyncPolicy, stop <-chan struct{}) {
	queued := time.Now()
	timing := h.AsyncTiming
	backoff := timing.ThrottleBackoff
	retries := 0
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-stop:
			return false
		}
	}
	for {
		if time.Since(queued) > policy.MaxAge {
			zap.L().Warn("async lambda invocation dropped: older than its maximum event age",
				zap.String("function", inv.InvokedARN), zap.String("request_id", inv.RequestID))
			return
		}
		res, err := h.run(context.Background(), inv)
		if err != nil {
			var te *throttleError
			var ae *apiError
			switch {
			case errors.As(err, &ae), errors.Is(err, ErrUnsupportedRuntime):
				zap.L().Warn("async lambda invocation failed", zap.String("function", inv.InvokedARN), zap.Error(err))
				return
			case !errors.As(err, &te) && !errors.Is(err, ErrThrottled):
				zap.L().Info("async lambda invocation failed, will retry", zap.String("function", inv.InvokedARN), zap.Error(err))
			}
			// Jitter so a burst of throttled events doesn't retry in step.
			d := backoff/2 + rand.N(backoff/2+1)
			if backoff *= 2; backoff > timing.MaxThrottleBackoff {
				backoff = timing.MaxThrottleBackoff
			}
			if !wait(d) {
				return
			}
			continue
		}
		// Retries keep the request ID, as in Lambda.
		if res.FunctionError != "" && retries < policy.MaxRetries {
			if !wait(timing.RetryDelay << retries) {
				return
			}
			retries++
			continue
		}
		return
	}
}
