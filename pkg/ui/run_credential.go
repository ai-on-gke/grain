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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A run credential is what one run's `grain mcpserver` reaches the daemon
// with in place of the access code: a bearer token bound to that run's
// task and run ids, good for as long as the run is live and no longer
// (docs/access-code.md, "Runs"). It is what lets an acting-task request
// (ActingTaskHeader) be believed -- the task it names is the one the
// credential was minted for, not whatever its sender wrote -- and so what
// lets `grain access-code mode off` refuse the code on every request.
//
// The token is stateless: grr_<task>.<run>.<mac>, the two ids base64url
// encoded and the MAC an HMAC-SHA256 of both under the deployment's run
// credential key (RunCredentialKeyPath). Nothing is kept per run, so a
// run this daemon adopts across a restart keeps the credential it was
// started with; what ends one is its run finishing (Store.RunFinished),
// checked on every request.

// RunCredentialPrefix begins every run credential.
const RunCredentialPrefix = "grr_"

// RunCredentialEnv names the environment variable a run's `grain
// mcpserver` is handed its run credential in (mcp.RunCredentialEnv).
const RunCredentialEnv = mcp.RunCredentialEnv

// RunCredentialKeyFileName is the run credential key's file name under
// -data-dir.
const RunCredentialKeyFileName = "run-credential-key"

// RunCredentialKeyPath is where a daemon started with dataDir keeps the
// key its run credentials are signed with.
func RunCredentialKeyPath(dataDir string) string {
	return filepath.Join(dataDir, RunCredentialKeyFileName)
}

// EnsureRunCredentialKey returns the key in the file at path, writing a
// new random one (mode 0600) first if there is none. Rotating it is
// deleting the file and restarting the daemon, which ends every live
// run's credential with it.
func EnsureRunCredentialKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(key) >= 32 {
			return key, nil
		}
		return nil, fmt.Errorf("%s does not hold a run credential key: remove it to have a new one written", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another process wrote it first: use theirs.
		return EnsureRunCredentialKey(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

// MintRunCredential is the run credential for taskID's run runID under
// key.
func MintRunCredential(key []byte, taskID, runID string) (string, error) {
	taskID, runID = strings.TrimSpace(taskID), strings.TrimSpace(runID)
	if len(key) == 0 {
		return "", errors.New("no run credential key")
	}
	if taskID == "" || runID == "" {
		return "", errors.New("a run credential names both a task and a run")
	}
	enc := base64.RawURLEncoding
	body := enc.EncodeToString([]byte(taskID)) + "." + enc.EncodeToString([]byte(runID))
	return RunCredentialPrefix + body + "." + enc.EncodeToString(runCredentialMAC(key, body)), nil
}

func runCredentialMAC(key []byte, body string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("grain run credential v1\x00" + body))
	return m.Sum(nil)
}

// runCredential is the run a verified run credential was minted for.
type runCredential struct {
	TaskID string
	RunID  string
}

// parseRunCredential checks tok's MAC under key and returns the run it
// names. It does not ask whether that run is still live
// (Server.authenticateRunCredential does).
func parseRunCredential(key []byte, tok string) (runCredential, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(tok), RunCredentialPrefix)
	if !ok || len(key) == 0 {
		return runCredential{}, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return runCredential{}, false
	}
	enc := base64.RawURLEncoding
	mac, err := enc.DecodeString(parts[2])
	if err != nil || !hmac.Equal(mac, runCredentialMAC(key, parts[0]+"."+parts[1])) {
		return runCredential{}, false
	}
	task, err1 := enc.DecodeString(parts[0])
	run, err2 := enc.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(task) == 0 || len(run) == 0 {
		return runCredential{}, false
	}
	return runCredential{TaskID: string(task), RunID: string(run)}, true
}

// presentedRunCredential is the run credential r carries as its bearer
// token, if it carries one.
func presentedRunCredential(r *http.Request) (string, bool) {
	tok, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(tok, RunCredentialPrefix) {
		return "", false
	}
	return tok, true
}

type runCredentialContextKey struct{}

func withRunCredential(ctx context.Context, cred runCredential) context.Context {
	return context.WithValue(ctx, runCredentialContextKey{}, cred)
}

// runCredentialFromContext is the run whose credential admitted the
// request, if one did.
func runCredentialFromContext(ctx context.Context) (runCredential, bool) {
	if ctx == nil {
		return runCredential{}, false
	}
	cred, ok := ctx.Value(runCredentialContextKey{}).(runCredential)
	return cred, ok
}

// runCredentialKey is the key this server verifies run credentials with,
// nil when it was given none.
func (s *Server) runCredentialKey() []byte {
	if s.tasks == nil {
		return nil
	}
	return s.tasks.Config.RunCredentialKey
}

// runCredentialsRequired reports whether an acting-task request has to
// carry its run's own credential: on a server that issues them, the
// access code no longer vouches for which task a request acts for.
func (s *Server) runCredentialsRequired() bool {
	return len(s.runCredentialKey()) > 0
}

// authenticateRunCredential is serveHTTP's step for a request presenting
// a run credential: it checks the credential, that its run is still live,
// and that the request names no other task or run than the credential's,
// then pins ActingTaskHeader to that task (so agentAPIGate holds every
// call of it to its grants) only when the request asked to act for it.
// It answers the request itself and reports false on any refusal.
func (s *Server) authenticateRunCredential(w http.ResponseWriter, r *http.Request, tok string) (*http.Request, bool) {
	cred, ok := parseRunCredential(s.runCredentialKey(), tok)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("invalid run credential"))
		return r, false
	}
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusUnauthorized, errors.New("this server has no store to check a run credential's run against"))
		return r, false
	}
	finished, found, err := s.tasks.Store.RunFinished(r.Context(), cred.RunID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("checking run %s: %w", cred.RunID, err))
		return r, false
	}
	if !found || finished {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("run credential for run %s is no longer valid: the run has finished", cred.RunID))
		return r, false
	}
	if named := strings.TrimSpace(r.Header.Get(ActingTaskHeader)); named != "" && named != cred.TaskID {
		writeError(w, http.StatusForbidden, fmt.Errorf("run %s's credential acts for task %s, not task %s", cred.RunID, cred.TaskID, named))
		return r, false
	}
	if named := strings.TrimSpace(r.Header.Get(ActingRunHeader)); named != "" && named != cred.RunID {
		writeError(w, http.StatusForbidden, fmt.Errorf("run %s's credential cannot speak for run %s", cred.RunID, named))
		return r, false
	}
	r.Header.Del("Authorization")
	r.Header.Del("X-Grain-Tunnel-Token")
	if strings.TrimSpace(r.Header.Get(ActingTaskHeader)) != "" {
		r.Header.Set(ActingRunHeader, cred.RunID)
	}
	return r.WithContext(withRunCredential(r.Context(), cred)), true
}

// errActingTaskNeedsRunCredential refuses an acting-task request that
// only the access code admitted: the code says nothing about which task
// its holder is, so on a server issuing run credentials a request acts
// for a task only with that task's run's own.
var errActingTaskNeedsRunCredential = errors.New("acting for a task (" + ActingTaskHeader + ") needs that task's run credential: " +
	"the access code does not say which task you are (docs/access-code.md)")

// actingTaskCredentialError is why r may not act for the task its
// ActingTaskHeader names on the strength of how it was admitted, nil when
// it may: it came with its run's credential, in-process, or under a
// verified identity, or this server issues no run credentials.
func (s *Server) actingTaskCredentialError(r *http.Request) error {
	if !s.runCredentialsRequired() {
		return nil
	}
	if _, ok := runCredentialFromContext(r.Context()); ok {
		return nil
	}
	if trustedRequestSource(r) == model.SourceAccessCode {
		return errActingTaskNeedsRunCredential
	}
	return nil
}
