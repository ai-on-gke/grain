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
	"crypto/sha256"
	"encoding/hex"
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

// LiteralMove is one typed-in credential MoveLiteralCredentials took out
// of a server entry: where it was, the secrets store name
// (<secret>/<key>) it is now referenced by, and the text to store there.
// Value is the credential itself: store it, never log it.
type LiteralMove struct {
	Where  string
	Secret string
	Value  string
}

// LiteralSecretPrefix starts the secret every typed-in credential of a
// capability server is moved into: capability-server-<id>/<field>.
const LiteralSecretPrefix = "capability-server-"

// LiteralSecretName is the secrets store name (<secret>/<key>) the
// typed-in credential at where ("env API_TOKEN", "header X-Api-Key") of
// server id is moved into: capability-server-<id>/<field>, with id and
// field cut down to letters, digits, '-' and '_' -- a capability id is
// usually a URL, and a secret's name holds no '/' -- and, when that
// changed either, a short hash of the original appended, so that two ids
// that only differ in punctuation never share a secret.
func LiteralSecretName(id, where string) string {
	kind, name, _ := strings.Cut(where, " ")
	return LiteralSecretPrefix + secretComponent(id) + "/" + secretComponent(kind+"-"+name)
}

func secretComponent(s string) string {
	trimmed := s
	for _, scheme := range []string{"https://", "http://"} {
		trimmed = strings.TrimPrefix(trimmed, scheme)
	}
	var b strings.Builder
	dash := false
	for _, r := range trimmed {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == s {
		return out
	}
	sum := sha256.Sum256([]byte(s))
	if out == "" {
		return hex.EncodeToString(sum[:4])
	}
	return out + "-" + hex.EncodeToString(sum[:4])
}

// MoveLiteralCredentials is c with every typed-in credential in its env
// and headers -- each LiteralCredentials names there -- replaced by a
// $secret: reference to LiteralSecretName(id, where), and the moves the
// caller has to make in the secrets store for the result to resolve to
// exactly what c did (Resolve). A value with no reference in it moves
// whole ("Bearer abc123" becomes "$secret:<name>"); in one that already
// mixes references with typed-in text, each piece of that text moves on
// its own, to <name>, <name>-2 and so on, and an auth scheme word is
// kept.
//
// stuck names the typed-in credentials it cannot move: those in args
// and serverUrl, where grain resolves no reference, and a value whose
// typed-in text runs straight into a reference ("abc$secret:x"), which
// no rewrite can separate. The returned copy shares no map with c.
func (c ServerConfig) MoveLiteralCredentials(id string) (out ServerConfig, moves []LiteralMove, stuck []string) {
	out = c
	out.Env = maps.Clone(c.Env)
	out.Headers = maps.Clone(c.Headers)
	for _, where := range c.LiteralCredentials() {
		kind, name, _ := strings.Cut(where, " ")
		var m map[string]string
		switch kind {
		case "env":
			m = out.Env
		case "header":
			m = out.Headers
		default:
			stuck = append(stuck, where)
			continue
		}
		rewritten, moved, ok := moveLiteralValue(m[name], LiteralSecretName(id, where))
		if !ok {
			stuck = append(stuck, where)
			continue
		}
		m[name] = rewritten
		for _, mv := range moved {
			moves = append(moves, LiteralMove{Where: where, Secret: mv[0], Value: mv[1]})
		}
	}
	return out, moves, stuck
}

// moveLiteralValue rewrites v with its typed-in text referenced from
// secret (and secret-2, ...), returning each [name, value] to store.
func moveLiteralValue(v, secret string) (string, [][2]string, bool) {
	lits, refs := splitSecretRefs(v)
	if len(refs) == 0 {
		return SecretRef(secret), [][2]string{{secret, v}}, true
	}
	var b strings.Builder
	var moved [][2]string
	for i, lit := range lits {
		t := strings.TrimSpace(lit)
		if t != "" && !authSchemes[strings.ToLower(t)] {
			if i < len(refs) && strings.HasSuffix(lit, t) {
				return "", nil, false
			}
			name := secret
			if len(moved) > 0 {
				name += "-" + strconv.Itoa(len(moved)+1)
			}
			moved = append(moved, [2]string{name, t})
			lit = strings.Replace(lit, t, SecretRef(name), 1)
		}
		b.WriteString(lit)
		if i < len(refs) {
			b.WriteString(secretPrefix + refs[i])
		}
	}
	return b.String(), moved, true
}
