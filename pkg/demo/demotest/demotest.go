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

// Package demotest starts the seeded demo `grain demo` serves, in-process,
// for Go tests that assert on what the server does: the Go half of the
// classic Playwright suite (ui/e2e), whose tests each ran against a `grain
// demo` of their worker's (ui/e2e/fixtures.js). See
// docs/test-plans/classic-ui-e2e-migration/README.md, "Harness -> Bucket 1".
//
// Start runs demo.Start -- the same store, seed, fake CI, GitHub sim and
// access code `grain demo` runs, from the same flags -- on 127.0.0.1:0
// over the test's own temp directory, so every test gets a freshly seeded
// store of its own rather than a worker's shared one:
//
//	srv := demotest.Start(t, demotest.Options{Args: []string{"-github-sim"}})
//	res := srv.Do(t, "POST", "/api/tasks", map[string]any{"title": "x"})
//	res.WantStatus(t, 201)
//
// GRAIN_E2E_DB_DRIVER=postgres (and GRAIN_E2E_DB_DSN) put the store on
// PostgreSQL exactly as they do for the classic suite. CLI runs the real
// `grain` binary against the server; a package that uses it calls Main
// from its TestMain.
package demotest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/cli"
	"github.com/ai-on-gke/grain/pkg/demo"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Options is how a test's demo is started.
type Options struct {
	// Args are `grain demo`'s own flags ("-github-sim", "-fake-ci", ...),
	// as a classic spec's test.use({demoArgs}) gave them. -addr, -open and
	// -access-code-file are supplied, as are -db-driver and -db-dsn from
	// the environment unless Args names them.
	Args []string
}

// Server is a running demo, stopped when its test ends.
type Server struct {
	// URL is where it listens: http://127.0.0.1:<port>, no trailing slash.
	URL string
	// AccessCode is the code every caller signs in with; it stays the
	// same across Restart.
	AccessCode string
	// HTTP is a client that signs every request in with AccessCode, as
	// the grain CLI does (Authorization: Bearer).
	HTTP *http.Client
	// API is grain's own typed client against the server, signed in.
	API *ui.HTTPClient
	// Store is the server's store, for a test that has to look under the
	// API. Prefer the API: that is what a classic spec asserted on.
	Store *model.Store
	// DBDriver is the database the store is on: "sqlite", or "postgres"
	// under GRAIN_E2E_DB_DRIVER=postgres.
	DBDriver string
	// Seed names the rows `grain demo` seeds, so a test need not hard-code
	// their ids.
	Seed Seed

	t        testing.TB
	codeFile string
	args     []string
	demo     *demo.Server
	served   chan error
}

// Seed is the seeded rows ported tests lean on, read back from the server
// once it is up. Their titles are pkg/demo's seedDemo.
type Seed struct {
	// Repo is the repository every seeded task targets, and
	// -github-sim's simulated one: acme/widgets.
	Repo string
	// ParkedQuestionTask is the id of the task waiting on its owner's
	// answer to a question ("Investigate the merge queue stall").
	ParkedQuestionTask string
	// Boards is every seeded saved board's id by name: "Board" (kanban),
	// "Timeline", "Report" and "Tree".
	Boards map[string]string
	// Tasks is every seeded task's id by title.
	Tasks map[string]string
}

// ParkedQuestionTitle is the title of the seeded task parked on a
// question to its owner.
const ParkedQuestionTitle = "Investigate the merge queue stall"

// Start boots a seeded demo for t and stops it when t ends. It fails t
// rather than returning an error: nothing a test can do with a demo that
// did not start.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	codeFile := filepath.Join(t.TempDir(), "access-code")
	if i := slices.Index(opts.Args, "-access-code-file"); i >= 0 && i+1 < len(opts.Args) {
		codeFile = opts.Args[i+1]
	}
	s := &Server{t: t, codeFile: codeFile}
	s.start(opts.Args)
	t.Cleanup(s.stop)
	return s
}

// Restart stops the server and starts a freshly seeded one with args in
// place of the flags it was started with (nil keeps them), on a new port
// and store but the same access code: a classic spec's change of
// test.use({demoArgs}), or a check that a client survives the daemon
// going away. A demo's store is throwaway, so nothing written before a
// Restart survives it. Every field of s is updated.
func (s *Server) Restart(args ...string) {
	s.t.Helper()
	s.stop()
	if args == nil {
		args = s.args
	}
	s.start(args)
}

func (s *Server) start(args []string) {
	t := s.t
	t.Helper()
	s.args = args
	full := []string{"-addr", "127.0.0.1:0", "-open=false"}
	if driver := os.Getenv("GRAIN_E2E_DB_DRIVER"); driver != "" && !hasFlag(args, "-db-driver") {
		full = append(full, "-db-driver", driver)
		if dsn := os.Getenv("GRAIN_E2E_DB_DSN"); dsn != "" && !hasFlag(args, "-db-dsn") {
			full = append(full, "-db-dsn", dsn)
		}
	}
	full = append(full, args...)
	if !hasFlag(args, "-access-code-file") {
		full = append(full, "-access-code-file", s.codeFile)
	}
	opts, err := cli.ParseDemoArgs(full)
	if err != nil {
		t.Fatalf("grain demo %s: %v", strings.Join(args, " "), err)
	}
	// The test's own directory rather than a throwaway one: testing
	// removes it, after the cleanup that stops the server (an embedded
	// PostgreSQL included) has run.
	opts.Dir = t.TempDir()
	d, err := demo.Start(opts)
	if err != nil {
		t.Fatalf("grain demo %s: %v", strings.Join(args, " "), err)
	}
	s.demo = d
	s.served = make(chan error, 1)
	go func() { s.served <- d.Serve() }()
	s.URL, s.AccessCode, s.Store, s.DBDriver = d.URL, d.AccessCode, d.Store, d.DBDriver
	s.HTTP = &http.Client{
		Timeout:   2 * time.Minute,
		Transport: bearer{code: d.AccessCode, next: http.DefaultTransport},
	}
	s.API = ui.NewHTTPClient(d.URL)
	s.API.Token = d.AccessCode
	s.API.RetryPolicy = &ui.RetryPolicy{} // one attempt: a test sees the refusal, not a retry

	// The check fixtures.js makes before any test runs: a server that is
	// listening and cannot answer fails here, once.
	s.Do(t, http.MethodGet, "/api/config", nil).WantStatus(t, http.StatusOK)
	s.Seed = s.readSeed()
}

func (s *Server) stop() {
	if s.demo == nil {
		return
	}
	s.demo.Close()
	if err := <-s.served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.t.Errorf("grain demo stopped serving: %v", err)
	}
	s.demo = nil
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") || a == "-"+name || strings.HasPrefix(a, "-"+name+"=") {
			return true
		}
	}
	return false
}

func (s *Server) readSeed() Seed {
	t := s.t
	t.Helper()
	seed := Seed{Repo: "acme/widgets", Boards: map[string]string{}, Tasks: map[string]string{}}
	var tasks []struct{ ID, Title string }
	s.Do(t, http.MethodGet, "/api/tasks", nil).WantStatus(t, http.StatusOK).Items(t, &tasks)
	for _, task := range tasks {
		seed.Tasks[task.Title] = task.ID
	}
	seed.ParkedQuestionTask = seed.Tasks[ParkedQuestionTitle]
	var boards []struct{ ID, Name string }
	s.Do(t, http.MethodGet, "/api/boards", nil).WantStatus(t, http.StatusOK).Items(t, &boards)
	for _, b := range boards {
		seed.Boards[b.Name] = b.ID
	}
	return seed
}

// bearer signs a request in with the access code unless it already
// carries an Authorization of its own.
type bearer struct {
	code string
	next http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.code)
	}
	return b.next.RoundTrip(r)
}

// Response is one answer from the server, read in full.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// What is the request, for messages: "POST /api/tasks".
	What string
}

// Header is one extra request header for Do.
type Header struct{ Name, Value string }

// Do sends method path with body -- JSON-encoded unless it is already a
// []byte or a string -- signed in with the access code, and reads the
// answer. headers add to, or replace, the request's own; an
// Authorization among them replaces the access code.
func (s *Server) Do(t testing.TB, method, path string, body any, headers ...Header) *Response {
	t.Helper()
	return Request(t, s.HTTP, method, s.URL+path, body, headers...)
}

// Request is Do with any client, for a caller that is not signed in with
// the access code (a minted token, or nobody at all).
func Request(t testing.TB, c *http.Client, method, url string, body any, headers ...Header) *Response {
	t.Helper()
	var r io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = strings.NewReader(b)
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("encoding %s %s's body: %v", method, url, err)
		}
		r, contentType = bytes.NewReader(enc), "application/json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, h := range headers {
		req.Header.Set(h.Name, h.Value)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the answer: %v", method, url, err)
	}
	what := method + " " + req.URL.Path
	return &Response{Status: res.StatusCode, Header: res.Header, Body: data, What: what}
}

// OK is whether the status is 2xx, as fetch's res.ok is.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// WantStatus fails t unless the answer's status is want, showing the
// body, and returns r for chaining.
func (r *Response) WantStatus(t testing.TB, want int) *Response {
	t.Helper()
	if r.Status != want {
		t.Fatalf("%s = %d, want %d: %s", r.What, r.Status, want, r.Body)
	}
	return r
}

// WantOK fails t unless the answer is 2xx.
func (r *Response) WantOK(t testing.TB) *Response {
	t.Helper()
	if !r.OK() {
		t.Fatalf("%s = %d, want 2xx: %s", r.What, r.Status, r.Body)
	}
	return r
}

// JSON decodes the body into v, failing t if it is not JSON.
func (r *Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("%s: decoding %s: %v", r.What, r.Body, err)
	}
}

// Items decodes a list route's answer into v: a bare JSON array, or a
// page's {"items": [...]}.
func (r *Response) Items(t testing.TB, v any) {
	t.Helper()
	if trimmed := bytes.TrimSpace(r.Body); len(trimmed) > 0 && trimmed[0] == '[' {
		r.JSON(t, v)
		return
	}
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	r.JSON(t, &page)
	if err := json.Unmarshal(page.Items, v); err != nil {
		t.Fatalf("%s: decoding items of %s: %v", r.What, r.Body, err)
	}
}

// Error is the "error" field of a JSON refusal, or the body itself when
// it is not one.
func (r *Response) Error() string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(r.Body)
}

// String is the body as text.
func (r *Response) String() string { return string(r.Body) }

// ID is the "id" of a JSON object answer, as the task, schedule and
// release routes give back what they created.
func (r *Response) ID(t testing.TB) string {
	t.Helper()
	var v struct {
		ID json.RawMessage `json:"id"`
	}
	r.JSON(t, &v)
	id := strings.Trim(string(v.ID), `"`)
	if id == "" || id == "null" {
		t.Fatalf("%s answered no id: %s", r.What, r.Body)
	}
	return id
}
