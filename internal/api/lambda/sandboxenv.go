// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// RoleCredentials are temporary credentials for a function's execution
// role.
type RoleCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expires         time.Time
}

// CredentialIssuer mints credentials for a function's execution role, in
// the function's own namespace and account, the way STS AssumeRole does.
// The router wires in opensnack's STS.
type CredentialIssuer interface {
	RoleCredentials(namespace, accountID, roleARN, sessionName string, ttl time.Duration) (*RoleCredentials, error)
	// Revoke invalidates credentials early, when their sandbox goes away.
	Revoke(accessKeyID string)
}

// sandboxPaths are where a sandbox finds its code, its runtime and its
// scratch space.
type sandboxPaths struct {
	TaskRoot, RuntimeDir, Tmp string
}

// sandboxOptions is what an executor adds to the function's own settings.
type sandboxOptions struct {
	Paths     sandboxPaths
	LogStream string
	Creds     *RoleCredentials
	// EndpointURL, when set, points the AWS SDKs at opensnack
	// (AWS_ENDPOINT_URL) instead of the real AWS host names.
	EndpointURL string
	// Extra variables the executor sets (CA bundles, PATH...).
	Extra map[string]string
}

var envKeyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// reservedEnv reports whether a function may not set key itself. Lambda
// refuses the keys in reservedEnvKeys when the function is configured; at
// run time opensnack also keeps back anything in the runtime's own
// namespaces, so a variable stored before the check existed, or one the
// API doesn't refuse, can't steer the sandbox.
func reservedEnv(key string) bool {
	for _, k := range reservedEnvKeys {
		if key == k {
			return true
		}
	}
	for _, p := range []string{"AWS_LAMBDA_", "LAMBDA_", "_OPENSNACK", "_AWS_XRAY", "AWS_ENDPOINT_URL"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return key == "_HANDLER" || key == "AWS_CA_BUNDLE" || !envKeyPattern.MatchString(key)
}

// sandboxEnv is the environment a function runs with: its own variables,
// then the ones Lambda defines, which always win.
func sandboxEnv(inv *Invocation, o sandboxOptions) map[string]string {
	env := map[string]string{}
	for k, v := range inv.Environment {
		if !reservedEnv(k) {
			env[k] = v
		}
	}
	set := map[string]string{
		"HOME":                            o.Paths.Tmp,
		"TMPDIR":                          o.Paths.Tmp,
		"LANG":                            "en_US.UTF-8",
		"TZ":                              ":UTC",
		"LAMBDA_TASK_ROOT":                o.Paths.TaskRoot,
		"LAMBDA_RUNTIME_DIR":              o.Paths.RuntimeDir,
		"_HANDLER":                        inv.Handler,
		"AWS_REGION":                      inv.Region,
		"AWS_DEFAULT_REGION":              inv.Region,
		"AWS_EXECUTION_ENV":               "AWS_Lambda_" + inv.Runtime,
		"AWS_LAMBDA_FUNCTION_NAME":        inv.FunctionName,
		"AWS_LAMBDA_FUNCTION_VERSION":     inv.Version,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprint(inv.MemoryMB),
		"AWS_LAMBDA_LOG_GROUP_NAME":       inv.LogGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      o.LogStream,
		"AWS_LAMBDA_INITIALIZATION_TYPE":  "on-demand",
	}
	if o.Creds != nil {
		set["AWS_ACCESS_KEY_ID"] = o.Creds.AccessKeyID
		set["AWS_SECRET_ACCESS_KEY"] = o.Creds.SecretAccessKey
		set["AWS_SESSION_TOKEN"] = o.Creds.SessionToken
	}
	if o.EndpointURL != "" {
		set["AWS_ENDPOINT_URL"] = o.EndpointURL
	}
	for k, v := range o.Extra {
		set[k] = v
	}
	for k, v := range set {
		env[k] = v
	}
	return env
}

// endpointFromEnv is OPENSNACK_LAMBDA_AWS_ENDPOINT_URL: where functions'
// SDK calls go when they can't use the real AWS host names (development,
// or a deployment without DNS for them).
func endpointFromEnv() string { return os.Getenv("OPENSNACK_LAMBDA_AWS_ENDPOINT_URL") }

// credentialTTL is how long a sandbox's credentials last: its whole life,
// plus the longest invocation it could start just before retiring.
func credentialTTL(maxLifetime time.Duration) time.Duration {
	return maxLifetime + 15*time.Minute + time.Minute
}
