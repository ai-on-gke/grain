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

// Package studio implements the PLC Studio HTTP server and API handlers backed
// by grainclient, playbook, plcseams, perf, and the embedded web/studio UI.
package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/playbook"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
	studioassets "github.com/ai-on-gke/grain/grain-plc/web/studio"
)

// Config configures a PLC Studio Server.
type Config struct {
	Client      *grainclient.Client
	Playbooks   []playbook.Playbook
	Perf        *perf.Recorder
	RequireAuth bool
}

// CreateStudioTaskRequest wraps CreateTaskRequest with an optional TargetInstance override.
type CreateStudioTaskRequest struct {
	TargetInstance string `json:"target_instance,omitempty"`
	grainclient.CreateTaskRequest
}

// LaunchPlaybookRequest is the payload for POST /api/studio/playbooks/{id}/launch.
type LaunchPlaybookRequest struct {
	TargetInstance string `json:"target_instance,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
	playbook.PlanParams
}

// LaunchPlaybookResponse is returned by POST /api/studio/playbooks/{id}/launch.
type LaunchPlaybookResponse struct {
	PlaybookID string                 `json:"playbook_id"`
	Instance   string                 `json:"instance,omitempty"`
	Planned    []playbook.PlannedTask `json:"planned"`
	Created    []grainclient.Task     `json:"created,omitempty"`
}

// Server serves the PLC Studio web UI and JSON API endpoints.
type Server struct {
	cfg       Config
	client    *grainclient.Client
	perf      *perf.Recorder
	staticSrv http.Handler

	mu        sync.RWMutex
	playbooks map[string]playbook.Playbook
	order     []string
}

// NewServer constructs a PLC Studio Server from cfg.
func NewServer(cfg Config) *Server {
	recorder := cfg.Perf
	if recorder == nil {
		recorder = perf.Default()
	}
	client := cfg.Client
	if client == nil {
		client = grainclient.New(grainclient.Config{
			ClientName: grainclient.ClientPLCStudio,
			Perf:       recorder,
		})
	}
	pbs := cfg.Playbooks
	if len(pbs) == 0 {
		pbs = playbook.DefaultPlaybooks()
	}
	pbMap := make(map[string]playbook.Playbook, len(pbs))
	order := make([]string, 0, len(pbs))
	for _, pb := range pbs {
		pbMap[pb.ID] = pb
		order = append(order, pb.ID)
	}

	return &Server{
		cfg:       cfg,
		client:    client,
		perf:      recorder,
		staticSrv: http.FileServer(http.FS(fs.FS(studioassets.FS))),
		playbooks: pbMap,
		order:     order,
	}
}

// RegisterPlaybook adds or replaces a Playbook in the Studio catalog.
func (s *Server) RegisterPlaybook(pb playbook.Playbook) error {
	if err := playbook.ValidatePlaybook(pb); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.playbooks[pb.ID]; !exists {
		s.order = append(s.order, pb.ID)
	}
	s.playbooks[pb.ID] = pb
	return nil
}

// Handler returns the root http.Handler for PLC Studio.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/api/studio/perf", s.perf.Handler())
	mux.Handle("/api/studio/directory", s.withAuth("studio.directory", http.HandlerFunc(s.handleDirectory)))
	mux.Handle("/api/studio/route", s.withAuth("studio.route", http.HandlerFunc(s.handleRoute)))
	mux.Handle("/api/studio/tasks", s.withAuth("studio.tasks", http.HandlerFunc(s.handleTasks)))
	mux.Handle("/api/studio/playbooks", s.withAuth("studio.playbooks", http.HandlerFunc(s.handlePlaybooks)))
	mux.Handle("/api/studio/playbooks/", s.withAuth("studio.playbook_action", http.HandlerFunc(s.handlePlaybookAction)))
	mux.Handle("/api/studio/refs/", s.withAuth("studio.ref_summary", http.HandlerFunc(s.handleRefSummary)))
	mux.Handle("/", s.perf.Middleware("studio.static", http.HandlerFunc(s.handleStatic)))
	return mux
}

func (s *Server) withAuth(op string, next http.Handler) http.Handler {
	return s.perf.Middleware(op, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := plcseams.CurrentIdentityVerifier().VerifyRequest(r.Context(), r)
		if err != nil {
			if s.cfg.RequireAuth {
				writeError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
				return
			}
		} else if p != nil {
			r = r.WithContext(plcseams.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "plc-studio",
	})
}

func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	dir, err := s.client.Directory(r.Context())
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dir)
}

func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "repo query parameter is required")
		return
	}
	resp, err := s.client.Route(r.Context(), repo)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRefSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/api/studio/refs/")
	rawRef, ok := strings.CutSuffix(escaped, "/summary")
	if !ok || rawRef == "" {
		writeError(w, http.StatusNotFound, "not_found", "expected /api/studio/refs/{ref}/summary")
		return
	}
	ref, err := url.PathUnescape(rawRef)
	if err != nil || strings.TrimSpace(ref) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid externalRef")
		return
	}

	instName := r.URL.Query().Get("instance")
	repo := r.URL.Query().Get("repo")
	inst, _, err := s.client.ResolveInstance(r.Context(), instName, repo)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	summary, err := inst.RefSummary(r.Context(), ref)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		inst, _, err := s.client.ResolveInstance(r.Context(), q.Get("instance"), q.Get("repo"))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		tasks, err := inst.ListTasks(r.Context(), grainclient.ListTasksFilter{
			ExternalRef: q.Get("externalRef"),
			State:       q.Get("state"),
			Repo:        q.Get("repo"),
		})
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})

	case http.MethodPost:
		var req CreateStudioTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
		for _, c := range req.Context {
			if err := playbook.ValidateContextPath(c.Path); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_context_path", err.Error())
				return
			}
			if c.Rev != "" {
				if err := playbook.ValidateCommitSHA(c.Rev); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_context_rev", err.Error())
					return
				}
			}
		}
		req.FiledVia = grainclient.ClientPLCStudio
		inst, _, err := s.client.ResolveInstance(r.Context(), req.TargetInstance, req.Repo)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		created, err := inst.CreateTask(r.Context(), req.CreateTaskRequest)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handlePlaybooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.mu.RLock()
	list := make([]playbook.Playbook, 0, len(s.order))
	for _, id := range s.order {
		list = append(list, s.playbooks[id])
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"playbooks": list})
}

func (s *Server) handlePlaybookAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/studio/playbooks/")
	pbID, ok := strings.CutSuffix(trimmed, "/launch")
	if !ok || pbID == "" {
		writeError(w, http.StatusNotFound, "not_found", "expected /api/studio/playbooks/{id}/launch")
		return
	}

	pb, err := s.resolvePlaybook(r.Context(), pbID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	var req LaunchPlaybookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	req.PlanParams.FiledVia = grainclient.ClientPLCStudio
	planned, err := playbook.Plan(*pb, req.PlanParams)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plan", err.Error())
		return
	}

	if req.DryRun {
		writeJSON(w, http.StatusOK, LaunchPlaybookResponse{
			PlaybookID: pb.ID,
			Planned:    planned,
		})
		return
	}

	inst, entry, err := s.client.ResolveInstance(r.Context(), req.TargetInstance, req.TargetRepo)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	stepToTaskID := make(map[string]string, len(planned))
	created := make([]grainclient.Task, 0, len(planned))
	for i := range planned {
		taskReq := planned[i].Request
		if len(taskReq.DependsOn) > 0 {
			mappedDeps := make([]string, 0, len(taskReq.DependsOn))
			for _, depStep := range taskReq.DependsOn {
				if realID, ok := stepToTaskID[depStep]; ok {
					mappedDeps = append(mappedDeps, realID)
				} else {
					mappedDeps = append(mappedDeps, depStep)
				}
			}
			taskReq.DependsOn = mappedDeps
		}
		t, err := inst.CreateTask(r.Context(), taskReq)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		stepToTaskID[planned[i].StepID] = t.ID
		created = append(created, *t)
	}

	var instName string
	if entry != nil {
		instName = entry.Name
	}
	writeJSON(w, http.StatusCreated, LaunchPlaybookResponse{
		PlaybookID: pb.ID,
		Instance:   instName,
		Planned:    planned,
		Created:    created,
	})
}

func (s *Server) resolvePlaybook(ctx context.Context, id string) (*playbook.Playbook, error) {
	s.mu.RLock()
	pb, ok := s.playbooks[id]
	s.mu.RUnlock()
	if ok {
		return &pb, nil
	}
	raw, err := plcseams.CurrentSpecFetcher().FetchSpec(ctx, id)
	if err != nil {
		return nil, err
	}
	return playbook.ParsePlaybook(raw)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		b, err := studioassets.FS.ReadFile("index.html")
		if err != nil {
			http.Error(w, "missing index.html", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	s.staticSrv.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": msg,
		},
	})
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var apiErr *grainclient.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.Code
		if code == "" {
			code = "upstream_error"
		}
		writeError(w, apiErr.StatusCode, code, apiErr.Message)
		return
	}
	if errors.Is(err, grainclient.ErrUnsupportedEntryKind) {
		writeError(w, http.StatusBadRequest, "unsupported_entry_kind", err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
}
