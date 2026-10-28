// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package security

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staticRule is one construct the non-test source may not contain.
type staticRule struct {
	name    string
	pattern *regexp.Regexp
	// why is printed with every violation: it is what the author of the
	// offending line reads, so it says what to do instead.
	why string
	// allow names files (relative to the repository root, slash
	// separated) exempt from this rule, each with its reason. Empty on
	// every rule today, and meant to stay short.
	allow map[string]string
}

var staticRules = []staticRule{
	{
		name:    "tls-skip-verify",
		pattern: regexp.MustCompile(`InsecureSkipVerify:\s*true\b`),
		why: "a TLS client that never verifies the server accepts any certificate on the path, " +
			"credentials included; pin a CA with RootCAs instead. (A verify decision computed from " +
			"explicit configuration, like libpq's sslmode=require, is not this literal.)",
	},
	{
		name:    "jwt-alg-none",
		pattern: regexp.MustCompile(`jwt\.(UnsafeAllowNoneSignatureType|SigningMethodNone)\b`),
		why:     "an unsigned JWT is a claim anyone can write; every token grain accepts is verified against a key",
	},
	{
		name:    "jwt-parse-unverified",
		pattern: regexp.MustCompile(`\.ParseUnverified\(`),
		why: "a JWT read without checking its signature says whatever its author wrote; only a " +
			"token grain itself just obtained may be read that way, and only for its own bookkeeping",
		allow: map[string]string{
			"pkg/modelexec/iap_auth.go": "reads the exp of an access token grain just fetched from Google, " +
				"to schedule its refresh; nothing is authorized on it",
			"pkg/ui/assertion_diagnostics.go": "logs the issuer and audience an assertion grain refused carried, " +
				"so an operator can see what to configure; nothing is authorized on it",
		},
	},
	{
		name:    "identity-from-query-string",
		pattern: regexp.MustCompile(`URL\.Query\(\)\.Get\("(user|as_user|actor|principal)"\)`),
		why: "who a request is comes from a verified session, a verified IAP assertion or, from a trusted " +
			"source, a proxy's identity header -- never from the URL, which any link can set and every access " +
			"log records",
	},
	{
		name:    "math-rand-secret",
		pattern: regexp.MustCompile(`(?i)(token|secret|password|nonce|salt)\w*\s*(:=|=)\s*.*\bm?rand\.(Int|Intn|Int63|Uint64|Read)\(`),
		why:     "math/rand is predictable; anything an attacker must not guess comes from crypto/rand",
	},
}

// repoRoot is two directories up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected the repository root at %s: %v", root, err)
	}
	return root
}

// productionGoFiles is every non-test .go file under cmd/ and pkg/ --
// the code a deployment runs. third_party/ and ui/node_modules/ are other
// people's trees and are not walked.
func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	if len(files) == 0 {
		t.Fatal("found no Go source to check; the walk is looking in the wrong place")
	}
	return files
}

// TestStaticSecurityRules is Layer 1: the source may not contain any of
// staticRules' constructs outside that rule's allowlist.
func TestStaticSecurityRules(t *testing.T) {
	root := repoRoot(t)
	files := productionGoFiles(t, root)
	for _, rule := range staticRules {
		t.Run(rule.name, func(t *testing.T) {
			for _, path := range files {
				rel, _ := filepath.Rel(root, path)
				rel = filepath.ToSlash(rel)
				if _, ok := rule.allow[rel]; ok {
					continue
				}
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
				for n := 1; sc.Scan(); n++ {
					line := sc.Text()
					if strings.HasPrefix(strings.TrimSpace(line), "//") {
						continue
					}
					if rule.pattern.MatchString(line) {
						t.Errorf("%s:%d: %s\n\t%s", rel, n, rule.why, strings.TrimSpace(line))
					}
				}
				if err := sc.Err(); err != nil {
					t.Errorf("reading %s: %v", rel, err)
				}
				f.Close()
			}
		})
	}
}

// TestStaticRulesCatchWhatTheyClaim keeps each pattern honest: a rule
// that stopped matching its own example would pass every tree forever.
func TestStaticRulesCatchWhatTheyClaim(t *testing.T) {
	examples := map[string][]string{
		"tls-skip-verify":            {`tls.Config{InsecureSkipVerify: true}`},
		"jwt-alg-none":               {`jwt.ParseWithClaims(s, c, func(*jwt.Token) (any, error) { return jwt.UnsafeAllowNoneSignatureType, nil })`},
		"jwt-parse-unverified":       {`_, _, err := parser.ParseUnverified(raw, &claims)`},
		"identity-from-query-string": {`if u := strings.TrimSpace(r.URL.Query().Get("user")); u != "" {`},
		"math-rand-secret":           {`token := fmt.Sprint(rand.Int63())`, `sessionSecret = mrand.Read(b)`},
	}
	notViolations := map[string][]string{
		"tls-skip-verify":  {`InsecureSkipVerify: strings.ToLower(cfg.SSLMode) == "require",`},
		"math-rand-secret": {`jitter := float64(d) * (0.8 + 0.4*rand.Float64())`},
	}
	for _, rule := range staticRules {
		ex, ok := examples[rule.name]
		if !ok {
			t.Errorf("rule %s has no example here; add one", rule.name)
		}
		for _, line := range ex {
			if !rule.pattern.MatchString(line) {
				t.Errorf("rule %s does not match its own example %q", rule.name, line)
			}
		}
		for _, line := range notViolations[rule.name] {
			if rule.pattern.MatchString(line) {
				t.Errorf("rule %s matches %q, which is not a violation", rule.name, line)
			}
		}
	}
}
