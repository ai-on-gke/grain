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
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The access code's mode (docs/access-code.md, "Bootstrap and recovery
// only"): whether the code is the owner's everyday credential (on, the
// default and the behaviour before modes existed), only a way back in
// once people sign in with a verified identity (recovery), or not
// accepted at all (off).
//
// The mode lives in a file beside the code, not in Settings, and only
// `grain access-code mode` on the daemon's own host writes it: a code
// holder on the network must not be able to switch the code back on, and
// a person who has lost every verified sign-in still has the host. The
// daemon reads the file again within accessCodeRecheck, as it does the
// code.
type AccessCodeMode string

const (
	AccessCodeOn       AccessCodeMode = "on"
	AccessCodeRecovery AccessCodeMode = "recovery"
	AccessCodeOff      AccessCodeMode = "off"
)

// AccessCodeModeFileName is the mode's file in the data directory.
const AccessCodeModeFileName = "access-code-mode"

// AccessCodeModePath is the mode file for the token file at tokenPath:
// the same directory.
func AccessCodeModePath(tokenPath string) string {
	return filepath.Join(filepath.Dir(tokenPath), AccessCodeModeFileName)
}

// ParseAccessCodeMode is s as a mode; "" is on.
func ParseAccessCodeMode(s string) (AccessCodeMode, error) {
	switch m := AccessCodeMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", AccessCodeOn:
		return AccessCodeOn, nil
	case AccessCodeRecovery, AccessCodeOff:
		return m, nil
	}
	return "", fmt.Errorf("access-code mode %q is not on, recovery or off", s)
}

// WriteAccessCodeMode writes mode to the mode file beside tokenPath,
// owner-only, atomically.
func WriteAccessCodeMode(tokenPath string, mode AccessCodeMode) error {
	path := AccessCodeModePath(tokenPath)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+AccessCodeModeFileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(string(mode) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadAccessCodeMode is the mode in the file beside tokenPath: on when
// there is none. A file holding anything else is off, so a mistyped
// mode never opens the code wider than whoever wrote it meant.
func ReadAccessCodeMode(tokenPath string) AccessCodeMode {
	b, err := os.ReadFile(AccessCodeModePath(tokenPath))
	if err != nil {
		return AccessCodeOn
	}
	return accessCodeModeOf(string(b))
}

func accessCodeModeOf(s string) AccessCodeMode {
	m, err := ParseAccessCodeMode(s)
	if err != nil {
		return AccessCodeOff
	}
	return m
}

// accessCodeModeNow is the mode as it stands: the file beside
// Config.LocalAPITokenFile, re-read as the code is, else
// Config.AccessCodeMode.
func (s *Server) accessCodeModeNow() AccessCodeMode {
	if s == nil || s.tasks == nil {
		return AccessCodeOn
	}
	f := s.accessCodeMode.Load()
	if f == nil {
		path := ""
		if tok := s.tasks.Config.LocalAPITokenFile; tok != "" {
			path = AccessCodeModePath(tok)
		}
		s.accessCodeMode.CompareAndSwap(nil, &accessCodeFile{path: path, fallback: string(s.tasks.Config.AccessCodeMode)})
		f = s.accessCodeMode.Load()
	}
	if f.path != "" {
		if _, err := os.Stat(f.path); errors.Is(err, os.ErrNotExist) {
			return AccessCodeOn
		}
	}
	return accessCodeModeOf(f.current())
}

// accessCodeRestricted reports whether a request the code authenticates
// is held to recovery routes: the mode is recovery and somebody can sign
// in without the code -- a person on the allowlist with a full role and a
// verifier configured to sign them in. Until then recovery is the
// bootstrap it would otherwise lock out, and the code works as on.
func (s *Server) accessCodeRestricted(ctx context.Context, cfg IAPConfig) bool {
	if s.accessCodeModeNow() != AccessCodeRecovery {
		return false
	}
	return s.verifiedAdminCanSignIn(ctx, cfg)
}

// verifiedAdminCanSignIn reports whether some person could sign in
// through a verifier with every scope: a user: entry on the allowlist
// whose role is full, on a daemon that verifies an identity (IAP, or a
// registered verifier such as an identity proxy).
func (s *Server) verifiedAdminCanSignIn(ctx context.Context, cfg IAPConfig) bool {
	if iapAudienceFor(ctx) == "" && requiredVerifier(cfg) == "" && len(registeredVerifiersSnapshot()) == 0 {
		return false
	}
	entries, err := s.effectiveAllowedPrincipals(ctx)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Prefix != "user" {
			continue
		}
		role := e.Role
		if role == "" {
			role = model.DefaultAccessRole
		}
		if resolveAccessRoles(e.Value, []string{role}).Full {
			return true
		}
	}
	return false
}

// accessCodeRecoveryRoute reports whether r is one a recovery-mode code
// session may use: managing access and settings, signing in and out, and
// the frontend that does both. Everything else is a verified person's.
func accessCodeRecoveryRoute(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/") && p != "/mcp" && !strings.HasPrefix(p, model.PersistentSandboxSubpathPrefix) {
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	for _, prefix := range []string{"/api/access/", "/api/api-scope-sets", "/api/auth/", "/api/settings"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	switch p {
	case "/api/config", "/api/whoami", "/api/deployment", "/api/deployment/config-audit", "/api/restart-watch":
		return true
	}
	return false
}

const errAccessCodeRecovery = "the access code is in recovery mode (grain access-code mode): it only manages access and settings " +
	"(Settings -> Access) now that a person signs in with a verified identity; sign in that way for everything else"

const errAccessCodeOff = "the access code is switched off on this daemon (grain access-code mode off): sign in with a verified identity, " +
	"or run `grain access-code mode on` on the daemon's own host"

// accessCodeUse is one request the code authenticated, as the access
// history and the recovery notification record it.
type accessCodeUse struct {
	Name   string
	Source string
	Method string
	Path   string
}

func (u accessCodeUse) who() string {
	if u.Name == "" {
		return "an unnamed access-code session"
	}
	return u.Name
}

// mutatingMethod reports whether method changes state.
func mutatingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// recordAccessCodeUse writes u to the access history (config audit,
// surface access-code) when it changed state, and in recovery mode raises
// the Inbox notification that says the code was used -- at most once a
// minute per name and source, so a page's reads do not each raise one,
// though each raise still counts.
func (s *Server) recordAccessCodeUse(ctx context.Context, u accessCodeUse, recovery bool) {
	if s.tasks == nil || s.tasks.Store == nil {
		return
	}
	at := s.tasks.now()
	if mutatingMethod(u.Method) {
		summary := fmt.Sprintf("access code: %s %s by %s from %s", u.Method, u.Path, u.who(), u.Source)
		by := RequestActor(ctx)
		if _, err := s.tasks.Store.RecordConfigAudit(context.WithoutCancel(ctx), model.ConfigAuditEntry{
			At: at, By: by.Actor, OnBehalfOf: by.OnBehalfOf, Surface: "access-code", Summary: summary,
		}); err != nil {
			log.Printf("grain access: recording %q: %v", summary, err)
		}
	}
	if !recovery || !s.accessCodeNotices.due(u.Name+"\x00"+u.Source, at) {
		return
	}
	req := RaiseNotificationRequest{
		Subject:  "access-code:recovery:" + u.Source,
		Severity: string(model.NotificationWarning),
		Title:    "The access code was used in recovery mode",
		Body: fmt.Sprintf("%s used the access code from %s (%s %s). In recovery mode the code only manages access and settings; "+
			"if this was not you, rotate it on the daemon's host (grain access-code rotate).", u.who(), u.Source, u.Method, u.Path),
	}
	if _, err := s.tasks.raise(context.WithoutCancel(ctx), req, model.PrincipalString(RequestActor(ctx).Actor)); err != nil {
		log.Printf("grain access: raising the recovery-use notification: %v", err)
	}
}

// accessNoticeThrottle holds recovery-use notifications to one a minute
// per key.
type accessNoticeThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

const accessNoticeEvery = time.Minute

func (t *accessNoticeThrottle) due(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if prev, ok := t.last[key]; ok && now.Sub(prev) < accessNoticeEvery {
		return false
	}
	if len(t.last) > accessMaxSources {
		t.last = map[string]time.Time{}
	}
	t.last[key] = now
	return true
}

// accessNameAllowed reports why name may not sign in with the access code
// through the UI, or nil: it must be an email address, and when the
// allowlist names anybody, one it admits -- so the name an access-code
// session records is somebody the deployment knows.
func (s *Server) accessNameAllowed(ctx context.Context, name string) error {
	local, domain, ok := strings.Cut(name, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") {
		return errors.New("give your email address with the access code, so what you do is recorded as you")
	}
	if s.tasks == nil {
		return nil
	}
	entries, err := s.effectiveAllowedPrincipals(ctx)
	if err != nil {
		return err
	}
	anyPerson := false
	for _, e := range entries {
		if e.Prefix == "user" || e.Prefix == "group" {
			anyPerson = true
			break
		}
	}
	if !anyPerson {
		return nil
	}
	if _, err := s.allowedPrincipalRoles(ctx, VerifiedIdentity{Email: name, Kind: model.PrincipalHuman}, false); err != nil {
		return fmt.Errorf("%s is not on this deployment's allowlist (Settings -> Access): sign in with the email you are listed under", name)
	}
	return nil
}

type accessCodeNameContextKey struct{}

// withAccessCodeName records who the access code that admitted a request
// was presented by, for RequestActor.
func withAccessCodeName(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, accessCodeNameContextKey{}, name)
}

func accessCodeNameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(accessCodeNameContextKey{}).(string)
	return name
}

// AccessCodeActorPrefix begins the id of the principal a named access-code
// request is recorded as: access-code:<name>.
const AccessCodeActorPrefix = "access-code:"

// admitAccessCodeRequest is serveHTTP's step for a request the access
// code admitted (and no verified identity): it names the request's actor
// after whoever presented the code, holds a recovery-mode code to
// recovery routes, and records the use. A run's own plumbing
// (ActingTaskHeader) is the acting task's agent, held to its grants and
// recorded as that task, so it is let through as it was. It reports
// whether serveHTTP should go on, with r's context carrying the name.
func (s *Server) admitAccessCodeRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if strings.TrimSpace(r.Header.Get(ActingTaskHeader)) != "" {
		return r, true
	}
	cfg, _ := IAPConfigFromContext(r.Context())
	name := accessCodeName(r, cfg)
	r = r.WithContext(withAccessCodeName(r.Context(), name))
	recovery := s.accessCodeRestricted(r.Context(), cfg)
	use := accessCodeUse{Name: name, Source: accessSource(r), Method: r.Method, Path: r.URL.Path}
	if recovery && !accessCodeRecoveryRoute(r) {
		writeError(w, http.StatusForbidden, errors.New(errAccessCodeRecovery))
		return r, false
	}
	s.recordAccessCodeUse(r.Context(), use, recovery)
	return r, true
}

// OwnerEnv names the person a host's grain CLI acts for when it signs in
// with the access code (AccessCodeClientName).
const OwnerEnv = "GRAIN_OWNER"

// AccessCodeClientName is who this process says it is when it presents
// the access code (AccessNameHeader): $GRAIN_OWNER, else this host's
// account as <user>@<host>, else "".
func AccessCodeClientName() string {
	if name, ok := cleanAccessName(os.Getenv(OwnerEnv)); ok && name != "" {
		return name
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	host, _ := os.Hostname()
	name := u.Username
	if host != "" {
		name += "@" + host
	}
	if name, ok := cleanAccessName(name); ok {
		return name
	}
	return ""
}

// couldBeAccessCodeShape reports whether tok could be an access code at
// all -- not a JWT, not a gma_ external agent credential -- so the CLI
// names itself beside a code and not beside anybody else's credential.
func couldBeAccessCodeShape(tok string) bool {
	return tok != "" && !strings.ContainsAny(tok, "._")
}

// AccessCodeModeView is GET /api/access/access-code: the access code's
// mode as Settings -> Access shows it. It is read-only -- only
// `grain access-code mode` on the daemon's host changes the mode -- so
// it names the command that does, as the account that owns the code.
type AccessCodeModeView struct {
	// Mode is on, recovery or off.
	Mode AccessCodeMode `json:"mode"`
	// VerifiedAdminCanSignIn is whether a person with a full role on
	// the allowlist can sign in through a configured verifier.
	VerifiedAdminCanSignIn bool `json:"verifiedAdminCanSignIn"`
	// RecoveryInForce is whether the code is held to recovery routes
	// now: the mode is recovery and VerifiedAdminCanSignIn.
	RecoveryInForce bool `json:"recoveryInForce"`
	// ViewerVerified is whether the caller signed in with a verified
	// identity rather than the access code.
	ViewerVerified bool `json:"viewerVerified"`
	// Account owns the code's file; the mode command runs as it.
	Account string `json:"account"`
	// Command switches the mode to recovery; OnCommand back to on.
	Command   string `json:"command"`
	OnCommand string `json:"onCommand"`
}

// handleAccessCodeMode is GET /api/access/access-code.
func (s *Server) handleAccessCodeMode(w http.ResponseWriter, r *http.Request) {
	cfg, _ := IAPConfigFromContext(r.Context())
	view := AccessCodeModeView{Mode: s.accessCodeModeNow(), Account: "grain"}
	view.VerifiedAdminCanSignIn = s.verifiedAdminCanSignIn(r.Context(), cfg)
	view.RecoveryInForce = view.Mode == AccessCodeRecovery && view.VerifiedAdminCanSignIn
	if p, ok := PrincipalFromContext(r.Context()); ok {
		view.ViewerVerified = p.Verified()
	}
	cmd := "grain access-code"
	if s.tasks != nil {
		if tok := s.tasks.Config.LocalAPITokenFile; tok != "" {
			view.Account = FileOwner(tok)
			cmd += " -data-dir " + filepath.Dir(tok)
		}
	}
	prefix := "sudo -u " + view.Account + " " + cmd + " mode "
	view.Command = prefix + string(AccessCodeRecovery)
	view.OnCommand = prefix + string(AccessCodeOn)
	writeJSON(w, http.StatusOK, view)
}
