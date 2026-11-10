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

package protocol

import (
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/redact"
)

// LiteralWithheld is what WithheldLiterals puts in place of a value's
// literal text.
const LiteralWithheld = "[withheld]"

// authSchemes are the words a header may carry literally ahead of its
// $secret:<name> reference ("Bearer $secret:cap-token"): they say how the
// credential is presented and are not themselves secret.
var authSchemes = map[string]bool{"bearer": true, "basic": true, "token": true, "apikey": true, "digest": true}

// literalText is v with its $secret:<name> references and any auth scheme
// word ahead of them taken out: what of v is typed in rather than stored.
func literalText(v string) string {
	lits, _ := splitSecretRefs(v)
	var out []string
	for _, lit := range lits {
		lit = strings.TrimSpace(lit)
		if lit == "" || authSchemes[strings.ToLower(lit)] {
			continue
		}
		out = append(out, lit)
	}
	return strings.Join(out, " ")
}

// credentialName says whether an env var or header name is one a
// credential is passed in: Authorization, X-Api-Key, GITHUB_TOKEN,
// DB_PASSWORD and the like.
func credentialName(name string) bool {
	n := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	switch n {
	case "AUTHORIZATION", "PROXY_AUTHORIZATION", "COOKIE", "KEY":
		return true
	}
	for _, word := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY", "AUTH"} {
		if strings.Contains(n, word) {
			return true
		}
	}
	return strings.HasSuffix(n, "_KEY")
}

// filePath says whether lit is a path to a file rather than a value: a
// credential's name holding one -- GOOGLE_APPLICATION_CREDENTIALS, as
// docs/gchat.md has a server entry set it, or a *_TOKEN_FILE -- names
// where the credential is, not the credential itself. A path that has a
// credential's shape in it is still caught by that shape.
func filePath(lit string) bool {
	if strings.ContainsAny(lit, " \t\r\n") {
		return false
	}
	return strings.HasPrefix(lit, "/") || strings.HasPrefix(lit, "~/") || strings.HasPrefix(lit, "./")
}

// LiteralCredentials names, sorted, every env var and header of c whose
// value holds a credential typed in rather than a $secret:<name>
// reference: one under a credential's name (credentialName) with any
// literal text at all, or one whose literal text has a credential's
// shape (pkg/redact) whatever it is called; and every arg, and the URL,
// with a credential's shape. Such a value is stored in
// grain_config.mcp_servers, exported to the state repository, and shown
// to anyone who may read settings.
func (c ServerConfig) LiteralCredentials() []string {
	return sortedKeys(c.literalCredentials())
}

// NewLiteralCredentials is LiteralCredentials less those prev already
// held with the same value: what a save of c over prev would add. A save
// that adds one is refused (Client.SetCapabilityServer); one that only
// carries forward what the stored entry already held -- an entry saved
// before saves refused it, edited for some other field -- is not, so
// that the entry stays editable until it is migrated.
func (c ServerConfig) NewLiteralCredentials(prev ServerConfig) []string {
	held := prev.literalCredentials()
	var out []string
	for where, v := range c.literalCredentials() {
		if old, ok := held[where]; !ok || old != v {
			out = append(out, where)
		}
	}
	sort.Strings(out)
	return out
}

// literalCredentials is LiteralCredentials keyed by where each is, with
// the value it holds there.
func (c ServerConfig) literalCredentials() map[string]string {
	out := map[string]string{}
	check := func(kind, name, v string) {
		lit := literalText(v)
		if lit == "" {
			return
		}
		if _, n := (*redact.Redactor)(nil).Line(lit); n > 0 || (credentialName(name) && !filePath(lit)) {
			out[kind+" "+name] = v
		}
	}
	for name, v := range c.Env {
		check("env", name, v)
	}
	for name, v := range c.Headers {
		check("header", name, v)
	}
	for i, a := range c.Args {
		if _, n := (*redact.Redactor)(nil).Line(a); n > 0 {
			out["args["+strconv.Itoa(i)+"]"] = a
		}
	}
	if _, n := (*redact.Redactor)(nil).Line(c.ServerURL); n > 0 {
		out["serverUrl"] = c.ServerURL
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WithheldLiterals is c as a caller who may read capability servers but
// not change them is shown it: every env var and header value's literal
// text replaced by LiteralWithheld, its $secret:<name> references and
// auth scheme kept, and credential shapes cut out of args, serverUrl and
// config strings. A literal value may be a credential an operator typed in
// before saves refused one (LiteralCredentials), and the names of what is
// referenced are all such a caller needs to read the entry. The returned
// copy shares nothing with c.
func (c ServerConfig) WithheldLiterals() ServerConfig {
	out := c
	out.Env = withheldMap(c.Env)
	out.Headers = withheldMap(c.Headers)
	if c.Args != nil {
		out.Args = make([]string, len(c.Args))
		for i, a := range c.Args {
			out.Args[i], _ = (*redact.Redactor)(nil).Line(a)
		}
	}
	out.ServerURL, _ = (*redact.Redactor)(nil).Line(c.ServerURL)
	if c.Config != nil {
		out.Config = make(map[string]any, len(c.Config))
		for k, v := range c.Config {
			out.Config[k] = withheldConfigValue(v)
		}
	}
	out.Aliases = slices.Clone(c.Aliases)
	return out
}

func withheldMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := maps.Clone(m)
	for name, v := range out {
		if literalText(v) == "" {
			continue
		}
		lits, refs := splitSecretRefs(v)
		var b strings.Builder
		for i, lit := range lits {
			if t := strings.TrimSpace(lit); t != "" && !authSchemes[strings.ToLower(t)] {
				lit = strings.Replace(lit, t, LiteralWithheld, 1)
			}
			b.WriteString(lit)
			if i < len(refs) {
				b.WriteString(secretPrefix + refs[i])
			}
		}
		out[name] = b.String()
	}
	return out
}

func withheldConfigValue(v any) any {
	switch t := v.(type) {
	case string:
		s, _ := (*redact.Redactor)(nil).Line(t)
		return s
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = withheldConfigValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = withheldConfigValue(e)
		}
		return out
	}
	return v
}
