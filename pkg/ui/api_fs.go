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
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// GET /api/fs/<scheme>/<path> is grain's namespace (pkg/namespace) on
// the daemon's own API, for a workstation's `grain fs` and an external
// agent (design/spec/external-cli/external-cli.spec.md, #XC-24): the
// same namespace.Server the sandbox proxy serves at /fs/
// (pkg/gitproxy/namespace.go), so ?list, ?glob, ?grep, ?tail, ?follow,
// Range, redaction and the size caps answer alike on both. What differs
// is who is asking: there it is a run, held to its task's scope; here it
// is whoever agentAPIGate let in, held to the scope of the scheme's own
// route below and narrowed by fsScope to the repositories its grants
// reach.

// fsRoutes is each /api/fs/ route and the scheme it serves. One route per
// scheme is what gives each its own scope in route_scopes.go.
var fsRoutes = map[string]string{
	"GET /api/fs/ci/{path...}":     "ci",
	"GET /api/fs/task/{path...}":   "task",
	"GET /api/fs/deploy/{path...}": "deploy",
	"GET /api/fs/config/{path...}": "config",
	"GET /api/fs/memory/{path...}": "memory",
	"GET /api/fs/src/{path...}":    "src",
}

// SetNamespace sets where GET /api/fs/ finds the namespace it serves:
// the daemon builds it beside the sandbox proxy, after this server, so
// it is looked up per request. Call it before the server serves.
func (s *Server) SetNamespace(source func() *namespace.Server) {
	s.fsNamespace = source
}

func (s *Server) handleReadCINamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "ci")
}

func (s *Server) handleReadTaskNamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "task")
}

func (s *Server) handleReadDeployNamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "deploy")
}

func (s *Server) handleReadConfigNamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "config")
}

func (s *Server) handleReadMemoryNamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "memory")
}

func (s *Server) handleReadSrcNamespace(w http.ResponseWriter, r *http.Request) {
	s.serveFS(w, r, "src")
}

func (s *Server) serveFS(w http.ResponseWriter, r *http.Request, scheme string) {
	var ns *namespace.Server
	if s.fsNamespace != nil {
		ns = s.fsNamespace()
	}
	if ns == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("this server has no namespace to serve /api/fs/ from"))
		return
	}
	scope, err := s.fsScope(r.Context(), scheme)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	in := r.Clone(r.Context())
	in.URL.Path = "/fs/" + scheme + "/" + r.PathValue("path")
	in.URL.RawPath = ""
	ns.ServeHTTP(w, in, scope)
}

// fsScope is what the caller may read under scheme. A person -- every
// person is the deployment's owner (checkRepoPerson) -- and an
// in-process request read everything; an agent reads what the grants
// agentAPIGate authorized it under reach, for every repository they
// name and no other.
func (s *Server) fsScope(ctx context.Context, scheme string) (namespace.Scope, error) {
	call, ok := agentCallFromContext(ctx)
	if !ok {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
			return namespace.Scope{}, fmt.Errorf("%s may read the namespace only under an api grant (docs/agent-api.md)", p.ID)
		}
		return namespace.Scope{
			MayReadCI: true, MayReadAllCI: true, MayReadSrc: true, MayReadAllTasks: true,
			MayReadFullConfig: true, MayReadAllMemory: true, MayReadMetrics: true, MayReadPipeline: true,
			MayReadHealth: true, MayReadGrainLog: true, MayReadPlaybooks: true, MayReadIssues: true, MayReadDB: true,
		}, nil
	}
	set := call.grants
	scope := namespace.Scope{TaskIDStr: call.taskID}
	ci := func() {
		scope.MayReadCI = set.Has("ci:read")
		scope.MayReadAllCI = set.Allows("ci:read", "")
		scope.AllowedRepos = repoSet(set.Repos("ci:read"))
	}
	switch scheme {
	case "ci":
		ci()
	case "task":
		scope.MayReadAllTasks = set.Allows("tasks:read", "")
		scope.TaskReadRepos = repoSet(set.Repos("tasks:read"))
	case "deploy":
		if set.Allows("deployment:read", "") {
			scope.MayReadMetrics, scope.MayReadPipeline, scope.MayReadHealth = true, true, true
		} else {
			// In under ci:read, which reaches deploy://merge-queue
			// alone, and only its repositories' part of it.
			ci()
			scope.MergeQueueByCI = true
		}
	case "config":
		scope.MayReadFullConfig = set.Allows("settings:read", "")
	case "memory":
		scope.MayReadAllMemory = set.Allows("memory:read", "")
		// The repository the gate held a repository-limited grant to.
		scope.TaskRepo = call.repo
	case "src":
		scope.MayReadSrc, scope.MayReadPlaybooks = true, true
	}
	return scope, nil
}

func repoSet(repos []string) map[string]bool {
	if len(repos) == 0 {
		return nil
	}
	out := make(map[string]bool, len(repos))
	for _, r := range repos {
		out[r] = true
		out[strings.ToLower(r)] = true
	}
	return out
}

// fsCallRepo is the repository an /api/fs/ read names, for agentAPIGate
// to hold a repository-limited grant to: ci://<owner>/<name>/...,
// memory://repos/<owner>/<name>..., and task://<id>/... by its task's
// repository. A read naming none (a listing of ci://, say) is "", which
// the gate lets through to be narrowed by fsScope instead. ok is false
// for any other route.
func (s *Server) fsCallRepo(ctx context.Context, r *http.Request, pattern string) (repo string, ok bool, err error) {
	scheme, ok := fsRoutes[pattern]
	if !ok {
		return "", false, nil
	}
	segs := strings.Split(strings.Trim(r.PathValue("path"), "/"), "/")
	switch scheme {
	case "ci":
		if len(segs) >= 2 && segs[0] != "" && segs[1] != "" {
			repo, err = s.agentCanonicalRepo(segs[0] + "/" + segs[1])
		}
	case "memory":
		if len(segs) >= 3 && segs[0] == "repos" && segs[1] != "" && segs[2] != "" {
			repo, err = s.agentCanonicalRepo(segs[1] + "/" + strings.TrimSuffix(segs[2], ".md"))
		}
	case "task":
		if id := strings.TrimPrefix(segs[0], "#"); id != "" {
			if t, terr := s.tasks.Store.GetTask(ctx, id); terr == nil && t != nil {
				repo = taskRepo(t)
			}
		}
	}
	return repo, true, err
}

// fsMergeQueueByCI reports whether this is a read of deploy://merge-queue
// a caller holding ci:read but not deployment:read may make: the queue is
// CI's as much as the deployment's, and fsScope shows such a caller only
// its own repositories' part of it.
func fsMergeQueueByCI(set APIGrantSet, pattern string, r *http.Request) bool {
	if fsRoutes[pattern] != "deploy" || !set.Has("ci:read") {
		return false
	}
	switch strings.Trim(r.PathValue("path"), "/") {
	case "merge-queue", "merge-queue.md":
		return true
	}
	return false
}

// errExternalSrc refuses src:// to an external agent token that has not
// opted into it: grain's own source is the deployment's, and
// deployment:read is too ordinary a scope to carry it by itself.
func errExternalSrc(principal string) error {
	return fmt.Errorf("%s may not read src://: an external agent token reads grain's source only when it is "+
		"granted deployment:src explicitly, beside deployment:read", principal)
}
