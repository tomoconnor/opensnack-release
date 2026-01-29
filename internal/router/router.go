// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"net/http"
	"strings"

	"opensnack/internal/api/dynamodb"
	"opensnack/internal/api/ec2"
	"opensnack/internal/api/elasticache"
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
func New(store resource.Store) http.Handler {
	mux := http.NewServeMux()

	s3h := s3.NewHandler(store)
	sqsh := sqs.NewHandler(store)
	snsh := sns.NewHandler(store)
	stsh := sts.NewHandler()
	iamh := iam.NewHandler(store)
	logsh := logs.NewHandler(store)
	lambdah := lambda.NewHandler(store)
	s3ctl := s3control.NewHandler(store)
	dynamoh := dynamodb.NewHandler(store)
	kmsh := kms.NewHandler(store)
	ec2h := ec2.NewHandler(store)
	elasticacheh := elasticache.NewHandler(store)
	secretsmanagerh := secretsmanager.NewHandler(store)
	ssmh := ssm.NewHandler(store)
	route53h := route53.NewHandler(store)

	// Apply middleware
	handler := DebugLoggerMiddleware(SigV4Middleware(mux))

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
			if r.Method == "GET" {
				action := getFormValue(r, "Action")
				if action != "" {
					queryDispatch(w, r, stsh, iamh, sqsh, snsh)
					return
				}
				s3h.ListBuckets(w, r)
				return
			}
			if r.Method == "POST" {
				queryDispatch(w, r, stsh, iamh, sqsh, snsh)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Otherwise, delegate to S3 handler logic
		s3Handler(s3h)(w, r)
	}

	mux.HandleFunc("/", rootHandler)

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
		s3ctl.ListTagsForResource(w, r)
	})

	// Lambda routes - must come before S3 wildcard routes
	mux.HandleFunc("/lambda", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			lambdah.Dispatch(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/lambda/", lambdaHandler(lambdah))

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

	return handler
}

func queryDispatch(w http.ResponseWriter, r *http.Request, stsh *sts.Handler, iamh *iam.Handler, sqsh *sqs.Handler, snsh *sns.Handler) {
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
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func lambdaHandler(lambdah *lambda.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		// Handle code-signing-config routes
		if strings.Contains(path, "/code-signing-config") {
			if method == "GET" || method == "HEAD" || method == "OPTIONS" {
				// Extract function name from path
				parts := strings.Split(path, "/")
				for i, part := range parts {
					if part == "functions" && i+1 < len(parts) {
						// Create a request with function name in context or modify path
						lambdah.GetFunctionCodeSigningConfigREST(w, r)
						return
					}
				}
			}
			if method == "POST" {
				lambdah.Dispatch(w, r)
				return
			}
		}

		// Handle specific Lambda REST routes
		if strings.HasSuffix(path, "/configuration") && method == "GET" {
			lambdah.GetFunctionConfigurationREST(w, r)
			return
		}
		if strings.HasSuffix(path, "/versions") && method == "GET" {
			lambdah.ListVersionsByFunctionREST(w, r)
			return
		}
		if strings.Contains(path, "/functions/") && method == "GET" && !strings.Contains(path, "/code-signing-config") && !strings.Contains(path, "/configuration") && !strings.Contains(path, "/versions") {
			lambdah.GetFunctionREST(w, r)
			return
		}
		if strings.Contains(path, "/functions/") && method == "DELETE" {
			lambdah.DeleteFunctionREST(w, r)
			return
		}

		// Default: try Dispatch for POST, otherwise 405
		if method == "POST" {
			lambdah.Dispatch(w, r)
			return
		}

		http.Error(w, "Method "+method+" not allowed for "+path, http.StatusMethodNotAllowed)
	}
}

func s3Handler(s3h *s3.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		// Remove leading slash
		if strings.HasPrefix(path, "/") {
			path = path[1:]
		}

		// Split path into bucket and key
		parts := strings.SplitN(path, "/", 2)
		bucket := parts[0]
		key := ""
		if len(parts) > 1 {
			key = parts[1]
		}

		// Handle root bucket operations
		if bucket == "" {
			if method == "GET" {
				s3h.ListBuckets(w, r)
			}
			return
		}

		// Handle bucket-level operations (no key)
		if key == "" {
			query := r.URL.Query()
			if method == "PUT" {
				// Check for query parameters
				if _, exists := query["versioning"]; exists {
					s3h.PutBucketVersioning(w, r)
					return
				}
				if _, exists := query["lifecycle"]; exists {
					s3h.PutBucketLifecycleConfiguration(w, r)
					return
				}
				if _, exists := query["acl"]; exists {
					s3h.PutBucketAcl(w, r)
					return
				}
				if _, exists := query["policy"]; exists {
					s3h.PutBucketPolicy(w, r)
					return
				}
				s3h.CreateBucket(w, r)
				return
			}
			if method == "DELETE" {
				s3h.DeleteBucket(w, r)
				return
			}
			if method == "HEAD" {
				s3h.HeadBucket(w, r)
				return
			}
			if method == "GET" {
				// Check for query parameters
				if _, exists := query["versioning"]; exists {
					s3h.GetBucketVersioning(w, r)
					return
				}
				if _, exists := query["lifecycle"]; exists {
					s3h.GetBucketLifecycleConfiguration(w, r)
					return
				}
				if _, exists := query["acl"]; exists {
					s3h.GetBucketAcl(w, r)
					return
				}
				if _, exists := query["policy"]; exists {
					s3h.GetBucketPolicy(w, r)
					return
				}
				if _, exists := query["location"]; exists {
					s3h.GetBucketLocation(w, r)
					return
				}
				// GET /bucket (no query params) is not valid
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Handle object-level operations (has key)
		switch method {
		case "PUT":
			s3h.PutObject(w, r)
		case "GET":
			s3h.GetObject(w, r)
		case "HEAD":
			s3h.HeadObject(w, r)
		case "DELETE":
			s3h.DeleteObject(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
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
