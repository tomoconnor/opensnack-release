// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// tfcycle runs the "is this resource done?" cycle against a running
// opensnack, for each case directory given:
//
//  1. apply (variant "a")                  must succeed
//  2. plan                                  must show no changes
//  3. apply variant "b", if the case has one: the plan must update in place
//     (no replacements), and a plan afterwards must be clean
//  4. state rm everything, import it back:  the import plan must change nothing
//  5. destroy:                              must succeed and leave the
//     namespace empty (checked through the inspection API)
//
// A case is a directory of .tf files. tfcycle supplies the provider
// configuration (aws, plus aws.west for eu-west-1) pointed at opensnack's
// real-style host names, and a variable "variant". Optional files in the case:
//
//	imports.txt     "<address> <import id template>" lines, for resources
//	                that don't import by their id attribute. Templates use
//	                {attr} for top-level attributes, e.g. "{role}/{policy_arn}".
//	no_import.txt   addresses (one per line) to leave out of step 4
//
// Usage:
//
//	go run ./cmd/tfcycle [flags] opentofu/cycle/cases/sqs_queue ...
//
// The server must accept the harness's random access key (dev mode, the
// default), or pass -access-key/-secret-key for a strict-mode key. It must
// treat -host-suffix as an AWS domain (OPENSNACK_HOST_SUFFIXES).
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	endpoint      = flag.String("endpoint", "http://localhost:4566", "opensnack base URL (for the inspection API)")
	hostSuffix    = flag.String("host-suffix", "localtest.me", "wildcard domain resolving to opensnack, configured in OPENSNACK_HOST_SUFFIXES")
	port          = flag.String("port", "", "port to use in provider endpoints (default: the -endpoint port)")
	tofuBin       = flag.String("tofu", "tofu", "OpenTofu (>= 1.7) or Terraform binary")
	providerVer   = flag.String("provider", "~> 6.0", "hashicorp/aws provider version constraint")
	accessKey     = flag.String("access-key", "", "access key (default: a random one, i.e. a fresh namespace in dev mode)")
	secretKey     = flag.String("secret-key", "cycle-secret", "secret key")
	internalToken = flag.String("token", os.Getenv("OPENSNACK_INTERNAL_TOKEN"), "inspection API token")
	keep          = flag.Bool("keep", false, "keep the work directories")
	verbose       = flag.Bool("v", false, "print tofu output for every step")
)

// services maps provider endpoint keys to the host-name prefixes AWS uses.
// Global services have no region in the host.
var services = []struct {
	key, host string
	global    bool
}{
	{"s3", "s3", false},
	{"s3control", "s3-control", false},
	{"sqs", "sqs", false},
	{"sns", "sns", false},
	{"sts", "sts", false},
	{"iam", "iam", true},
	{"kms", "kms", false},
	{"dynamodb", "dynamodb", false},
	{"logs", "logs", false},
	{"cloudwatch", "monitoring", false},
	{"lambda", "lambda", false},
	{"route53", "route53", true},
	{"ec2", "ec2", false},
	{"elasticache", "elasticache", false},
	{"secretsmanager", "secretsmanager", false},
	{"ssm", "ssm", false},
	{"events", "events", false},
	{"scheduler", "scheduler", false},
	{"apigatewayv2", "apigateway", false},
	{"cloudfront", "cloudfront", true},
}

func main() {
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tfcycle [flags] <case dir>...")
		os.Exit(2)
	}
	if *port == "" {
		if i := strings.LastIndex(*endpoint, ":"); i > len("http://") {
			*port = (*endpoint)[i+1:]
		}
	}

	failed := 0
	for _, dir := range flag.Args() {
		if !runCase(dir) {
			failed++
		}
	}
	fmt.Printf("\n%d/%d cases passed\n", flag.NArg()-failed, flag.NArg())
	if failed > 0 {
		os.Exit(1)
	}
}

type caseRun struct {
	name, work, key string
	env             []string
	ok              bool
}

func runCase(dir string) bool {
	c := &caseRun{name: filepath.Base(dir), ok: true}
	c.key = *accessKey
	if c.key == "" {
		c.key = "AKIACYCLE" + strings.ToUpper(randHex(6))
	}
	fmt.Printf("\n=== %s (namespace %s)\n", c.name, c.key)

	work, err := os.MkdirTemp("", "tfcycle-"+c.name+"-")
	if err != nil {
		c.fail("setup", err.Error())
		return false
	}
	c.work = work
	if !*keep {
		defer os.RemoveAll(work)
	} else {
		fmt.Println("    work dir:", work)
	}
	if err := copyTF(dir, work); err != nil {
		c.fail("setup", err.Error())
		return false
	}
	if err := os.WriteFile(filepath.Join(work, "zz_tfcycle_provider.tf"), []byte(providerConfig(c.key)), 0o644); err != nil {
		c.fail("setup", err.Error())
		return false
	}
	c.env = append(os.Environ(),
		"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1",
		"AWS_EC2_METADATA_DISABLED=true")
	if os.Getenv("TF_PLUGIN_CACHE_DIR") == "" {
		cache := filepath.Join(os.TempDir(), "tfcycle-plugin-cache")
		os.MkdirAll(cache, 0o755)
		c.env = append(c.env, "TF_PLUGIN_CACHE_DIR="+cache)
	}

	if *accessKey != "" {
		c.deleteNamespace()
	}

	if _, ok := c.tofu("init", "init", "-no-color"); !ok {
		return false
	}

	// 1. apply
	if _, ok := c.tofu("apply", "apply", "-auto-approve", "-no-color", "-var", "variant=a"); !ok {
		c.cleanup()
		return false
	}
	c.pass("apply")

	// 2. clean plan
	c.cleanPlan("plan clean", "a")

	// 3. in-place update
	variant := "a"
	if hasVariantB(dir) {
		variant = "b"
		if c.planNoReplace("update in place", "b") {
			if _, ok := c.tofu("update in place", "apply", "-auto-approve", "-no-color", "-var", "variant=b"); ok {
				c.cleanPlan("plan clean after update", "b")
			}
		}
	} else {
		fmt.Println("    -    update in place (no variant b in this case)")
	}

	// 4. import
	c.importAll(dir, variant)

	// 5. destroy
	c.cleanup()
	return c.ok
}

func (c *caseRun) cleanup() {
	if _, ok := c.tofu("destroy", "destroy", "-auto-approve", "-no-color", "-var", "variant=b"); !ok {
		return
	}
	c.pass("destroy")
	if left, err := c.leftovers(); err != nil {
		fmt.Printf("    ?    nothing left behind (inspection API: %v)\n", err)
	} else if len(left) != 0 {
		c.fail("nothing left behind", fmt.Sprintf("%d resources remain: %s", len(left), strings.Join(left, ", ")))
	} else {
		c.pass("nothing left behind")
	}
}

func (c *caseRun) cleanPlan(step, variant string) bool {
	out, code := c.tofuCode("plan", "-detailed-exitcode", "-no-color", "-var", "variant="+variant)
	switch code {
	case 0:
		c.pass(step)
		return true
	case 2:
		c.fail(step, "perpetual diff:\n"+indent(planBody(out)))
	default:
		c.fail(step, indent(tail(out, 30)))
	}
	return false
}

// planNoReplace checks a plan updates in place: no resource is destroyed.
func (c *caseRun) planNoReplace(step, variant string) bool {
	out, code := c.tofuCode("plan", "-no-color", "-out=tfcycle.plan", "-var", "variant="+variant)
	if code != 0 {
		c.fail(step, indent(tail(out, 30)))
		return false
	}
	changes, err := c.planChanges()
	if err != nil {
		c.fail(step, err.Error())
		return false
	}
	var replaced []string
	updated := 0
	for _, rc := range changes {
		if slices.Contains(rc.Change.Actions, "delete") {
			replaced = append(replaced, rc.Address)
		}
		if slices.Contains(rc.Change.Actions, "update") {
			updated++
		}
	}
	if len(replaced) > 0 {
		c.fail(step, "replaces "+strings.Join(replaced, ", ")+":\n"+indent(planBody(out)))
		return false
	}
	if updated == 0 {
		fmt.Println("    !    variant b changes nothing in place; is the case using var.variant?")
	}
	c.pass(step)
	return true
}

type resourceChange struct {
	Address string `json:"address"`
	Mode    string `json:"mode"`
	Change  struct {
		Actions   []string        `json:"actions"`
		Importing json.RawMessage `json:"importing"`
	} `json:"change"`
}

func (c *caseRun) planChanges() ([]resourceChange, error) {
	out, code := c.tofuCode("show", "-json", "tfcycle.plan")
	if code != 0 {
		return nil, fmt.Errorf("show plan: %s", tail(out, 10))
	}
	var plan struct {
		ResourceChanges []resourceChange `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	return plan.ResourceChanges, nil
}

type stateResource struct {
	Address string         `json:"address"`
	Mode    string         `json:"mode"`
	Values  map[string]any `json:"values"`
}

func (c *caseRun) stateResources() ([]stateResource, error) {
	out, code := c.tofuCode("show", "-json")
	if code != 0 {
		return nil, fmt.Errorf("show: %s", tail(out, 10))
	}
	var st struct {
		Values struct {
			RootModule struct {
				Resources []stateResource `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, err
	}
	var managed []stateResource
	for _, r := range st.Values.RootModule.Resources {
		if r.Mode == "managed" {
			managed = append(managed, r)
		}
	}
	return managed, nil
}

var templateVar = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// importAll removes every managed resource from state and imports it back
// with import blocks; the resulting plan must change nothing.
func (c *caseRun) importAll(dir, variant string) {
	const step = "import"
	resources, err := c.stateResources()
	if err != nil {
		c.fail(step, err.Error())
		return
	}
	templates := readPairs(filepath.Join(dir, "imports.txt"))
	skip := map[string]bool{}
	for _, a := range readLines(filepath.Join(dir, "no_import.txt")) {
		skip[a] = true
	}

	var blocks strings.Builder
	var addrs []string
	for _, r := range resources {
		if skip[r.Address] {
			continue
		}
		tmpl, ok := templates[r.Address]
		if !ok {
			tmpl = "{id}"
		}
		var missing []string
		id := templateVar.ReplaceAllStringFunc(tmpl, func(m string) string {
			v, ok := r.Values[m[1:len(m)-1]]
			if !ok || v == nil {
				missing = append(missing, m)
				return ""
			}
			return fmt.Sprint(v)
		})
		if len(missing) > 0 {
			c.fail(step, fmt.Sprintf("%s: no attribute %v for its import id", r.Address, missing))
			return
		}
		fmt.Fprintf(&blocks, "import {\n  to = %s\n  id = %q\n}\n\n", r.Address, id)
		addrs = append(addrs, r.Address)
	}
	if len(addrs) == 0 {
		fmt.Println("    -    import (nothing to import)")
		return
	}

	if _, ok := c.tofu(step, append([]string{"state", "rm", "-no-color"}, addrs...)...); !ok {
		return
	}
	importFile := filepath.Join(c.work, "zz_tfcycle_imports.tf")
	os.WriteFile(importFile, []byte(blocks.String()), 0o644)
	defer os.Remove(importFile)

	out, code := c.tofuCode("plan", "-no-color", "-out=tfcycle.plan", "-var", "variant="+variant)
	if code != 0 {
		c.fail(step, indent(tail(out, 30)))
		c.reimport()
		return
	}
	changes, err := c.planChanges()
	if err != nil {
		c.fail(step, err.Error())
		c.reimport()
		return
	}
	var dirty []string
	for _, rc := range changes {
		if rc.Mode == "managed" && !(len(rc.Change.Actions) == 1 && rc.Change.Actions[0] == "no-op") {
			dirty = append(dirty, rc.Address+" "+strings.Join(rc.Change.Actions, "+"))
		}
	}
	if len(dirty) > 0 {
		c.fail(step, "import plan is not clean: "+strings.Join(dirty, ", ")+"\n"+indent(planBody(out)))
		c.reimport()
		return
	}
	if _, ok := c.tofu(step, "apply", "-auto-approve", "-no-color", "tfcycle.plan"); !ok {
		return
	}
	c.pass(fmt.Sprintf("import (%d resources)", len(addrs)))
}

// reimport applies the import blocks regardless, so destroy can still find
// what the failed import step took out of state.
func (c *caseRun) reimport() {
	c.tofuCode("apply", "-auto-approve", "-no-color", "-var", "variant=b")
}

func (c *caseRun) tofu(step string, args ...string) (string, bool) {
	out, code := c.tofuCode(args...)
	if code != 0 {
		c.fail(step, indent(tail(out, 40)))
		return out, false
	}
	return out, true
}

func (c *caseRun) tofuCode(args ...string) (string, int) {
	cmd := exec.Command(*tofuBin, args...)
	cmd.Dir = c.work
	cmd.Env = c.env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if *verbose && args[0] != "show" {
		fmt.Printf("    $ %s %s\n%s\n", *tofuBin, strings.Join(args, " "), indent(out))
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, ee.ExitCode()
		}
		return out + err.Error(), -1
	}
	return out, 0
}

func (c *caseRun) pass(step string) { fmt.Printf("    ok   %s\n", step) }

func (c *caseRun) fail(step, detail string) {
	c.ok = false
	fmt.Printf("    FAIL %s\n%s\n", step, detail)
}

// inspection API

func (c *caseRun) api(method, path string) (*http.Response, error) {
	req, _ := http.NewRequest(method, strings.TrimRight(*endpoint, "/")+"/_opensnack/v1/"+path, nil)
	if *internalToken != "" {
		req.Header.Set("Authorization", "Bearer "+*internalToken)
	}
	return (&http.Client{Timeout: 10 * time.Second}).Do(req)
}

// leftovers lists what destroy left in the namespace, apart from what real
// AWS keeps too: KMS keys pending deletion (a 7-30 day window), and the
// AWS-managed keys and alias/aws/* aliases services create on first use.
func (c *caseRun) leftovers() ([]string, error) {
	resp, err := c.api("GET", "namespaces/"+c.key+"/resources")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d %s", resp.StatusCode, b)
	}
	var out struct {
		Resources []struct {
			Service string          `json:"service"`
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Attrs   json.RawMessage `json:"attributes"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var left []string
	for _, r := range out.Resources {
		if r.Service == "kms" {
			var key struct {
				Metadata struct {
					KeyState   string `json:"KeyState"`
					KeyManager string `json:"KeyManager"`
				} `json:"key_metadata"`
			}
			json.Unmarshal(r.Attrs, &key)
			if (r.Type == "key" && (key.Metadata.KeyState == "PendingDeletion" || key.Metadata.KeyManager == "AWS")) ||
				(r.Type == "alias" && strings.HasPrefix(r.ID, "alias/aws/")) {
				continue
			}
		}
		left = append(left, r.Service+"/"+r.Type+"/"+r.ID)
	}
	return left, nil
}

func (c *caseRun) deleteNamespace() {
	if resp, err := c.api("DELETE", "namespaces/"+c.key); err == nil {
		resp.Body.Close()
	}
}

// configuration

func providerConfig(key string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = %q
    }
  }
}

variable "variant" {
  type    = string
  default = "a"
}
`, *providerVer)
	for _, p := range []struct{ alias, region string }{{"", "us-east-1"}, {"west", "eu-west-1"}} {
		fmt.Fprintf(&b, "\nprovider \"aws\" {\n")
		if p.alias != "" {
			fmt.Fprintf(&b, "  alias = %q\n", p.alias)
		}
		fmt.Fprintf(&b, `  region                  = %q
  access_key              = %q
  secret_key              = %q
  skip_metadata_api_check = true
  s3_use_path_style       = false

  endpoints {
`, p.region, key, *secretKey)
		for _, s := range services {
			host := s.host + "." + p.region + "." + *hostSuffix
			if s.global {
				host = s.host + "." + *hostSuffix
			}
			if *port != "" {
				host += ":" + *port
			}
			fmt.Fprintf(&b, "    %-15s = %q\n", s.key, "http://"+host)
		}
		b.WriteString("  }\n}\n")
	}
	return b.String()
}

func hasVariantB(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.tf"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && bytes.Contains(b, []byte("var.variant")) {
			return true
		}
	}
	return false
}

func copyTF(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tfstate") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func readPairs(path string) map[string]string {
	out := map[string]string{}
	for _, l := range readLines(path) {
		if k, v, ok := strings.Cut(l, " "); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// planBody keeps the human-readable resource diff from plan output.
func planBody(out string) string {
	if i := strings.Index(out, "OpenTofu will perform"); i >= 0 {
		out = out[i:]
	} else if i := strings.Index(out, "Terraform will perform"); i >= 0 {
		out = out[i:]
	}
	return tail(out, 60)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{"..."}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

func indent(s string) string {
	return "        " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n        ")
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
