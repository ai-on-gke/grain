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

// Package playbook implements PLC playbook parsing, committed-context folder
// rule validation (.grain/context/<source>/<slug> and context.yml schema 1),
// and multi-step Grain task execution planning across target and -read
// context repositories.
package playbook

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"gopkg.in/yaml.v3"
)

// PlacementMode controls where a committed-context bundle is stored.
type PlacementMode string

const (
	// PlacementTarget commits the context bundle directly into the target code repository.
	PlacementTarget PlacementMode = "target"
	// PlacementRead commits the context bundle into a companion -read or shared context repository.
	PlacementRead PlacementMode = "read"
)

// ContextRootPrefix is the canonical directory prefix for committed context bundles.
const ContextRootPrefix = ".grain/context/"

// Validation errors for committed-context paths, revisions, and manifests.
var (
	ErrInvalidContextPath     = errors.New("playbook: invalid committed-context path")
	ErrInvalidCommitSHA       = errors.New("playbook: invalid git commit SHA")
	ErrInvalidContextManifest = errors.New("playbook: invalid context.yml manifest")
)

// Playbook defines a reusable multi-phase PLC workflow that expands into Grain tasks.
type Playbook struct {
	ID               string        `json:"id" yaml:"id"`
	Title            string        `json:"title" yaml:"title"`
	Description      string        `json:"description,omitempty" yaml:"description,omitempty"`
	DefaultPlacement PlacementMode `json:"default_placement,omitempty" yaml:"default_placement,omitempty"`
	Phases           []Phase       `json:"phases" yaml:"phases"`
}

// Phase groups related steps within a Playbook.
type Phase struct {
	ID    string `json:"id" yaml:"id"`
	Title string `json:"title" yaml:"title"`
	Steps []Step `json:"steps" yaml:"steps"`
}

// Step is a single task template inside a Phase.
type Step struct {
	ID              string   `json:"id" yaml:"id"`
	Title           string   `json:"title" yaml:"title"`
	Body            string   `json:"body" yaml:"body"`
	Repo            string   `json:"repo,omitempty" yaml:"repo,omitempty"`
	Reads           []string `json:"reads,omitempty" yaml:"reads,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	Priority        int      `json:"priority,omitempty" yaml:"priority,omitempty"`
	HoldForApproval bool     `json:"hold_for_approval,omitempty" yaml:"hold_for_approval,omitempty"`
}

// PlanParams supplies the concrete project, repository, and committed-context
// parameters when instantiating a Playbook into Grain tasks.
type PlanParams struct {
	ProjectSlug string                   `json:"project_slug"`
	ExternalRef string                   `json:"external_ref"`
	TargetRepo  string                   `json:"target_repo"`
	Placement   PlacementMode            `json:"placement,omitempty"`
	ContextRepo string                   `json:"context_repo,omitempty"`
	ContextPath string                   `json:"context_path,omitempty"`
	ContextRev  string                   `json:"context_rev"`
	DesignRefs  []grainclient.ContextRef `json:"design_refs,omitempty"`
	FiledVia    string                   `json:"filed_via,omitempty"`
}

// PlannedTask pairs a playbook step identifier with the ready-to-file CreateTaskRequest.
type PlannedTask struct {
	StepID  string                        `json:"step_id"`
	PhaseID string                        `json:"phase_id"`
	Request grainclient.CreateTaskRequest `json:"request"`
}

// BuildContextPath returns the canonical committed-context directory path
// .grain/context/<source>/<slug>.
func BuildContextPath(source, slug string) (string, error) {
	source = strings.TrimSpace(source)
	slug = strings.TrimSpace(slug)
	if source == "" || slug == "" {
		return "", fmt.Errorf("%w: source and slug must be non-empty", ErrInvalidContextPath)
	}
	p := ContextRootPrefix + source + "/" + slug
	if err := ValidateContextPath(p); err != nil {
		return "", err
	}
	return p, nil
}

// ValidateContextPath enforces the committed-context folder rule:
// paths must be clean relative paths under .grain/context/<source>/<slug>
// with no parent traversal (".."), leading slash, or backslashes.
func ValidateContextPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidContextPath)
	}
	if strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: %q must be a relative slash-separated path", ErrInvalidContextPath, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q contains invalid path segment %q", ErrInvalidContextPath, p, seg)
		}
	}
	cleaned := path.Clean(p)
	if cleaned != p {
		return fmt.Errorf("%w: %q is not clean (want %q)", ErrInvalidContextPath, p, cleaned)
	}
	if !strings.HasPrefix(cleaned, ContextRootPrefix) {
		return fmt.Errorf("%w: %q must live under %s<source>/<slug>", ErrInvalidContextPath, p, ContextRootPrefix)
	}
	rest := strings.TrimPrefix(cleaned, ContextRootPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("%w: %q must include both <source> and <slug> under %s", ErrInvalidContextPath, p, ContextRootPrefix)
	}
	return nil
}

// ValidateCommitSHA verifies that rev is a 40-character lowercase hexadecimal Git commit SHA.
func ValidateCommitSHA(rev string) error {
	if len(rev) != 40 {
		return fmt.Errorf("%w: %q must be a 40-char hex commit SHA", ErrInvalidCommitSHA, rev)
	}
	for i := 0; i < len(rev); i++ {
		ch := rev[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return fmt.Errorf("%w: %q contains non-lowercase-hex character %q", ErrInvalidCommitSHA, rev, ch)
		}
	}
	return nil
}

// ParseContextRef parses a committed-context shorthand string:
//   - "owner/repo@rev:path"
//   - "owner/repo:path"
//   - "@rev:path"
//   - "path"
func ParseContextRef(raw string) (grainclient.ContextRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return grainclient.ContextRef{}, fmt.Errorf("%w: empty context reference", ErrInvalidContextPath)
	}
	if strings.HasPrefix(s, "@") {
		revPart, pathPart, ok := strings.Cut(s[1:], ":")
		if !ok || revPart == "" || pathPart == "" {
			return grainclient.ContextRef{}, fmt.Errorf("%w: invalid @rev:path shorthand %q", ErrInvalidContextPath, raw)
		}
		return grainclient.ContextRef{Rev: revPart, Path: pathPart}, nil
	}
	if prefix, pathPart, ok := strings.Cut(s, ":"); ok {
		if repoPart, revPart, hasAt := strings.Cut(prefix, "@"); hasAt {
			if repoPart == "" || revPart == "" || pathPart == "" {
				return grainclient.ContextRef{}, fmt.Errorf("%w: invalid repo@rev:path shorthand %q", ErrInvalidContextPath, raw)
			}
			return grainclient.ContextRef{Repo: repoPart, Rev: revPart, Path: pathPart}, nil
		}
		if strings.Contains(prefix, "/") && pathPart != "" {
			return grainclient.ContextRef{Repo: prefix, Path: pathPart}, nil
		}
	}
	return grainclient.ContextRef{Path: s}, nil
}

// FormatContextRef formats a ContextRef into canonical shorthand ("repo@rev:path", "@rev:path", "repo:path", or "path").
func FormatContextRef(ref grainclient.ContextRef) string {
	switch {
	case ref.Repo != "" && ref.Rev != "":
		return ref.Repo + "@" + ref.Rev + ":" + ref.Path
	case ref.Repo != "":
		return ref.Repo + ":" + ref.Path
	case ref.Rev != "":
		return "@" + ref.Rev + ":" + ref.Path
	default:
		return ref.Path
	}
}

// ParseContextManifest parses and validates a context.yml manifest document.
func ParseContextManifest(data []byte) (*grainclient.ContextManifest, error) {
	var m grainclient.ContextManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidContextManifest, err)
	}
	if err := ValidateContextManifest(m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ValidateContextManifest checks that a ContextManifest uses schema 1 and valid relative file paths.
func ValidateContextManifest(m grainclient.ContextManifest) error {
	if m.Schema != 1 {
		return fmt.Errorf("%w: unsupported schema %d (want 1)", ErrInvalidContextManifest, m.Schema)
	}
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		p := strings.TrimSpace(f.Path)
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
			return fmt.Errorf("%w: invalid file path %q", ErrInvalidContextManifest, f.Path)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("%w: file path %q contains invalid segment %q", ErrInvalidContextManifest, f.Path, seg)
			}
		}
		if seen[p] {
			return fmt.Errorf("%w: duplicate file path %q", ErrInvalidContextManifest, p)
		}
		seen[p] = true
	}
	return nil
}

// MarshalContextManifest serializes a ContextManifest to YAML after validating it.
func MarshalContextManifest(m grainclient.ContextManifest) ([]byte, error) {
	if err := ValidateContextManifest(m); err != nil {
		return nil, err
	}
	return yaml.Marshal(m)
}

// ParsePlaybook parses a YAML or JSON Playbook definition and validates its structure.
func ParsePlaybook(data []byte) (*Playbook, error) {
	var pb Playbook
	if err := yaml.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("playbook: decode YAML: %w", err)
	}
	if err := ValidatePlaybook(pb); err != nil {
		return nil, err
	}
	return &pb, nil
}

// ValidatePlaybook checks that a Playbook has non-empty IDs, phases, steps, and valid step dependencies.
func ValidatePlaybook(pb Playbook) error {
	if strings.TrimSpace(pb.ID) == "" {
		return errors.New("playbook: id is required")
	}
	if strings.TrimSpace(pb.Title) == "" {
		return errors.New("playbook: title is required")
	}
	if pb.DefaultPlacement != "" && pb.DefaultPlacement != PlacementTarget && pb.DefaultPlacement != PlacementRead {
		return fmt.Errorf("playbook: invalid default_placement %q", pb.DefaultPlacement)
	}
	if len(pb.Phases) == 0 {
		return errors.New("playbook: at least one phase is required")
	}
	stepIDs := make(map[string]bool)
	for _, ph := range pb.Phases {
		if strings.TrimSpace(ph.ID) == "" {
			return errors.New("playbook: phase id is required")
		}
		if len(ph.Steps) == 0 {
			return fmt.Errorf("playbook: phase %q must contain at least one step", ph.ID)
		}
		for _, st := range ph.Steps {
			if strings.TrimSpace(st.ID) == "" {
				return fmt.Errorf("playbook: step id is required in phase %q", ph.ID)
			}
			if strings.TrimSpace(st.Title) == "" {
				return fmt.Errorf("playbook: step %q title is required", st.ID)
			}
			if stepIDs[st.ID] {
				return fmt.Errorf("playbook: duplicate step id %q", st.ID)
			}
			stepIDs[st.ID] = true
		}
	}
	for _, ph := range pb.Phases {
		for _, st := range ph.Steps {
			for _, dep := range st.DependsOn {
				if !stepIDs[dep] {
					return fmt.Errorf("playbook: step %q depends on unknown step %q", st.ID, dep)
				}
				if dep == st.ID {
					return fmt.Errorf("playbook: step %q cannot depend on itself", st.ID)
				}
			}
		}
	}
	return nil
}

// Plan expands pb with params into an ordered slice of PlannedTask requests,
// enforcing the committed-context folder rule and placement mode (target repo
// vs companion -read repository).
func Plan(pb Playbook, params PlanParams) ([]PlannedTask, error) {
	if err := ValidatePlaybook(pb); err != nil {
		return nil, err
	}
	targetRepo := strings.TrimSpace(params.TargetRepo)
	if targetRepo == "" || !strings.Contains(targetRepo, "/") {
		return nil, errors.New("playbook: target_repo (owner/name) is required")
	}
	extRef := strings.TrimSpace(params.ExternalRef)
	if extRef == "" {
		return nil, errors.New("playbook: external_ref is required")
	}
	if err := ValidateCommitSHA(params.ContextRev); err != nil {
		return nil, err
	}

	ctxPath := strings.TrimSpace(params.ContextPath)
	if ctxPath == "" {
		slug := strings.TrimSpace(params.ProjectSlug)
		if slug == "" {
			slug = pb.ID
		}
		var err error
		ctxPath, err = BuildContextPath("plc", slug)
		if err != nil {
			return nil, err
		}
	} else if err := ValidateContextPath(ctxPath); err != nil {
		return nil, err
	}

	placement := params.Placement
	if placement == "" {
		placement = pb.DefaultPlacement
	}
	if placement == "" {
		placement = PlacementTarget
	}

	var ctxRef grainclient.ContextRef
	var extraReadRepo string
	switch placement {
	case PlacementTarget:
		ctxRef = grainclient.ContextRef{
			Path: ctxPath,
			Rev:  params.ContextRev,
		}
	case PlacementRead:
		ctxRepo := strings.TrimSpace(params.ContextRepo)
		if ctxRepo == "" {
			ctxRepo = targetRepo + "-read"
		}
		ctxRef = grainclient.ContextRef{
			Repo: ctxRepo,
			Path: ctxPath,
			Rev:  params.ContextRev,
		}
		if ctxRepo != targetRepo {
			extraReadRepo = ctxRepo
		}
	default:
		return nil, fmt.Errorf("playbook: unsupported placement mode %q", placement)
	}

	filedVia := strings.TrimSpace(params.FiledVia)
	if filedVia == "" {
		filedVia = grainclient.ClientPLCStudio
	}

	var planned []PlannedTask
	for _, ph := range pb.Phases {
		for _, st := range ph.Steps {
			stepRepo := strings.TrimSpace(st.Repo)
			if stepRepo == "" {
				stepRepo = targetRepo
			}

			reads := append([]string(nil), st.Reads...)
			if extraReadRepo != "" && !containsString(reads, extraReadRepo) {
				reads = append(reads, extraReadRepo)
			}

			stepCtx := ctxRef
			if placement == PlacementTarget && stepRepo != targetRepo {
				stepCtx.Repo = targetRepo
				if !containsString(reads, targetRepo) {
					reads = append(reads, targetRepo)
				}
			}

			title := strings.TrimSpace(st.Title)
			if !strings.HasPrefix(title, "[PLC]") {
				title = "[PLC] " + title
			}

			deps := append([]string(nil), st.DependsOn...)
			designs := append([]grainclient.ContextRef(nil), params.DesignRefs...)

			planned = append(planned, PlannedTask{
				StepID:  st.ID,
				PhaseID: ph.ID,
				Request: grainclient.CreateTaskRequest{
					Title:           title,
					Body:            st.Body,
					Repo:            stepRepo,
					Reads:           reads,
					DependsOn:       deps,
					Priority:        st.Priority,
					ExternalRef:     extRef,
					Designs:         designs,
					Context:         []grainclient.ContextRef{stepCtx},
					HoldForApproval: st.HoldForApproval,
					FiledVia:        filedVia,
				},
			})
		}
	}
	return planned, nil
}

func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// DefaultPlaybooks returns the built-in starter playbooks bundled with PLC Studio.
func DefaultPlaybooks() []Playbook {
	return []Playbook{
		{
			ID:               "plc-feature-launch",
			Title:            "Feature Launch Playbook",
			Description:      "End-to-end PLC workflow from committed PRD/design context to implementation and verification.",
			DefaultPlacement: PlacementTarget,
			Phases: []Phase{
				{
					ID:    "foundation",
					Title: "Foundation & API Contract",
					Steps: []Step{
						{
							ID:       "scaffold-api",
							Title:    "Scaffold core API types and schema",
							Body:     "Implement the API types and validation rules specified in the committed context bundle.",
							Priority: 5,
						},
					},
				},
				{
					ID:    "delivery",
					Title: "Implementation & Integration Tests",
					Steps: []Step{
						{
							ID:        "implement-service",
							Title:     "Implement service handlers and integration tests",
							Body:      "Build the handlers, wire them into the server, and verify all integration tests pass.",
							DependsOn: []string{"scaffold-api"},
							Priority:  5,
						},
					},
				},
			},
		},
		{
			ID:               "plc-cross-repo-rollout",
			Title:            "Cross-Repo Context Rollout",
			Description:      "Files tasks referencing a shared -read context repository.",
			DefaultPlacement: PlacementRead,
			Phases: []Phase{
				{
					ID:    "rollout",
					Title: "Rollout",
					Steps: []Step{
						{
							ID:       "apply-spec",
							Title:    "Apply cross-repo specification from context bundle",
							Body:     "Read the committed context bundle from the companion -read repository and implement the changes.",
							Priority: 5,
						},
					},
				},
			},
		},
	}
}
