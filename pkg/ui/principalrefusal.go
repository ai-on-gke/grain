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

package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PrincipalAdvice says how the owner of some environment's install lets in
// a person an IdentityVerifier vouched for but -ui-allowed-principals does
// not admit: a workstation installer's own re-run command, say. It is given
// the allowlist entry that would admit them ("user:<email>") and the
// GRAIN_ALLOWED_PRINCIPALS list that would (the entries already in force,
// plus that one -- the list replaces the default rather than adding to
// it), and returns a sentence for the refusal page. Either argument may be
// empty when the entry is not safe to quote into a command.
type PrincipalAdvice func(entry, rerunPrincipals string) string

var (
	principalAdviceMu sync.RWMutex
	principalAdvice   PrincipalAdvice
)

// RegisterPrincipalAdvice makes f the advice the refusal page gives, in
// place of the default naming -ui-allowed-principals, and returns a
// function that puts back what was there before (for tests).
func RegisterPrincipalAdvice(f PrincipalAdvice) (unregister func()) {
	principalAdviceMu.Lock()
	defer principalAdviceMu.Unlock()
	prev := principalAdvice
	principalAdvice = f
	return func() {
		principalAdviceMu.Lock()
		defer principalAdviceMu.Unlock()
		principalAdvice = prev
	}
}

func registeredPrincipalAdvice() PrincipalAdvice {
	principalAdviceMu.RLock()
	defer principalAdviceMu.RUnlock()
	return principalAdvice
}

// defaultPrincipalAdvice names the daemon's own flag and environment
// variable.
func defaultPrincipalAdvice(entry, rerunPrincipals string) string {
	if rerunPrincipals == "" {
		return "To let them in, the owner adds them to the daemon's -ui-allowed-principals " +
			"(or GRAIN_ALLOWED_PRINCIPALS in its environment) and restarts it."
	}
	return "To let them in, the owner adds " + entry + " to the daemon's -ui-allowed-principals " +
		"(or GRAIN_ALLOWED_PRINCIPALS in its environment) and restarts it. The list replaces the default, " +
		"so it keeps the entries already allowed: grain init -daemon-arg -ui-allowed-principals='" + rerunPrincipals + "'"
}

// plainPrincipalList is whether s is made of nothing but the characters an
// allowlist of emails and domains needs, and so safe to quote into a
// suggested shell command.
func plainPrincipalList(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("@.:_+,-", c):
		default:
			return false
		}
	}
	return true
}

// principalRefusalAdvice is the page's sentence on how id is let in.
func (s *Server) principalRefusalAdvice(r *http.Request, id VerifiedIdentity) string {
	prefix := "user"
	if id.Kind == model.PrincipalService {
		prefix = "serviceAccount"
	}
	entry := prefix + ":" + normalizeIdentityEmail(id.Email)
	var rerun string
	if plainPrincipalList(entry) {
		var names []string
		if entries, err := s.effectiveAllowedPrincipals(r.Context()); err == nil {
			for _, e := range entries {
				if n := e.Prefix + ":" + e.Value; n != entry {
					names = append(names, n)
				}
			}
		}
		if list := strings.Join(append(names, entry), ","); plainPrincipalList(list) {
			rerun = list
		}
	}
	if rerun == "" {
		entry = ""
	}
	if f := registeredPrincipalAdvice(); f != nil {
		return f(entry, rerun)
	}
	return defaultPrincipalAdvice(entry, rerun)
}

// browserNavigation is whether r is a browser loading a UI page, which is
// shown a page rather than an API error.
func browserNavigation(r *http.Request) bool {
	return !strings.HasPrefix(r.URL.Path, "/api/") && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

type principalRefusal struct {
	Email, Verifier, Reason, Advice string
	// AccessCodeWins is whether the verifier lets an access code
	// override it, so signing in by code here gets the operator in.
	AccessCodeWins bool
}

var principalRefusalPage = template.Must(template.New("refused").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: not allowed</title>
{{if .AccessCodeWins}}<script src="` + accessCodeScriptPath + `" defer></script>
{{end}}<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
main { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 30rem; width: 100%; box-sizing: border-box; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
h2 { font-size: 1rem; margin: 1.5rem 0 .5rem; }
input { width: 100%; box-sizing: border-box; font: 1rem monospace; padding: .5rem; margin: .5rem 0 1rem; }
button { font-size: 1rem; padding: .5rem 1rem; }
p { color: #444; font-size: .9rem; overflow-wrap: anywhere; }
.who { color: #000; font-size: 1rem; }
</style>
</head>
<body>
<main>
<h1>Not allowed on this grain</h1>
<p class="who" id="refused-principal">Signed in as <strong>{{.Email}}</strong> (verified by {{.Verifier}}), whom this daemon's allowlist does not admit.</p>
{{if .Reason}}<p id="refused-reason">{{.Reason}}</p>
{{end}}<p id="refused-advice">{{.Advice}}</p>
<h2>Owner: sign in with the access code</h2>
{{if .AccessCodeWins}}<form method="post" action="` + accessCodePath + `">
<label for="code">Access code</label>
<input id="code" name="code" type="password" autocomplete="current-password" required>
<button type="submit">Continue</button>
<p>Run <code>grain access-code</code> on the machine grain is installed on to see it.</p>
</form>
{{else}}<p id="refused-precedence">{{.Verifier}}'s identity takes precedence over the access code on requests that come through it,
so signing in by code here would still be refused. Reach the daemon without going through {{.Verifier}} -- on the machine
grain is installed on, or over an SSH tunnel to it -- and sign in there; <code>grain access-code</code> prints the code.</p>
{{end}}</main>
</body>
</html>
`))

// writePrincipalRefusedPage answers a browser navigation from a person cv's
// verifier vouched for but the allowlist refused (err says why): who they
// are, how the owner lets them in, and the access-code form for the owner
// themselves.
func (s *Server) writePrincipalRefusedPage(w http.ResponseWriter, r *http.Request, cv callerVerification, err error) {
	page := principalRefusal{
		Email:          cv.id.Email,
		Verifier:       string(cv.method),
		Advice:         s.principalRefusalAdvice(r, cv.id),
		AccessCodeWins: cv.accessCodeWins,
	}
	// A refusal other than plain absence from the list -- a group lookup
	// that failed, an allowlist that does not parse -- is worth saying.
	if msg := err.Error(); !strings.HasSuffix(msg, " is not allowed by -ui-allowed-principals") {
		page.Reason = msg
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	if err := principalRefusalPage.Execute(w, page); err != nil {
		fmt.Fprintln(w, "not allowed")
	}
}
