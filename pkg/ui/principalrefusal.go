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

const notAllowedPrincipalSuffix = " is not on the allowed users and groups list (Settings -> Access)"

// defaultPrincipalAdvice names Settings -> Access and `grain access allow`.
func defaultPrincipalAdvice(entry, _ string) string {
	if entry == "" {
		return "To let them in, an allowed user or access-code holder can add their user or group under Settings -> Access, or run grain access allow."
	}
	if strings.HasPrefix(entry, "serviceAccount:") {
		return "To let this service account in with full operator access, an allowed user or access-code holder can add " + entry +
			" with a reason under Settings -> Access, or run: grain access allow " + entry + " --reason <why>"
	}
	return "To let them in, an allowed user or access-code holder can add " + entry +
		" under Settings -> Access, or run: grain access allow " + entry
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
				if e.Prefix == "serviceAccount" || e.Prefix == "delegate" {
					continue
				}
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
	if msg := err.Error(); !strings.HasSuffix(msg, notAllowedPrincipalSuffix) {
		page.Reason = msg
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	if err := principalRefusalPage.Execute(w, page); err != nil {
		fmt.Fprintln(w, "not allowed")
	}
}

type delegateRefusal struct {
	PersonEmail, AppEmail, Label, Verifier string
}

var delegateRefusalPage = template.Must(template.New("delegate-refused").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: delegating app not allowed</title>
<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
main { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 30rem; width: 100%; box-sizing: border-box; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
p { color: #444; font-size: .9rem; overflow-wrap: anywhere; }
.who { color: #000; font-size: 1rem; }
</style>
</head>
<body>
<main>
<h1>Not allowed on this grain</h1>
<p class="who" id="refused-delegate">{{.Label}} is not allowed to act for people on this grain</p>
<p id="refused-principal">Signed in as <strong>{{.PersonEmail}}</strong> via <code>{{.AppEmail}}</code> (verified by {{.Verifier}}).</p>
<p id="refused-advice">To let this delegating app act for admitted users on this deployment, an allowed user or access-code holder can add <code>delegate:{{.AppEmail}}</code> with a reason under Settings -&gt; Access, or run: <code>grain access allow delegate:{{.AppEmail}} --reason &lt;why&gt;</code></p>
</main>
</body>
</html>
`))

// writeDelegateRefused answers a request from an admitted person whose
// delegating app (Via.Email) has no matching delegate: entry on the allowlist
// (AU-24.3): a browser navigation gets the refusal page naming the delegating
// app with no access-code form, and an API call gets 403 JSON with
// code="delegate_not_allowed".
func writeDelegateRefused(w http.ResponseWriter, r *http.Request, de *delegateNotAllowedError) {
	if browserNavigation(r) {
		label := de.label
		if label == "" {
			label = model.DelegateLabelForEmail(de.appEmail)
		}
		page := delegateRefusal{
			PersonEmail: de.personEmail,
			AppEmail:    de.appEmail,
			Label:       label,
			Verifier:    de.verifier,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusForbidden)
		if err := delegateRefusalPage.Execute(w, page); err != nil {
			fmt.Fprintln(w, "delegating app not allowed")
		}
		return
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":         de.Error(),
		"code":          "delegate_not_allowed",
		"delegatingApp": de.appEmail,
	})
}

// writeAssertionRefused answers a request whose signed identity header
// is present but does not verify: 401, never a fall-through to the
// access code (authentication.design.md, "A bad assertion is final"). A
// browser loading a UI page is shown a page that says so, rather than
// the API's JSON error, since an expired or rotated-key assertion
// reaches a person's browser, not only a forger's curl.
//
// A verifier that could not check the header yet
// (VerifierUnavailableError) is refused just the same, but with 503 and
// Retry-After, and a page that says to reload rather than advice about
// audiences and clocks.
func writeAssertionRefused(w http.ResponseWriter, r *http.Request, err error) {
	status, page := http.StatusUnauthorized, assertionRefusedPage
	if verifierUnavailable(err) {
		status, page = http.StatusServiceUnavailable, verifierStartingPage
		w.Header().Set("Retry-After", verifierRetryAfter)
		err = fmt.Errorf("identity assertion could not be checked yet: %w", err)
	} else {
		err = fmt.Errorf("identity assertion was not accepted: %w", err)
	}
	if !browserNavigation(r) {
		writeError(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := page.Execute(w, err.Error()); err != nil {
		fmt.Fprintln(w, "identity assertion was not accepted")
	}
}

// verifierRetryAfter is the Retry-After, in seconds, on a refusal whose
// verifier was not available yet.
const verifierRetryAfter = "5"

var verifierStartingPage = template.Must(template.New("verifier-starting").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: sign-in verification is starting up</title>
<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
main { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 30rem; width: 100%; box-sizing: border-box; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
p { color: #444; font-size: .9rem; overflow-wrap: anywhere; }
code { font-size: .85rem; }
</style>
</head>
<body>
<main>
<h1>Sign-in verification is starting up</h1>
<p>grain could not check the signed identity the proxy in front of it attached to this request yet -- usually because the daemon has just restarted -- so it has not let the request in.</p>
<p id="refused-reason"><code>{{.}}</code></p>
<p>Reload the page in a few seconds and it should work.</p>
</main>
</body>
</html>
`))

var assertionRefusedPage = template.Must(template.New("assertion-refused").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: sign-in not verified</title>
<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
main { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 30rem; width: 100%; box-sizing: border-box; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
p { color: #444; font-size: .9rem; overflow-wrap: anywhere; }
code { font-size: .85rem; }
</style>
</head>
<body>
<main>
<h1>Sign-in could not be verified</h1>
<p>The signed identity the proxy in front of grain attached to this request did not verify, so grain refused it. The access code is not consulted when a signed identity fails.</p>
<p id="refused-reason"><code>{{.}}</code></p>
<p>Reload the page to sign in again. If it keeps happening, the owner checks that the daemon's <code>-ui-iap-audience</code>, <code>-ui-iap-issuer</code> and <code>-ui-iap-keys-url</code> match the proxy's backend and that this host's clock is right.</p>
</main>
</body>
</html>
`))
