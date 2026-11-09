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

// Package grainclient provides a typed Go client wrapping pkg/hub/api and
// Grain's public HTTP, SSE integration-event, externalRef rollup, and
// committed-context APIs.
package grainclient

import (
	"errors"
	"time"

	registryapi "github.com/ai-on-gke/grain/pkg/hub/api"
)

// Re-exported types and constants from github.com/ai-on-gke/grain/pkg/hub/api
// so callers in grain-plc can work through a single client package.
type (
	Entry               = registryapi.Entry
	EntryKind           = registryapi.EntryKind
	EntryState          = registryapi.EntryState
	EntryStatus         = registryapi.EntryStatus
	Scope               = registryapi.Scope
	ScopeInput          = registryapi.ScopeInput
	DirectoryQuery      = registryapi.DirectoryQuery
	DirectoryEntry      = registryapi.DirectoryEntry
	DirectoryResponse   = registryapi.DirectoryResponse
	RouteQuery          = registryapi.RouteQuery
	RouteCandidate      = registryapi.RouteCandidate
	MatchedScope        = registryapi.MatchedScope
	RouteResponse       = registryapi.RouteResponse
	ListEntriesQuery    = registryapi.ListEntriesQuery
	CreateEntryRequest  = registryapi.CreateEntryRequest
	CreateEntryResponse = registryapi.CreateEntryResponse
	DeleteEntryRequest  = registryapi.DeleteEntryRequest
)

// Supported entry kinds in v1 (instance over VPC and connected over outbound tunnel).
const (
	EntryKindInstance  = registryapi.KindInstance
	EntryKindConnected = registryapi.KindConnected
)

// Client identity values passed via X-Grain-Client (recorded in task.FiledVia).
const (
	ClientPLCStudio      = "plc-studio"
	ClientPLCChatGateway = "plc-chat-gateway"
)

// Integration event type constants emitted by GET /api/events.
const (
	EventTaskState    = "task.state"
	EventTaskQuestion = "task.question"
	EventTaskSecret   = "task.secret"
	EventTaskAccess   = "task.access"
	EventTaskCI       = "task.ci"
)

// ErrUnsupportedEntryKind is returned when an entry has a kind not supported
// by the v1 routing rules (only instance and connected are routed in v1).
var ErrUnsupportedEntryKind = errors.New("grainclient: unsupported registry entry kind")

// ContextRef points at a committed file or directory in a Git repository
// ({repo, path, rev}) attached to a Grain task via context or designs.
type ContextRef struct {
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty"`
	Path string `json:"path" yaml:"path"`
	Rev  string `json:"rev,omitempty" yaml:"rev,omitempty"`
}

// ContextManifestFile describes a single file entry inside a context.yml manifest.
type ContextManifestFile struct {
	Path   string `json:"path" yaml:"path"`
	Role   string `json:"role,omitempty" yaml:"role,omitempty"`
	SHA256 string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
}

// ContextManifest is the parsed representation of a context.yml manifest inside
// a committed-context bundle directory.
type ContextManifest struct {
	Schema      int                   `json:"schema" yaml:"schema"`
	ExternalRef string                `json:"external_ref,omitempty" yaml:"external_ref,omitempty"`
	SpecRev     string                `json:"spec_rev,omitempty" yaml:"spec_rev,omitempty"`
	GeneratedBy string                `json:"generated_by,omitempty" yaml:"generated_by,omitempty"`
	GeneratedAt string                `json:"generated_at,omitempty" yaml:"generated_at,omitempty"`
	Files       []ContextManifestFile `json:"files,omitempty" yaml:"files,omitempty"`
}

// WriteTarget specifies an additional repository and base branch a task writes to.
type WriteTarget struct {
	Repo string `json:"repo" yaml:"repo"`
	Base string `json:"base,omitempty" yaml:"base,omitempty"`
}

// CreateTaskRequest is the payload for POST /api/tasks.
type CreateTaskRequest struct {
	ID              string        `json:"id,omitempty"`
	Title           string        `json:"title"`
	Body            string        `json:"body"`
	Repo            string        `json:"repo,omitempty"`
	Branch          string        `json:"branch,omitempty"`
	Reads           []string      `json:"reads,omitempty"`
	Writes          []WriteTarget `json:"writes,omitempty"`
	DependsOn       []string      `json:"dependsOn,omitempty"`
	Priority        int           `json:"priority,omitempty"`
	ExternalRef     string        `json:"externalRef,omitempty"`
	Designs         DesignRefs    `json:"designs,omitempty"`
	Context         []ContextRef  `json:"context,omitempty"`
	HoldForApproval bool          `json:"holdForApproval,omitempty"`
	FiledVia        string        `json:"-"`
}

// Task represents a Grain task returned by /api/tasks and /api/tasks/{id}.
type Task struct {
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Body            string           `json:"body,omitempty"`
	Description     string           `json:"description,omitempty"`
	State           string           `json:"state"`
	Repo            string           `json:"repo,omitempty"`
	Branch          string           `json:"branch,omitempty"`
	PRURL           string           `json:"prUrl,omitempty"`
	Priority        int              `json:"priority,omitempty"`
	ExternalRef     string           `json:"externalRef,omitempty"`
	Designs         DesignRefs       `json:"designs,omitempty"`
	Context         []ContextRef     `json:"context,omitempty"`
	ContextManifest *ContextManifest `json:"contextManifest,omitempty"`
	Reads           []string         `json:"reads,omitempty"`
	Writes          []WriteTarget    `json:"writes,omitempty"`
	DependsOn       []string         `json:"dependsOn,omitempty"`
	FiledBy         string           `json:"filedBy,omitempty"`
	FiledVia        string           `json:"filedVia,omitempty"`
	ApprovedBy      string           `json:"approvedBy,omitempty"`
	CreatedAt       time.Time        `json:"createdAt,omitempty"`
	UpdatedAt       time.Time        `json:"updatedAt,omitempty"`
}

// TaskPatch is the payload for PATCH /api/tasks/{id}.
type TaskPatch struct {
	Title       *string       `json:"title,omitempty"`
	Body        *string       `json:"body,omitempty"`
	State       *string       `json:"state,omitempty"`
	Priority    *int          `json:"priority,omitempty"`
	ExternalRef *string       `json:"externalRef,omitempty"`
	Designs     *DesignRefs   `json:"designs,omitempty"`
	Context     *[]ContextRef `json:"context,omitempty"`
}

// ListTasksFilter specifies optional query filters for GET /api/tasks.
type ListTasksFilter struct {
	ExternalRef string
	State       string
	Repo        string
}

// Comment represents a task conversation comment on a Grain task.
type Comment struct {
	ID        int64     `json:"id,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
	Author    string    `json:"author,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// RefRollupCounts holds aggregate task state counts for an externalRef.
type RefRollupCounts struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Blocked   int `json:"blocked"`
	Done      int `json:"done"`
	Merged    int `json:"merged"`
	Closed    int `json:"closed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// RefTaskSummary is the compact per-task summary inside RefSummary.
type RefTaskSummary struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	State           string    `json:"state"`
	Repo            string    `json:"repo,omitempty"`
	PRURL           string    `json:"prUrl,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
	CreatedAt       time.Time `json:"createdAt"`
	PendingQuestion string    `json:"pendingQuestion,omitempty"`
}

// RefSummary is the response payload from GET /api/refs/{ref}/summary.
type RefSummary struct {
	ExternalRef string           `json:"externalRef"`
	Counts      RefRollupCounts  `json:"counts"`
	Tasks       []RefTaskSummary `json:"tasks"`
	// Total, ByState, PullRequests and OpenQuestions are the fields grain's
	// GET /api/refs/{ref}/summary actually answers with (model.RefSummary);
	// UnmarshalJSON derives Counts from them when the daemon sends no
	// "counts" object (see wire.go).
	Total         int               `json:"total,omitempty"`
	ByState       map[string]int    `json:"byState,omitempty"`
	PullRequests  *RefPullRequests  `json:"pullRequests,omitempty"`
	OpenQuestions []RefOpenQuestion `json:"openQuestions,omitempty"`
}

// IntegrationEvent is a structured lifecycle event emitted by GET /api/events.
type IntegrationEvent struct {
	ID          int64     `json:"id"`
	Type        string    `json:"type"`
	Time        time.Time `json:"time"`
	TaskID      string    `json:"taskId"`
	ExternalRef string    `json:"externalRef,omitempty"`
	Repo        string    `json:"repo,omitempty"`
	State       string    `json:"state,omitempty"`
	PrevState   string    `json:"prevState,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	PRURL       string    `json:"prUrl,omitempty"`
	Question    string    `json:"question,omitempty"`
	Secret      string    `json:"secret,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Requests    any       `json:"requests,omitempty"`
	RunID       string    `json:"runId,omitempty"`
	Status      string    `json:"status,omitempty"`
	URL         string    `json:"url,omitempty"`
}

// EventsFilter specifies query parameters for GET /api/events (SSE or JSON poll).
type EventsFilter struct {
	Since       int64
	Types       []string
	ExternalRef string
	TaskID      string
	Limit       int
	Wait        time.Duration
}

// EventsPage is the JSON response returned by GET /api/events in poll mode.
type EventsPage struct {
	Events     []IntegrationEvent `json:"events"`
	NextCursor int64              `json:"nextCursor"`
}
