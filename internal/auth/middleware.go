// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/util"

	"go.uber.org/zap"
)

// Mode decides what happens to requests that don't verify.
type Mode string

const (
	// ModeDev accepts every request. The namespace is the legacy
	// "custom-" User-Agent token if present, else the access key ID, else
	// "default". Known keys still get their account ID.
	ModeDev Mode = "dev"
	// ModeStrict rejects unsigned requests, unknown keys and bad signatures
	// with the AWS error codes.
	ModeStrict Mode = "strict"
)

// Authenticator resolves each request to an Identity.
type Authenticator struct {
	Mode   Mode
	Source Source // may be nil in dev mode
	Now    func() time.Time
	// Exempt paths skip authentication (the internal API authenticates
	// itself; data planes like CloudFront are anonymous).
	ExemptPrefixes []string
}

// FromEnv builds the authenticator from:
//
//	OPENSNACK_AUTH_MODE         dev (default) | strict
//	OPENSNACK_CREDENTIALS_FILE  JSON list of credentials (see TableSource)
//	OPENSNACK_CREDENTIALS_URL   HTTP lookup endpoint (see HTTPSource)
//	OPENSNACK_CREDENTIALS_TOKEN bearer token for that endpoint
//
// Both sources may be set; the file is consulted first.
func FromEnv() (*Authenticator, error) {
	a := &Authenticator{Mode: ModeDev, Now: time.Now}
	switch m := Mode(strings.ToLower(os.Getenv("OPENSNACK_AUTH_MODE"))); m {
	case "", ModeDev:
	case ModeStrict:
		a.Mode = ModeStrict
	default:
		return nil, fmt.Errorf("OPENSNACK_AUTH_MODE: unknown mode %q", m)
	}

	var chain ChainSource
	if path := os.Getenv("OPENSNACK_CREDENTIALS_FILE"); path != "" {
		t, err := LoadTableSource(path)
		if err != nil {
			return nil, err
		}
		chain = append(chain, t)
	}
	if u := os.Getenv("OPENSNACK_CREDENTIALS_URL"); u != "" {
		chain = append(chain, NewHTTPSource(u, os.Getenv("OPENSNACK_CREDENTIALS_TOKEN")))
	}
	if len(chain) > 0 {
		a.Source = chain
	}
	if a.Mode == ModeStrict && a.Source == nil {
		return nil, errors.New("strict auth mode needs OPENSNACK_CREDENTIALS_FILE or OPENSNACK_CREDENTIALS_URL")
	}
	return a, nil
}

// AddSource puts s in front of the configured sources (STS registers the
// temporary credentials it issues this way).
func (a *Authenticator) AddSource(s Source) {
	if a.Source == nil {
		a.Source = s
		return
	}
	a.Source = ChainSource{s, a.Source}
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range a.ExemptPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		id, err := a.Resolve(r)
		if err != nil {
			zap.L().Info("auth rejected",
				zap.String("host", r.Host),
				zap.String("path", r.URL.Path),
				zap.Error(err))
			writeAuthError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(util.WithIdentity(r.Context(), id)))
	})
}

// Resolve works out who the request is from, verifying the signature in
// strict mode.
func (a *Authenticator) Resolve(r *http.Request) (*Identity, error) {
	host, _ := util.ParseAWSHost(r.Host)
	id := &Identity{
		AccountID: util.DefaultAccountID(),
		Region:    host.Region,
	}

	signed, err := Parse(r)
	switch {
	case err == nil:
		id.AccessKeyID = signed.AccessKeyID
		id.SigningService = signed.Service
		if signed.Region != "" && signed.Region != "aws-global" {
			id.Region = signed.Region
		}
	case a.Mode == ModeStrict:
		return nil, err
	}
	if id.Region == "" {
		id.Region = util.DefaultRegion()
	}

	if a.Mode == ModeDev {
		id.Namespace = util.NamespaceFromUserAgent(r)
		if a.Source != nil && id.AccessKeyID != "" {
			if c, err := a.Source.Lookup(r.Context(), id.AccessKeyID); err == nil {
				applyCredential(id, c)
			}
		}
		if id.Namespace == "" {
			id.Namespace = firstNonEmpty(id.AccessKeyID, "default")
		}
		return id, nil
	}

	cred, err := a.Source.Lookup(r.Context(), signed.AccessKeyID)
	if err != nil {
		return nil, err
	}
	if err := Verify(r, signed, cred.SecretAccessKey, a.Now()); err != nil {
		return nil, err
	}
	applyCredential(id, cred)
	id.Authenticated = true
	return id, nil
}

// Identity is re-exported so callers needn't import util for it.
type Identity = util.Identity

func applyCredential(id *Identity, c *Credential) {
	if c.AccountID != "" {
		id.AccountID = c.AccountID
	}
	if id.Namespace == "" {
		id.Namespace = firstNonEmpty(c.Namespace, c.AccessKeyID)
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// writeAuthError answers in the error format of the protocol the client is
// speaking, with the codes AWS uses, so SDKs surface the real message.
func writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	proto := protocolOf(r)

	var code, msg string
	switch {
	case errors.Is(err, ErrUnknownKey):
		switch proto {
		case "s3":
			code, msg = "InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records."
		case "json", "rest-json":
			code, msg = "UnrecognizedClientException", "The security token included in the request is invalid."
		default:
			code, msg = "InvalidClientTokenId", "The security token included in the request is invalid."
		}
	case errors.Is(err, ErrBadSignature):
		msg = "The request signature we calculated does not match the signature you provided. Check your AWS Secret Access Key and signing method. Consult the service documentation for details."
		code = "SignatureDoesNotMatch"
		if proto == "json" || proto == "rest-json" {
			code = "InvalidSignatureException"
		}
	case errors.Is(err, ErrExpired):
		msg = "Signature expired: the request time is too far from the server time, or the presigned URL has expired."
		switch proto {
		case "s3":
			code = "RequestTimeTooSkewed"
			if r.URL.Query().Get("X-Amz-Expires") != "" {
				code, msg = "AccessDenied", "Request has expired"
			}
		case "json", "rest-json":
			code = "InvalidSignatureException"
		default:
			code = "RequestExpired"
		}
	case errors.Is(err, ErrContentSHA256Mismatch):
		writeError(w, proto, http.StatusBadRequest, "XAmzContentSHA256Mismatch",
			"The provided 'x-amz-content-sha256' header does not match what was computed.")
		return
	case errors.Is(err, ErrBadContentSHA256):
		writeError(w, proto, http.StatusBadRequest, "InvalidArgument",
			"x-amz-content-sha256 must be UNSIGNED-PAYLOAD, STREAMING-UNSIGNED-PAYLOAD-TRAILER, STREAMING-AWS4-HMAC-SHA256-PAYLOAD, STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER, STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD, STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD-TRAILER or a valid sha256 value.")
		return
	case errors.Is(err, ErrMissingAuth):
		switch proto {
		case "s3":
			code, msg = "AccessDenied", "Access Denied"
		case "json", "rest-json":
			code, msg = "MissingAuthenticationTokenException", "Missing Authentication Token"
		default:
			code, msg = "MissingAuthenticationToken", "Request is missing Authentication Token"
		}
	case errors.Is(err, ErrMalformedAuth):
		code, msg = "IncompleteSignature", "The request signature does not conform to AWS standards."
		if proto == "s3" {
			code = "AuthorizationHeaderMalformed"
		} else if proto == "json" || proto == "rest-json" {
			code = "IncompleteSignatureException"
		}
	default:
		// The credential source itself failed (the callback is down, say).
		zap.L().Error("credential lookup failed", zap.Error(err))
		code, msg = "InternalFailure", "The request processing has failed because of an unknown error."
		if proto == "s3" {
			code = "InternalError"
		}
		writeError(w, proto, http.StatusInternalServerError, code, msg)
		return
	}

	status := http.StatusForbidden
	if proto == "json" {
		status = http.StatusBadRequest
	}
	writeError(w, proto, status, code, msg)
}

// Protocol reports the wire protocol of a request: "s3", "json",
// "rest-json" or "query" (which covers the XML REST services too).
func Protocol(r *http.Request) string { return protocolOf(r) }

// WriteError writes an AWS error in the shape the request's protocol uses.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeError(w, protocolOf(r), status, code, msg)
}

func writeError(w http.ResponseWriter, proto string, status int, code, msg string) {
	switch proto {
	case "s3":
		awsresponses.WriteS3ErrorXML(w, status, code, msg, "")
	case "json":
		awsresponses.WriteJSON(w, status, map[string]string{"__type": code, "message": msg})
	case "rest-json":
		w.Header().Set("X-Amzn-ErrorType", code)
		awsresponses.WriteJSON(w, status, map[string]string{"Type": "User", "message": msg})
	default:
		awsresponses.WriteErrorXML(w, status, code, msg, "")
	}
}

// protocolOf guesses the wire protocol so errors come back in a shape the
// client can parse.
func protocolOf(r *http.Request) string {
	if r.Header.Get("X-Amz-Target") != "" {
		return "json"
	}
	service := ""
	if h, ok := util.ParseAWSHost(r.Host); ok {
		service = h.Service
	} else if s, err := Parse(r); err == nil {
		service = s.Service
	}
	switch service {
	case "s3", "s3-control":
		return "s3"
	case "lambda", "apigateway":
		return "rest-json"
	case "":
		// Unsigned request to a bare host: path-style S3 is the only REST
		// protocol served at arbitrary paths, so assume it unless this looks
		// like a Query call.
		if r.URL.Path != "/" || r.Method == http.MethodGet && r.URL.Query().Get("Action") == "" {
			return "s3"
		}
	}
	return "query"
}
