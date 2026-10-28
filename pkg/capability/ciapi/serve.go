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

package ciapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// maxBody bounds the one request body the listener reads.
const maxBody = 64 << 10

// legacyPaths are paths the release gate answered before it was this
// listener, kept so a script written against it still reads.
var legacyPaths = map[string]string{
	"/api/system/persistent-sandboxes": "/api/persistent-sandboxes",
}

// ServeHTTP implements http.Handler.
func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	grant, ok := e.Tokens.Lookup(strings.TrimSpace(token))
	if !ok {
		e.logf("ci api: refused %s %s: no live token", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "grain's CI API needs a live token from a job's api grants "+
			"(Authorization: Bearer $"+TokenEnv+")")
		return
	}
	upstream := e.handler()
	if upstream == nil {
		writeError(w, http.StatusServiceUnavailable, "grain's CI API is not connected to grain's API yet")
		return
	}
	forward, reason, status := Route(r, grant, upstream)
	if forward == nil {
		e.logf("ci api: refused run %s (%s) %s %s: %s", grant.RunID, grant.Repo, r.Method, r.URL.Path, reason)
		writeError(w, status, reason)
		return
	}
	e.logf("ci api: run %s (%s) %s %s", grant.RunID, grant.Repo, forward.Method, forward.URL.RequestURI())
	if forward.Method != http.MethodGet {
		upstream.ServeHTTP(w, forward)
		return
	}
	serveNarrowed(w, upstream, forward, grant.Repo)
}

// Route decides what r, authorized by grant, may be answered with: the
// request to hand upstream, built afresh so that nothing of r's but what
// is allowed reaches it, or why not and with which status. A request is
// matched against upstream's own route table, and the token must hold
// that route's scope.
func Route(r *http.Request, grant Grant, upstream Upstream) (forward *http.Request, reason string, status int) {
	path := strings.TrimRight(r.URL.Path, "/")
	if to, ok := legacyPaths[path]; ok {
		path = to
	}
	if r.Method == http.MethodPost && path == "/api/notifications/release-pending" {
		if !grant.MayFile("release-notes") {
			return nil, fmt.Sprintf("POST /api/notifications/release-pending needs release-gate-read (api:release-gate-read), "+
				"which this job's token does not hold (it holds %s)", scopeList(grant.Scopes)), http.StatusForbidden
		}
		body, reason := releasePendingBody(r, grant.Repo)
		if reason != "" {
			return nil, reason, http.StatusForbidden
		}
		return newForward(r.Context(), http.MethodPost, "/api/notifications", nil, body), "", 0
	}
	probe := newForward(r.Context(), r.Method, path, nil, nil)
	pattern, scope, ok := upstream.APIRoute(probe)
	if !ok {
		return nil, fmt.Sprintf("%s %s is not a route of grain's API a CI job's token can reach", r.Method, r.URL.Path),
			http.StatusForbidden
	}
	if !grant.Holds(scope) {
		return nil, fmt.Sprintf("%s needs api:%s, which this job's token does not hold (it holds %s)",
			pattern, scope, scopeList(grant.Scopes)), http.StatusForbidden
	}
	if reason := narrowPath(pattern, path, grant.Repo); reason != "" {
		return nil, reason, http.StatusForbidden
	}
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodGet:
		if reason := narrowRepo(query, grant.Repo); reason != "" {
			return nil, reason, http.StatusForbidden
		}
		return newForward(r.Context(), http.MethodGet, path, query, nil), "", 0
	case scope == "tasks:from-template":
		body, reason := fromTemplateBody(r, grant)
		if reason != "" {
			return nil, reason, http.StatusForbidden
		}
		return newForward(r.Context(), r.Method, path, nil, body), "", 0
	}
	// model.CIAPIScopeRefusal admits no other write, so this is a token
	// minted before a rule changed, or a route whose method and scope
	// disagree: either way, nothing a job may write.
	return nil, fmt.Sprintf("%s is a write a CI job's token cannot make", pattern), http.StatusForbidden
}

func scopeList(scopes []model.APIScope) string {
	if len(scopes) == 0 {
		return "none"
	}
	names := make([]string, len(scopes))
	for i, s := range scopes {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// narrowRepo holds a listing to repo: one that names none is given it,
// and one that names another is refused.
func narrowRepo(query url.Values, repo model.RepoRef) string {
	for _, raw := range query["repo"] {
		asked := strings.TrimSpace(raw)
		if asked != "" && !strings.EqualFold(asked, repo.String()) {
			return fmt.Sprintf("this token reads %s only, not %s", repo, asked)
		}
	}
	query.Set("repo", repo.String())
	return ""
}

// narrowPath refuses a route whose path names a repository --
// /api/repos/{owner}/{name}/... -- other than repo.
func narrowPath(pattern, path string, repo model.RepoRef) string {
	_, patternPath, _ := strings.Cut(pattern, " ")
	want := strings.Split(strings.Trim(patternPath, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	var owner, name string
	for i, seg := range want {
		if i >= len(got) {
			break
		}
		switch strings.Trim(seg, "{}.") {
		case "owner":
			owner = got[i]
		case "name", "repo":
			name = got[i]
		}
	}
	if owner == "" && name == "" {
		return ""
	}
	if !strings.EqualFold(owner, repo.Owner) || !strings.EqualFold(name, repo.Name) {
		return fmt.Sprintf("this token reaches %s only, not %s/%s", repo, owner, name)
	}
	return ""
}

// serveNarrowed answers forward, a GET, with upstream's answer less
// whatever in it belongs to a repository other than repo: an answer
// whose own "repo" is another's is refused as not found, and a list
// element whose "repo" is another's is dropped, to two levels deep
// (a list, or a list in an object -- {"runs": [...]}). An answer that is
// not a 200 holding JSON is passed on unchanged.
func serveNarrowed(w http.ResponseWriter, upstream http.Handler, forward *http.Request, repo model.RepoRef) {
	buf := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	upstream.ServeHTTP(buf, forward)
	body := buf.body.Bytes()
	if buf.status == http.StatusOK && strings.Contains(buf.header.Get("Content-Type"), "json") {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("reading grain's answer: %v", err))
			return
		}
		if foreign(v, repo) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("not found among %s's", repo))
			return
		}
		v = narrowValue(v, repo, 2)
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("encoding grain's answer: %v", err))
			return
		}
		body = out.Bytes()
		buf.header.Del("Content-Length")
	}
	for k, vs := range buf.header {
		w.Header()[k] = vs
	}
	w.WriteHeader(buf.status)
	_, _ = w.Write(body)
}

// foreign reports whether v is an object whose "repo" names a repository
// other than repo.
func foreign(v any, repo model.RepoRef) bool {
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	name, ok := obj["repo"].(string)
	return ok && strings.TrimSpace(name) != "" && !strings.EqualFold(strings.TrimSpace(name), repo.String())
}

func narrowValue(v any, repo model.RepoRef, depth int) any {
	if depth <= 0 {
		return v
	}
	switch t := v.(type) {
	case []any:
		kept := make([]any, 0, len(t))
		for _, e := range t {
			if !foreign(e, repo) {
				kept = append(kept, narrowValue(e, repo, depth-1))
			}
		}
		return kept
	case map[string]any:
		for k, e := range t {
			if list, ok := e.([]any); ok {
				t[k] = narrowValue(list, repo, depth-1)
			}
		}
		return t
	}
	return v
}

// bufferedResponse holds an upstream answer for serveNarrowed to read.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(status int)      { b.status = status }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

// FromTemplateRequest is what a job posts to POST
// /api/tasks/from-template: the template, by its stem, and its fields.
// Repo may be left out, or name the job's own; Template may be left out
// when the token may file from exactly one.
type FromTemplateRequest struct {
	Repo     string            `json:"repo,omitempty"`
	Template string            `json:"template,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// fromTemplateBody reads a FromTemplateRequest off r and writes the
// body grain's own POST /api/tasks/from-template takes, held to the
// job's repository and to the templates the grant names; the task is
// filed approved, as the trusted job that filed it was. Or it says why
// it is refused.
func fromTemplateBody(r *http.Request, grant Grant) ([]byte, string) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Sprintf("reading the request: %v", err)
	}
	if len(data) > maxBody {
		return nil, "the request body is too large"
	}
	var req FromTemplateRequest
	if len(bytes.TrimSpace(data)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return nil, fmt.Sprintf("reading the request: %v (it takes only repo, template and fields)", err)
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, "reading the request: trailing data after JSON object"
		}
	}
	repo := grant.Repo
	if asked := strings.TrimSpace(req.Repo); asked != "" && !strings.EqualFold(asked, repo.String()) {
		return nil, fmt.Sprintf("this token files tasks for %s only, not %s", repo, asked)
	}
	template := strings.TrimSpace(req.Template)
	if template == "" && !grant.AnyTemplate && len(grant.Templates) == 1 {
		template = grant.Templates[0]
	}
	stem := templateStem(repo, template)
	if stem == "" || !grant.MayFile(stem) {
		return nil, fmt.Sprintf("this token files tasks from %s's %s only, not %q", repo, templateList(grant), template)
	}
	body, err := json.Marshal(struct {
		Repo     string            `json:"repo"`
		Template string            `json:"template"`
		Fields   map[string]string `json:"fields,omitempty"`
		Approved bool              `json:"approved"`
	}{repo.String(), stem, req.Fields, true})
	if err != nil {
		return nil, fmt.Sprintf("encoding the request: %v", err)
	}
	return body, ""
}

// templateStem is the stem template names among repo's declared
// templates: itself, or the stem of repo's own declared id.
func templateStem(repo model.RepoRef, template string) string {
	if rest, ok := strings.CutPrefix(template, model.RepoDeclaredID(model.DeclaredTemplate, repo, "")); ok {
		template = rest
	}
	if strings.ContainsAny(template, ":/\\") || strings.HasPrefix(template, ".") {
		return ""
	}
	return template
}

func templateList(grant Grant) string {
	if grant.AnyTemplate {
		return ".grain/templates"
	}
	names := make([]string, len(grant.Templates))
	for i, t := range grant.Templates {
		names[i] = ".grain/templates/" + t + ".yml"
	}
	if len(names) == 0 {
		return "templates (none)"
	}
	return strings.Join(names, ", ")
}

// ReleasePendingRequest is the body of POST
// /api/notifications/release-pending: the release staged (vX.Y.0), the rc
// it came from, and what the notification says.
type ReleasePendingRequest struct {
	Version string `json:"version"`
	RC      string `json:"rc,omitempty"`
	Body    string `json:"body,omitempty"`
}

// releaseVersion is a release a person is asked to approve.
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.0$`)

// maxNotificationBody bounds what the caller may say.
const maxNotificationBody = 4096

// releasePendingBody reads a ReleasePendingRequest off r and writes the
// POST /api/notifications body it stands for, or says why it is refused.
// Everything but the version, rc and body is fixed: the token raises
// this one family about its own repository, never another.
func releasePendingBody(r *http.Request, repo model.RepoRef) ([]byte, string) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Sprintf("reading the request: %v", err)
	}
	if len(data) > maxBody {
		return nil, "the request body is too large"
	}
	var req ReleasePendingRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Sprintf("reading the request: %v (it takes only version, rc and body)", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, "reading the request: trailing data after JSON object"
	}
	if !releaseVersion.MatchString(req.Version) {
		return nil, fmt.Sprintf("%q is not a release (vX.Y.0)", req.Version)
	}
	if len(req.Body) > maxNotificationBody {
		return nil, fmt.Sprintf("the body is longer than %d bytes", maxNotificationBody)
	}
	title := req.Version + " is ready to promote"
	if rc := strings.TrimSpace(req.RC); rc != "" {
		if !strings.HasPrefix(rc, req.Version+"-rc.") {
			return nil, fmt.Sprintf("%q is not a release candidate of %s", rc, req.Version)
		}
		title += " (" + rc + " passed its soak)"
	}
	body, err := json.Marshal(struct {
		Subject  string   `json:"subject"`
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Severity string   `json:"severity"`
		Repo     string   `json:"repo"`
		Actions  []string `json:"actions"`
	}{model.NotificationSubject(model.FamilyReleasePending, req.Version), title, req.Body,
		string(model.NotificationWarning), repo.String(), model.ReleasePendingActions})
	if err != nil {
		return nil, fmt.Sprintf("encoding the request: %v", err)
	}
	return body, ""
}

// newForward is a request for grain's API carrying nothing of the
// original but method, path, query and body: no headers, and no remote
// address, which is what the API reads as a request from this process.
func newForward(ctx context.Context, method, path string, query url.Values, body []byte) *http.Request {
	u := &url.URL{Path: path}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req := (&http.Request{
		Method: method, URL: u, RequestURI: u.RequestURI(), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Host: "localhost",
	}).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	} else {
		req.Body = http.NoBody
	}
	return req
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
