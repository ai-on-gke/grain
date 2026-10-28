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

// Package authfix turns a failure that is really a lapsed host credential
// into the one sentence that fixes it.
//
// A host tool that will not start, or a request answered with a redirect
// to a login page, usually names neither the cause nor the fix. Which
// failures mean "your credentials on this host have lapsed", and what a
// person does about it, is knowledge about one particular environment, not
// about grain: so core grain knows only the shape of the answer (Error,
// Fix) and asks classifiers registered at start-up whether they recognise
// a failure. A build that registers none recognises nothing, and every
// failure passes through unchanged.
//
// Two kinds of classifier are registered:
//
//   - an ExecClassifier reads a host tool that could not run (exec's own
//     error, an errno in its chain, or what the tool said on stderr);
//   - a RedirectClassifier reads where an HTTP request was redirected to.
//
// Every place grain starts a host tool -- the agent CLIs (pkg/agent), the
// command-backed credential sources (pkg/gitproxy), the model picker --
// calls Wrap, and orchestrator reads the result back out of any of them
// with As.
package authfix

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strings"
	"sync"
)

// Fix is what a classifier says about a failure it recognised.
type Fix struct {
	// Summary is what has happened, as a person reads it: "host
	// credentials expired".
	Summary string
	// Action is what fixes it, as an instruction: "run `renew` as alice
	// on build-1, then retry". Who says who and where.
	Action string
	// Reason is the failure in the words it arrived in, short: "required
	// key not available". It is what follows the path that could not run.
	Reason string
}

// ExecClassifier recognises a host tool that could not run because the
// host's credentials have lapsed. err is the failure as reported (nil when
// only stderr said anything) and stderr what the tool printed.
type ExecClassifier func(err error, stderr string) (Fix, bool)

// RedirectClassifier recognises a redirect to a login page that means the
// host's credentials have lapsed.
type RedirectClassifier func(location *url.URL) (Fix, bool)

var (
	mu        sync.RWMutex
	execs     = map[string]ExecClassifier{}
	redirects = map[string]RedirectClassifier{}
)

// RegisterExec adds c under name, replacing any classifier already there,
// and returns a function that removes it again (for tests). Classifiers
// are consulted in name order, and the first to recognise a failure wins.
func RegisterExec(name string, c ExecClassifier) (unregister func()) {
	mu.Lock()
	defer mu.Unlock()
	execs[name] = c
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(execs, name)
	}
}

// RegisterRedirect is RegisterExec for redirects.
func RegisterRedirect(name string, c RedirectClassifier) (unregister func()) {
	mu.Lock()
	defer mu.Unlock()
	redirects[name] = c
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(redirects, name)
	}
}

func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ClassifyExec is the first registered ExecClassifier's verdict on err and
// stderr.
func ClassifyExec(err error, stderr string) (Fix, bool) {
	if err == nil && stderr == "" {
		return Fix{}, false
	}
	mu.RLock()
	defer mu.RUnlock()
	for _, n := range sortedNames(execs) {
		if fix, ok := execs[n](err, stderr); ok {
			return fix, true
		}
	}
	return Fix{}, false
}

// ClassifyRedirect is the first registered RedirectClassifier's verdict on
// location.
func ClassifyRedirect(location *url.URL) (Fix, bool) {
	if location == nil {
		return Fix{}, false
	}
	mu.RLock()
	defer mu.RUnlock()
	for _, n := range sortedNames(redirects) {
		if fix, ok := redirects[n](location); ok {
			return fix, true
		}
	}
	return Fix{}, false
}

// Error is a host tool that could not run because the host's credentials
// have lapsed. What fixes it is Fix.Action; nothing about the task, the
// agent or grain's configuration.
type Error struct {
	// Path is the binary that could not be started, or that reported the
	// refusal; empty where nothing named one.
	Path string
	// Fix is the classifier's verdict.
	Fix Fix
	// Err is the failure as it was reported.
	Err error
}

func (e *Error) Error() string {
	msg := e.Fix.Summary + ": " + e.Fix.Action
	if e.Path != "" {
		msg += " (" + e.Path + " could not run: " + e.Fix.Reason + ")"
	} else {
		msg += " (" + e.Fix.Reason + ")"
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// As is err's Error, if it carries one.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Wrap is err as an *Error when a registered classifier recognises it (or
// stderr), and err unchanged otherwise -- nil included. path is the tool
// that was run, and is used where err does not name one itself
// (fork/exec's own *fs.PathError, or *exec.Error, does).
func Wrap(err error, stderr, path string) error {
	if _, ok := As(err); ok {
		return err
	}
	fix, ok := ClassifyExec(err, stderr)
	if !ok {
		return err
	}
	if p := failedPath(err); p != "" {
		path = p
	}
	if err == nil {
		err = errors.New(strings.TrimSpace(stderr))
	}
	return &Error{Path: path, Fix: fix, Err: err}
}

func failedPath(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path != "" {
		return pe.Path
	}
	var ee *exec.Error
	if errors.As(err, &ee) && ee.Name != "" {
		return ee.Name
	}
	return ""
}

// lookupUser and hostname are user.Current and os.Hostname, swapped by
// tests (a classifier's own included) through SetWho.
var (
	whoMu      sync.RWMutex
	lookupUser = user.Current
	hostname   = os.Hostname
)

// SetWho replaces what Who reads, and returns a function restoring it.
// It is for tests.
func SetWho(account, host string) (restore func()) {
	whoMu.Lock()
	defer whoMu.Unlock()
	prevUser, prevHost := lookupUser, hostname
	lookupUser = func() (*user.User, error) { return &user.User{Username: account}, nil }
	hostname = func() (string, error) { return host, nil }
	return func() {
		whoMu.Lock()
		defer whoMu.Unlock()
		lookupUser, hostname = prevUser, prevHost
	}
}

// Who is the account this process runs as and the host it runs on: who
// has to act on a Fix, and where. Each falls back to a description rather
// than to nothing, since the sentence is read by a person either way.
func Who() (account, host string) {
	whoMu.RLock()
	defer whoMu.RUnlock()
	account = os.Getenv("USER")
	if u, err := lookupUser(); err == nil && u.Username != "" {
		account = u.Username
	}
	if account == "" {
		account = "the daemon's account"
	}
	host, err := hostname()
	if err != nil || host == "" {
		host = "this host"
	}
	return account, host
}
