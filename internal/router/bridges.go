// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"

	"opensnack/internal/api/apigatewayv2"
	"opensnack/internal/api/cloudfront"
	"opensnack/internal/api/events"
	"opensnack/internal/api/lambda"
	"opensnack/internal/api/logs"
	"opensnack/internal/api/s3"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// Services that act on other services (API Gateway and EventBridge invoking
// Lambda, Lambda writing CloudWatch Logs, CloudFront reading S3) are joined
// here, so no service package imports another's internals. Calls between
// handlers go through the target's public HTTP API in process, as the
// tenant that owns the calling resource.

// inProcess calls h as if a client in the given tenant had sent the request.
func inProcess(ctx context.Context, h http.Handler, id util.Identity, method, target string, headers map[string]string, body []byte) (int, http.Header, []byte) {
	ctx = util.WithIdentity(ctx, &id)
	req, _ := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if u, err := url.Parse(target); err == nil {
		req.Host = u.Host
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := &bufferedResponse{header: http.Header{}}
	h.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.header, rec.body.Bytes()
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

// lambdaInvoker invokes functions through Lambda's Invoke API.
type lambdaInvoker struct{ h *lambda.Handler }

// FunctionError is a function that ran and failed (X-Amz-Function-Error).
type FunctionError struct {
	Kind    string
	Payload []byte
}

func (e *FunctionError) Error() string {
	return fmt.Sprintf("function error (%s): %s", e.Kind, e.Payload)
}

// invoke runs function (a name or ARN, optionally qualified) synchronously,
// or queues it when async is set.
func (l lambdaInvoker) invoke(ctx context.Context, id util.Identity, function string, payload []byte, async bool) ([]byte, error) {
	invType := "RequestResponse"
	if async {
		invType = "Event"
	}
	id.SigningService = "lambda"
	host := "lambda." + id.Region + ".amazonaws.com"
	status, hdr, body := inProcess(ctx, http.HandlerFunc(l.h.ServeREST), id, http.MethodPost,
		"https://"+host+"/2015-03-31/functions/"+url.PathEscape(function)+"/invocations",
		map[string]string{"X-Amz-Invocation-Type": invType, "Content-Type": "application/json"}, payload)
	if status >= 300 {
		return nil, fmt.Errorf("lambda invoke %s: %d %s: %s", function, status, hdr.Get("X-Amzn-ErrorType"), body)
	}
	if kind := hdr.Get("X-Amz-Function-Error"); kind != "" {
		return nil, &FunctionError{Kind: kind, Payload: body}
	}
	return body, nil
}

// apigwInvoker adapts Lambda for API Gateway's AWS_PROXY integrations.
type apigwInvoker struct{ lambdaInvoker }

func (a apigwInvoker) InvokeLambda(ctx context.Context, in apigatewayv2.LambdaInvocation) ([]byte, error) {
	return a.invoke(ctx, util.Identity{Namespace: in.Namespace, Region: in.Region, AccountID: in.AccountID},
		in.Function, in.Payload, false)
}

// eventsLambda delivers EventBridge events to Lambda targets, asynchronously
// as EventBridge does.
type eventsLambda struct{ lambdaInvoker }

func (e eventsLambda) Deliver(ctx context.Context, scope events.Scope, d events.Delivery) error {
	_, err := e.invoke(ctx, util.Identity{Namespace: scope.Namespace, Region: scope.Region, AccountID: scope.AccountID},
		d.TargetArn, d.Payload, true)
	return err
}

// logsSink writes Lambda's invocation logs to CloudWatch Logs.
type logsSink struct{ store resource.Store }

func (l logsSink) PutLogEvents(ctx context.Context, scope, accountID, group, stream string, evs []lambda.LogEvent) error {
	in := make([]logs.InputEvent, len(evs))
	for i, e := range evs {
		in[i] = logs.InputEvent{Timestamp: e.Timestamp.UnixMilli(), Message: e.Message}
	}
	return logs.Append(l.store, logs.Target{Scope: scope, AccountID: accountID, Group: group, Stream: stream}, in)
}

// s3Objects lets CloudFront read S3 origins with their metadata.
func s3Objects(h *s3.Handler) cloudfront.ObjectReader {
	return cloudfront.ObjectReaderFunc(func(ns, bucket, key string) (*cloudfront.Object, error) {
		o, err := h.ReadObjectInfo(ns, bucket, key)
		if err != nil {
			return nil, err
		}
		return &cloudfront.Object{
			Body:         o.Body,
			ContentType:  o.ContentType,
			ETag:         o.ETag,
			LastModified: o.LastModified,
			CacheControl: o.CacheControl,
		}, nil
	})
}
