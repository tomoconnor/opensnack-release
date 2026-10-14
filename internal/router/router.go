// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"net/http"
	"strings"
	"time"

	"opensnack/internal/auth"
	"opensnack/internal/awsresponses"
	"opensnack/internal/inspect"
	"opensnack/internal/limits"

	"opensnack/internal/api/apigatewayv2"
	"opensnack/internal/api/cloudfront"
	"opensnack/internal/api/cloudwatch"
	"opensnack/internal/api/dynamodb"
	"opensnack/internal/api/ec2"
	"opensnack/internal/api/elasticache"
	"opensnack/internal/api/events"
	"opensnack/internal/api/iam"
	"opensnack/internal/api/kms"
	"opensnack/internal/api/lambda"
	"opensnack/internal/api/logs"
	"opensnack/internal/api/route53"
	"opensnack/internal/api/s3"
	"opensnack/internal/api/s3control"
	"opensnack/internal/api/secretsmanager"
	"opensnack/internal/api/sns"
	"opensnack/internal/api/sqs"
	"opensnack/internal/api/ssm"
	"opensnack/internal/api/sts"
	"opensnack/internal/resource"
)

// IMPORTANT:
// S3 REST routing must match AWS behavior:
//
//	GET    /                   → ListBuckets
//	PUT    /:bucket            → CreateBucket
//	DELETE /:bucket            → DeleteBucket
//	HEAD   /:bucket            → HeadBucket
//	GET    /:bucket?location   → GetBucketLocation
//
// The crucial detail:
// ?location has NO VALUE in AWS requests (it's literally '?location')
// So QueryParam("location") == "" but the parameter *exists*.
// We must check for existence, not value.

// Config is what the router needs beyond the store.
type Config struct {
	// Auth resolves the tenant from the request. Nil means dev mode with no
	// credential source, which is what the tests use.
	Auth *auth.Authenticator
	// InternalToken guards the inspection API under /_opensnack/v1/. Without
	// one the API is only served in dev mode.
	InternalToken string
	// Limits caps request sizes, resources per namespace and request rates.
	// The zero value disables them all.
	Limits limits.Config
	// Clock drives EventBridge event times and its scheduler. Nil means
	// time.Now.
	Clock func() time.Time
	// EventDeliverers replace the EventBridge target deliverers by ARN
	// service ("sqs", "sns", "lambda"). Tests use it.
	EventDeliverers map[string]events.Deliverer
}

// Server is the HTTP handler plus the background work that goes with it.
type Server struct {
	http.Handler
	// Scheduler fires scheduled EventBridge rules. The caller starts and
	// stops it; it is nil when the store can't list across namespaces.
	Scheduler *events.Scheduler
	// Events is the EventBridge handler, for its delivery log.
	Events *events.Handler
	// ESM polls SQS for Lambda event source mappings. Like Scheduler, the
	// caller starts and stops it, and it is nil without an Inspector.
	ESM *ESMPoller
}

// New builds the router in dev mode: every request is accepted and the
// namespace comes from the legacy User-Agent token or the access key ID.
func New(store resource.Store) http.Handler {
	return NewWithConfig(store, Config{})
}

func NewWithConfig(store resource.Store, cfg Config) http.Handler {
	return NewServer(store, cfg)
}

// NewServer builds the router and its background services.
func NewServer(store resource.Store, cfg Config) *Server {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Auth == nil {
		cfg.Auth = &auth.Authenticator{Mode: auth.ModeDev, Now: time.Now}
	}
	cfg.Auth.ExemptPrefixes = append(cfg.Auth.ExemptPrefixes, internalPrefix)

	mux := http.NewServeMux()

	s3h := s3.NewHandler(store)
	sqsh := sqs.NewHandler(store)
	snsh := sns.NewHandler(store)
	// Keys STS issues verify through the authenticator like any other.
	sessions := sts.NewSessions()
	cfg.Auth.AddSource(sessions)
	stsh := sts.NewHandler(store, sessions)
	snsh.RegisterDeliverer("sqs", sqsh)
	iamh := iam.NewHandler(store)
	logsh := logs.NewHandler(store)
	lambdah := lambda.NewHandler(store)
	snsh.RegisterDeliverer("lambda", snsLambda{lambdaInvoker{lambdah}})
	s3ctl := s3control.NewHandler(store)
	dynamoh := dynamodb.NewHandler(store)
	kmsh := kms.NewHandler(store)
	ec2h := ec2.NewHandler(store)
	elasticacheh := elasticache.NewHandler(store)
	secretsmanagerh := secretsmanager.NewHandler(store)
	ssmh := ssm.NewHandler(store)
	route53h := route53.NewHandler(store)
	cloudwatchh := cloudwatch.NewHandler(store)
	cloudwatchh.SetNotifier(snsNotifier{snsh})

	// EventBridge delivers to other services through their public HTTP
	// interfaces, so it doesn't depend on how they work inside.
	eventsh := events.NewHandler(store)
	eventsh.Now = cfg.Clock
	eventsh.Deliverers.Set("sqs", events.SQSDeliverer{Handler: http.HandlerFunc(sqsh.Dispatch)})
	eventsh.Deliverers.Set("sns", events.SNSDeliverer{Handler: http.HandlerFunc(snsh.Dispatch)})
	eventsh.Deliverers.Set("lambda", eventsLambda{lambdaInvoker{lambdah}})
	for svc, d := range cfg.EventDeliverers {
		eventsh.Deliverers.Set(svc, d)
	}

	apigwh := apigatewayv2.NewHandler(store)
	apigwh.Invoker = apigwInvoker{lambdaInvoker{lambdah}}
	lambdah.Logs = logsSink{store}
	go apigwh.SyncAll()
	cfh := cloudfront.NewHandler(store)
	cfh.Objects = s3Objects(s3h)
	// CloudFront in front of API Gateway reaches the execute-api data plane
	// in process.
	cfh.Loopback = func(host string) http.Handler {
		if _, _, ok := apigatewayv2.ParseHost(host); ok {
			return http.HandlerFunc(apigwh.ServeDataPlane)
		}
		return nil
	}

	// Requests that say which service they are for (real AWS host names,
	// X-Amz-Target, the SigV4 scope) go straight to it; the rest fall back
	// to the legacy path routes on mux.
	dispatch := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, internalPrefix) {
			mux.ServeHTTP(w, r)
			return
		}
		service, bucket := serviceFor(r)
		switch service {
		case "s3":
			if bucket != "" {
				r = withVirtualHostBucket(r, bucket)
			}
			s3Handler(s3h)(w, r)
		case "s3-control":
			s3ControlHandler(s3ctl)(w, stripPrefix(r, "/s3-control"))
		case "lambda":
			lambdah.ServeREST(w, r)
		case "route53":
			methods(route53h.Dispatch, "GET", "POST", "DELETE")(w, r)
		case "apigateway":
			if !strings.HasPrefix(r.URL.Path, "/v2/") {
				unsupportedService(w, "apigateway (REST APIs, v1)")
				return
			}
			apigwh.ServeHTTP(w, r)
		case "cloudfront":
			cfh.ServeHTTP(w, r)
		case "sts":
			methods(stsh.Dispatch, "GET", "POST")(w, r)
		case "iam":
			methods(iamh.Dispatch, "GET", "POST")(w, r)
		case "sqs":
			methods(sqsh.Dispatch, "GET", "POST")(w, r)
		case "sns":
			methods(snsh.Dispatch, "GET", "POST")(w, r)
		case "ec2":
			methods(ec2h.Dispatch, "GET", "POST")(w, r)
		case "elasticache":
			methods(elasticacheh.Dispatch, "GET", "POST")(w, r)
		case "kms":
			methods(kmsh.Dispatch, "POST")(w, r)
		case "dynamodb":
			methods(dynamoh.Dispatch, "POST")(w, r)
		case "logs":
			methods(logsh.Dispatch, "POST")(w, r)
		case "cloudwatch":
			methods(cloudwatchh.Dispatch, "GET", "POST")(w, r)
		case "secretsmanager":
			methods(secretsmanagerh.Dispatch, "POST")(w, r)
		case "ssm":
			methods(ssmh.Dispatch, "POST")(w, r)
		case "events":
			methods(eventsh.Dispatch, "POST")(w, r)
		case "":
			mux.ServeHTTP(w, r)
		default:
			unsupportedService(w, service)
		}
	}

	insp, _ := store.(resource.Inspector)
	limiter := limits.New(cfg.Limits, insp)
	authed := cfg.Auth.Middleware(limiter.Middleware(DebugLoggerMiddleware(http.HandlerFunc(dispatch))))
	// The data planes (execute-api, cloudfront.net) are anonymous HTTP, so
	// they go round authentication and the per-tenant limits; they find
	// their tenant from the API or distribution ID in the host name.
	apigwData := DebugLoggerMiddleware(http.HandlerFunc(apigwh.ServeDataPlane))
	cfData := DebugLoggerMiddleware(http.HandlerFunc(cfh.ServeDataPlane))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := apigatewayv2.ParseHost(r.Host); ok {
			apigwData.ServeHTTP(w, r)
			return
		}
		if _, ok := cloudfront.ParseHost(r.Host); ok {
			cfData.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})

	// Helper to parse form values
	parseForm := func(r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" {
			r.ParseForm()
		}
	}

	// Helper to get form/query value
	getFormValue := func(r *http.Request, key string) string {
		parseForm(r)
		if v := r.FormValue(key); v != "" {
			return v
		}
		return r.URL.Query().Get(key)
	}

	// Root route handler - combines query dispatch and S3 routing
	rootHandler := func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Remove leading slash for path processing
		if strings.HasPrefix(path, "/") {
			path = path[1:]
		}

		// If path is empty (root "/"), handle query dispatch or ListBuckets
		if path == "" {
			// JSON-protocol services (KMS, DynamoDB, Logs, SecretsManager, SSM,
			// SQS) are addressed by X-Amz-Target at the root, which is where every
			// AWS SDK and the CLI send them. The per-service paths below are an
			// opensnack convenience, not something a real client ever uses.
			if h := jsonTargetHandler(r, kmsh, dynamoh, logsh, secretsmanagerh, ssmh, sqsh); h != nil {
				if r.Method != "POST" {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				h(w, r)
				return
			}

			if r.Method == "GET" {
				action := getFormValue(r, "Action")
				if action != "" {
					queryDispatch(w, r, stsh, iamh, sqsh, snsh, ec2h, elasticacheh)
					return
				}
				s3h.ListBuckets(w, r)
				return
			}
			if r.Method == "POST" {
				queryDispatch(w, r, stsh, iamh, sqsh, snsh, ec2h, elasticacheh)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Otherwise, delegate to S3 handler logic
		s3Handler(s3h)(w, r)
	}

	mux.HandleFunc("/", rootHandler)

	if insp, ok := store.(resource.Inspector); ok && (cfg.InternalToken != "" || cfg.Auth.Mode == auth.ModeDev) {
		api := &inspect.API{
			Store:   insp,
			Token:   cfg.InternalToken,
			Objects: s3h.ReadObject,
			Cleaners: []inspect.NamespaceCleaner{
				s3.DeleteNamespaceObjects,
				logs.NamespaceCleaner(store),
				cloudwatch.EngineFor(store).DeleteNamespace,
				sqsh.DeleteNamespaceMessages,
			},
			Routes: map[string]http.HandlerFunc{
				"GET namespaces/{ns}/ec2/reachability": ec2h.ReachabilityHandler,
			},
		}
		mux.Handle(inspect.Prefix, api.Handler())
		mux.Handle("GET "+inspect.Prefix+"events/deliveries", eventsh.Log.Handler(cfg.InternalToken))
	}

	mux.HandleFunc(internalPrefix+"health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})

	// STS routes
	mux.HandleFunc("/sts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			stsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/sts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			stsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// IAM routes
	mux.HandleFunc("/iam", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			iamh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/iam/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			iamh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// S3 Control routes
	mux.HandleFunc("/s3-control/", func(w http.ResponseWriter, r *http.Request) {
		s3ControlHandler(s3ctl)(w, stripPrefix(r, "/s3-control"))
	})

	// Lambda routes - must come before S3 wildcard routes
	mux.HandleFunc("/lambda", lambdah.ServeREST)
	mux.HandleFunc("/lambda/", lambdah.ServeREST)
	// GetFunction's Code.Location: presigned-style, so outside SigV4.
	mux.HandleFunc(lambda.CodeDownloadPrefix, lambdah.ServeCodeDownload)

	// SQS routes
	mux.HandleFunc("/sqs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			// Check for JSON API format first
			target := r.Header.Get("X-Amz-Target")
			if target != "" && strings.HasPrefix(target, "AmazonSQS.") {
				sqsh.Dispatch(w, r)
				return
			}
			// Check for Query API format
			parseForm(r)
			version := getFormValue(r, "Version")
			action := getFormValue(r, "Action")
			if version == sqs.APIVersion || isSQSAction(action) {
				sqsh.Dispatch(w, r)
				return
			}
			sqsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// SNS routes
	mux.HandleFunc("/sns", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			snsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// CloudWatch Logs routes
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			logsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// DynamoDB routes
	mux.HandleFunc("/dynamodb", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			dynamoh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/dynamodb/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			dynamoh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// KMS routes
	mux.HandleFunc("/kms", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			kmsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/kms/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			kmsh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// EC2 routes
	mux.HandleFunc("/ec2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			ec2h.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/ec2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			ec2h.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// ElastiCache routes
	mux.HandleFunc("/elasticache", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			elasticacheh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/elasticache/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" {
			elasticacheh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// SecretsManager routes
	mux.HandleFunc("/secretsmanager", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			secretsmanagerh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/secretsmanager/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			secretsmanagerh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// SSM routes
	mux.HandleFunc("/ssm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			ssmh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/ssm/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			ssmh.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Route53 routes - REST API format: /route53/2013-04-01/hostedzone
	mux.HandleFunc("/route53/", route53Handler(route53h))

	// // RDS routes
	// mux.HandleFunc("/rds", func(w http.ResponseWriter, r *http.Request) {
	// 	if r.Method == "GET" || r.Method == "POST" {
	// 		rdsh.Dispatch(w, r)
	// 	} else {
	// 		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	// 	}
	// })
	// mux.HandleFunc("/rds/", func(w http.ResponseWriter, r *http.Request) {
	// 	if r.Method == "GET" || r.Method == "POST" {
	// 		rdsh.Dispatch(w, r)
	// 	} else {
	// 		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	// 	}
	// })

	// S3 routes are handled by rootHandler above

	srv := &Server{Handler: handler, Events: eventsh}
	if insp != nil {
		srv.Scheduler = events.NewScheduler(eventsh, insp)
		srv.Scheduler.Now = cfg.Clock
		srv.ESM = newESMPoller(insp, lambdah, http.HandlerFunc(sqsh.Dispatch))
	}
	return srv
}

// internalPrefix is opensnack's own API (health, and the inspection API for
// external checkers). It sits outside AWS authentication and checks its own token.
const internalPrefix = "/_opensnack/"

// jsonTargetHandler maps an X-Amz-Target header to the handler that owns that
// service, or nil when the request is not a JSON-protocol call.
func jsonTargetHandler(
	r *http.Request,
	kmsh *kms.Handler,
	ddbh *dynamodb.Handler,
	logsh *logs.Handler,
	smh *secretsmanager.Handler,
	ssmh *ssm.Handler,
	sqsh *sqs.Handler,
) http.HandlerFunc {
	target := r.Header.Get("X-Amz-Target")
	if target == "" {
		return nil
	}

	switch {
	case strings.HasPrefix(target, "TrentService."):
		return kmsh.Dispatch
	case strings.HasPrefix(target, "DynamoDB_"):
		return ddbh.Dispatch
	case strings.HasPrefix(target, "Logs_"):
		return logsh.Dispatch
	case strings.HasPrefix(target, "secretsmanager."):
		return smh.Dispatch
	case strings.HasPrefix(target, "AmazonSSM."):
		return ssmh.Dispatch
	// SQS moved from the Query protocol to JSON in 2023; AWS CLI v2 and the
	// current SDKs only speak this form. The Query path below still serves the
	// older clients that send Action/Version form bodies.
	case strings.HasPrefix(target, "AmazonSQS."):
		return sqsh.Dispatch
	}
	return nil
}

func queryDispatch(w http.ResponseWriter, r *http.Request, stsh *sts.Handler, iamh *iam.Handler, sqsh *sqs.Handler, snsh *sns.Handler, ec2h *ec2.Handler, elasticacheh *elasticache.Handler) {
	r.ParseForm()
	action := r.FormValue("Action")
	if action == "" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	version := r.FormValue("Version")
	switch version {
	case sts.APIVersion:
		stsh.Dispatch(w, r)
	case iam.APIVersion:
		iamh.Dispatch(w, r)
	case sqs.APIVersion:
		sqsh.Dispatch(w, r)
	case sns.APIVersion:
		snsh.Dispatch(w, r)
	case ec2.APIVersion:
		ec2h.Dispatch(w, r)
	case elasticache.APIVersion:
		elasticacheh.Dispatch(w, r)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// s3Handler serves the S3 REST API; the s3 package routes it by method,
// path and sub-resource.
func s3Handler(s3h *s3.Handler) http.HandlerFunc {
	return s3h.Dispatch
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// route53Handler handles Route53 REST API routes
func route53Handler(route53h *route53.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "POST" || r.Method == "DELETE" {
			route53h.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// isSQSAction checks if the action is an SQS action
func isSQSAction(action string) bool {
	sqsActions := []string{
		"CreateQueue",
		"ListQueues",
		"GetQueueUrl",
		"DeleteQueue",
		"SendMessage",
		"ReceiveMessage",
		"DeleteMessage",
		"GetQueueAttributes",
		"SetQueueAttributes",
	}
	for _, a := range sqsActions {
		if action == a {
			return true
		}
	}
	return false
}

// s3ControlHandler routes the S3 Control REST API (paths under /v20180820/).
func s3ControlHandler(s3ctl *s3control.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v20180820/tags") {
			s3ctl.Tags(w, r)
			return
		}
		awsresponses.WriteS3ErrorXML(w, http.StatusNotImplemented, "NotImplemented",
			"opensnack does not implement this S3 Control operation", r.URL.Path)
	}
}

// unsupportedService answers requests for AWS services opensnack doesn't
// emulate, rather than letting them fall through to S3.
func unsupportedService(w http.ResponseWriter, service string) {
	awsresponses.WriteErrorXML(w, http.StatusBadRequest, "UnsupportedService",
		"opensnack does not implement the "+service+" service", "")
}
