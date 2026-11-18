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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

func (s *Server) lookupFlowBundle(ctx context.Context, ref string) (qa.FlowVersionBundle, *time.Time, string, *time.Time, bool, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return qa.FlowVersionBundle{}, nil, "", nil, false, nil
	}
	if b, ok := qa.LookupBuiltInOrRegisteredVersion(trimmed); ok {
		return b, nil, "system", nil, true, nil
	}
	var (
		fv  model.QAFlowVersion
		err error
	)
	if before, after, hasVer := strings.Cut(trimmed, "@"); hasVer {
		if vNum, pErr := strconv.Atoi(after); pErr == nil && vNum > 0 {
			fv, err = s.tasks.Store.GetQAFlowVersionByKeyVersion(ctx, before, vNum)
		} else {
			err = model.ErrNotFound
		}
	} else {
		fv, err = s.tasks.Store.GetQAFlowVersion(ctx, trimmed)
		if errors.Is(err, model.ErrNotFound) {
			list, lErr := s.tasks.Store.ListQAFlowVersions(ctx, trimmed, 1)
			if lErr == nil && len(list) > 0 {
				fv = list[0]
				err = nil
			}
		}
	}
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return qa.FlowVersionBundle{}, nil, "", nil, false, nil
		}
		return qa.FlowVersionBundle{}, nil, "", nil, false, err
	}
	return s.hydrateStoredFlowBundle(ctx, fv)
}

func (s *Server) hydrateStoredFlowBundle(ctx context.Context, fv model.QAFlowVersion) (qa.FlowVersionBundle, *time.Time, string, *time.Time, bool, error) {
	created := fv.CreatedAt
	if strings.TrimSpace(fv.SourceYAML) != "" {
		resolvedBundle, rErr := qa.ResolveAnyFlowDeclaration([]byte(fv.SourceYAML), fv.Version, func(p string) (qa.FlowVersionBundle, bool) {
			if p == fv.Hash || p == fmt.Sprintf("%s@%d", fv.Key, fv.Version) {
				return qa.FlowVersionBundle{}, false
			}
			b, _, _, _, ok, _ := s.lookupFlowBundle(ctx, p)
			return b, ok
		})
		if rErr == nil {
			resolvedBundle.Hash = fv.Hash
			resolvedBundle.Key = fv.Key
			resolvedBundle.Version = fv.Version
			if fv.ParentHash != "" {
				resolvedBundle.ParentHash = fv.ParentHash
			}
			if fv.Label != "" {
				resolvedBundle.Label = fv.Label
			}
			if fv.Summary != "" {
				resolvedBundle.Summary = fv.Summary
			}
			resolvedBundle.BuiltIn = false
			resolvedBundle.QAEngineRev = fv.QAEngineRev
			resolvedBundle.SourceYAML = fv.SourceYAML
			resolvedBundle.SpecJSON = fv.SpecJSON
			if fv.SchemaVer != "" {
				resolvedBundle.SchemaVer = fv.SchemaVer
			}
			return resolvedBundle, &created, fv.CreatedBy, fv.RetiredAt, true, nil
		}
	}
	var sp qa.FlowSpec
	_ = json.Unmarshal([]byte(fv.SpecJSON), &sp)
	var declV2 qa.FlowDeclarationV2
	var declV3 *qa.FlowDeclarationV3
	var declV4 *qa.FlowDeclarationV4
	if fv.SourceYAML != "" {
		if parsed, pErr := qa.ParseFlowDeclaration([]byte(fv.SourceYAML)); pErr == nil {
			switch d := parsed.(type) {
			case *qa.FlowDeclarationV4:
				declV4 = d
			case *qa.FlowDeclarationV3:
				declV3 = d
			case *qa.FlowDeclarationV2:
				declV2 = *d
			}
		}
	}
	return qa.FlowVersionBundle{
		Hash:          fv.Hash,
		Key:           fv.Key,
		Version:       fv.Version,
		ParentHash:    fv.ParentHash,
		Label:         fv.Label,
		Summary:       fv.Summary,
		BuiltIn:       false,
		QAEngineRev:   fv.QAEngineRev,
		SourceYAML:    fv.SourceYAML,
		Declaration:   declV2,
		DeclarationV3: declV3,
		DeclarationV4: declV4,
		SpecJSON:      fv.SpecJSON,
		Resolved:      sp,
		SchemaVer:     fv.SchemaVer,
	}, &created, fv.CreatedBy, fv.RetiredAt, true, nil
}

func formatFlowBundleMap(b qa.FlowVersionBundle, createdAt *time.Time, createdBy string, retiredAt *time.Time) map[string]any {
	var createdStr any
	if createdAt != nil {
		createdStr = createdAt.UTC().Format(time.RFC3339)
	}
	var retiredStr any
	if retiredAt != nil {
		retiredStr = retiredAt.UTC().Format(time.RFC3339)
	}
	m := map[string]any{
		"hash":        b.Hash,
		"key":         b.Key,
		"version":     b.Version,
		"parentHash":  b.ParentHash,
		"label":       b.Label,
		"summary":     b.Summary,
		"sourceYaml":  b.SourceYAML,
		"builtIn":     b.BuiltIn,
		"qaEngineRev": b.QAEngineRev,
		"schemaVer":   b.SchemaVer,
		"createdBy":   createdBy,
		"createdAt":   createdStr,
		"retiredAt":   retiredStr,
		"declaration": b.Declaration,
		"resolved":    b.Resolved,
		"spec":        b.Resolved,
		"stagesView":  qa.BuildWorkflowStagesView(b),
	}
	if b.DeclarationV3 != nil {
		m["declarationV3"] = b.DeclarationV3
	}
	if b.DeclarationV4 != nil {
		m["declarationV4"] = b.DeclarationV4
	}
	return m
}

func (s *Server) handleGetQAFlowVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.URL.Query().Get("key"))

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = min(v, 200)
		}
	}

	dbVersions, err := s.tasks.Store.ListQAFlowVersions(ctx, key, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]map[string]any, 0, len(dbVersions)+4)
	for _, v := range qa.BuiltInFlowVersions() {
		if key != "" && v.Key != key {
			continue
		}
		out = append(out, formatFlowBundleMap(v, nil, "system", nil))
	}
	for _, fv := range dbVersions {
		b, created, createdBy, retiredAt, _, _ := s.hydrateStoredFlowBundle(ctx, fv)
		item := formatFlowBundleMap(b, created, createdBy, retiredAt)
		if fv.SourceKind != "" {
			item["sourceKind"] = fv.SourceKind
		}
		if fv.SourceRepo != "" {
			item["sourceRepo"] = fv.SourceRepo
		}
		if fv.SourcePath != "" {
			item["sourcePath"] = fv.SourcePath
		}
		if fv.SourceCommit != "" {
			item["sourceCommit"] = fv.SourceCommit
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": out,
	})
}

func (s *Server) handleGetQAFlowVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := r.PathValue("hash")

	b, createdAt, createdBy, retiredAt, ok, err := s.lookupFlowBundle(ctx, ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, model.ErrNotFound)
		return
	}

	lineage := []map[string]any{}
	seen := map[string]bool{b.Hash: true}
	curr := b.ParentHash
	for curr != "" && !seen[curr] {
		seen[curr] = true
		pb, pCreated, pBy, pRetired, pOK, pErr := s.lookupFlowBundle(ctx, curr)
		if pErr != nil || !pOK {
			break
		}
		lineage = append(lineage, formatFlowBundleMap(pb, pCreated, pBy, pRetired))
		curr = pb.ParentHash
	}

	resp := formatFlowBundleMap(b, createdAt, createdBy, retiredAt)
	resp["lineage"] = lineage
	writeJSON(w, http.StatusOK, resp)
}

type postFlowVersionReq struct {
	YAML        string          `json:"yaml"`
	Declaration json.RawMessage `json:"declaration"`
	Action      string          `json:"action"`
	Hash        string          `json:"hash"`
	SampleLimit int             `json:"sampleLimit"`
}

func hasRawDeclaration(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func (s *Server) simulateDryRun(ctx context.Context, resolved qa.FlowSpec, hash string, sampleLimit int) (map[string]any, error) {
	if sampleLimit <= 0 {
		sampleLimit = 50
	}
	enabledRules := map[string]string{}
	for _, r := range resolved.Rules {
		if r.Enabled {
			sev := r.Severity
			if sev == "" {
				sev = "blocking"
			}
			enabledRules[r.ID] = sev
		}
	}

	runs, err := s.tasks.Store.ListAllQACheckRuns(ctx)
	if err != nil {
		return nil, err
	}
	findings, err := s.tasks.Store.ListAllQAFindings(ctx)
	if err != nil {
		return nil, err
	}

	// Sample up to sampleLimit distinct tasks with recorded check runs (most recent first).
	seenTasks := map[string]bool{}
	for i := len(runs) - 1; i >= 0 && len(seenTasks) < sampleLimit; i-- {
		if runs[i].TaskID != "" {
			seenTasks[runs[i].TaskID] = true
		}
	}

	type ruleAgg struct {
		RuleID          string           `json:"ruleId"`
		WouldBlockTasks map[string]bool  `json:"-"`
		WarnCount       int              `json:"warnCount"`
		Findings        []map[string]any `json:"findings,omitempty"`
	}
	byRule := map[string]*ruleAgg{}
	blockedTasks := map[string]bool{}

	for _, f := range findings {
		if !seenTasks[f.TaskID] {
			continue
		}
		sev, ok := enabledRules[f.RuleID]
		if !ok || sev == "shadow" {
			continue
		}
		agg := byRule[f.RuleID]
		if agg == nil {
			agg = &ruleAgg{RuleID: f.RuleID, WouldBlockTasks: map[string]bool{}}
			byRule[f.RuleID] = agg
		}
		if sev == "blocking" {
			agg.WouldBlockTasks[f.TaskID] = true
			blockedTasks[f.TaskID] = true
		} else {
			agg.WarnCount++
		}
		if len(agg.Findings) < 5 {
			agg.Findings = append(agg.Findings, map[string]any{
				"filePath": f.FilePath,
				"line":     f.Line,
				"summary":  f.Summary,
			})
		}
	}

	keys := make([]string, 0, len(byRule))
	for k := range byRule {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	byCheck := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		agg := byRule[k]
		byCheck = append(byCheck, map[string]any{
			"ruleId":     agg.RuleID,
			"wouldBlock": len(agg.WouldBlockTasks),
			"warnCount":  agg.WarnCount,
			"findings":   agg.Findings,
		})
	}

	return map[string]any{
		"dryRun":        true,
		"valid":         true,
		"hash":          hash,
		"sampledPRs":    len(seenTasks),
		"wouldBlockPRs": len(blockedTasks),
		"byCheck":       byCheck,
	}, nil
}

func (s *Server) handlePostQAFlowVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := s.tasks.requestActor(ctx)
	isDryRun := r.URL.Query().Get("dryRun") == "1"

	var req postFlowVersionReq
	if !readJSON(w, r, &req) {
		return
	}

	if req.Action == "retire" {
		b, _, _, _, ok, err := s.lookupFlowBundle(ctx, req.Hash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, model.ErrNotFound)
			return
		}
		if b.BuiltIn {
			writeError(w, http.StatusBadRequest, fmt.Errorf("built-in flow %q cannot be retired", b.Key))
			return
		}
		if isDryRun {
			writeJSON(w, http.StatusOK, map[string]any{"dryRun": true, "valid": true, "hash": b.Hash})
			return
		}
		now := s.tasks.now()
		if err := s.tasks.Store.RetireQAFlowVersion(ctx, b.Hash, now); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		_, _ = s.tasks.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:         now,
			By:         actor.Actor,
			OnBehalfOf: actor.OnBehalfOf,
			Surface:    "qa_flow_version",
			Summary:    fmt.Sprintf("retire %s@%d (%s)", b.Key, b.Version, b.Hash),
		})
		retired := now.Format(time.RFC3339)
		writeJSON(w, http.StatusOK, map[string]any{
			"hash":      b.Hash,
			"retiredAt": retired,
		})
		return
	}

	// Dry-run by existing flow hash/ref (used by Start Experiment dialog and Processes detail view).
	if isDryRun && strings.TrimSpace(req.Hash) != "" && req.YAML == "" && !hasRawDeclaration(req.Declaration) {
		b, _, _, _, ok, err := s.lookupFlowBundle(ctx, req.Hash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, model.ErrNotFound)
			return
		}
		sim, err := s.simulateDryRun(ctx, b.Resolved, b.Hash, req.SampleLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sim)
		return
	}

	var yamlSrc string
	switch {
	case strings.TrimSpace(req.YAML) != "":
		yamlSrc = req.YAML
	case hasRawDeclaration(req.Declaration):
		yamlSrc = string(bytes.TrimSpace(req.Declaration))
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing yaml or declaration"))
		return
	}

	bundle, err := qa.ResolveAnyFlowDeclaration([]byte(yamlSrc), 0, func(p string) (qa.FlowVersionBundle, bool) {
		b, _, _, _, ok, _ := s.lookupFlowBundle(ctx, p)
		return b, ok
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	version, err := s.tasks.Store.NextQAFlowVersionNumber(ctx, bundle.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bundle.Version = version

	if isDryRun {
		sim, err := s.simulateDryRun(ctx, bundle.Resolved, bundle.Hash, req.SampleLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sim)
		return
	}

	// Idempotent publish if identical canonical hash already exists.
	if existing, err := s.tasks.Store.GetQAFlowVersion(ctx, bundle.Hash); err == nil {
		now := s.tasks.now()
		_, _ = s.tasks.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:         now,
			By:         actor.Actor,
			OnBehalfOf: actor.OnBehalfOf,
			Surface:    "qa_flow_version",
			Summary:    fmt.Sprintf("re-publish %s@%d (%s)", existing.Key, existing.Version, existing.Hash),
		})
		created := existing.CreatedAt
		bundle.Version = existing.Version
		writeJSON(w, http.StatusOK, map[string]any{
			"version": formatFlowBundleMap(bundle, &created, existing.CreatedBy, existing.RetiredAt),
			"hash":    existing.Hash,
		})
		return
	}

	now := s.tasks.now()
	fv := model.QAFlowVersion{
		Hash:        bundle.Hash,
		Key:         bundle.Key,
		Version:     version,
		ParentHash:  bundle.ParentHash,
		Label:       bundle.Label,
		Summary:     bundle.Summary,
		SpecJSON:    bundle.SpecJSON,
		SourceYAML:  yamlSrc,
		QAEngineRev: qa.QAEngineRev,
		CreatedBy:   actor.Actor.ID,
		CreatedAt:   now,
		SchemaVer:   bundle.SchemaVer,
		SourceKind:  "ui",
	}
	if err := s.tasks.Store.InsertQAFlowVersion(ctx, fv); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_, _ = s.tasks.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:         now,
		By:         actor.Actor,
		OnBehalfOf: actor.OnBehalfOf,
		Surface:    "qa_flow_version",
		Summary:    fmt.Sprintf("publish %s@%d (%s)", fv.Key, fv.Version, fv.Hash),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"version": formatFlowBundleMap(bundle, &now, fv.CreatedBy, nil),
		"hash":    fv.Hash,
	})
}

type postQAFlowVersionAdoptReq struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
	DryRun bool   `json:"dryRun"`
}

func isValidAdoptHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isValidAdoptFlowPath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return false
	}
	cleaned := path.Clean(p)
	if cleaned != p || !strings.HasPrefix(cleaned, ".grain/") {
		return false
	}
	lower := strings.ToLower(cleaned)
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func (s *Server) handlePostQAFlowVersionAdopt(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := s.tasks.requestActor(ctx)
	if actor.Actor.Kind == model.PrincipalAgent {
		writeError(w, http.StatusForbidden, errors.New("agent tokens cannot adopt quality flow versions"))
		return
	}

	var req postQAFlowVersionAdoptReq
	if !readJSON(w, r, &req) {
		return
	}

	isDryRun := req.DryRun || r.URL.Query().Get("dryRun") == "1"

	repoTrimmed := strings.TrimSpace(req.Repo)
	owner, repoName, hasSlash := strings.Cut(repoTrimmed, "/")
	if !hasSlash || owner == "" || repoName == "" || strings.Contains(repoName, "/") || strings.ContainsAny(repoTrimmed, " \t\r\n") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repo must be in owner/name format"))
		return
	}
	req.Repo = owner + "/" + repoName
	if ar := accessRoleFromContext(ctx); ar != nil && (!ar.allows("qa:admin", "") || !ar.allows("qa:admin", req.Repo)) {
		writeError(w, http.StatusForbidden, fmt.Errorf("%s cannot adopt global flow versions from %s", ar.label(), req.Repo))
		return
	}

	req.Commit = strings.ToLower(strings.TrimSpace(req.Commit))
	if !isValidAdoptHexSHA(req.Commit) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("commit must be a 40-char hexadecimal SHA"))
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if !isValidAdoptFlowPath(req.Path) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path must be a normalized .yml/.yaml file under .grain/"))
		return
	}

	readFile := s.ReadRepoFileAtCommit
	if readFile == nil {
		if hook := resolve.GetRuntimeHooks(); hook != nil {
			readFile = hook.ReadRepoFileAtCommit
		}
	}
	if readFile == nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("ReadRepoFileAtCommit hook not configured"))
		return
	}

	bContent, err := readFile(ctx, req.Repo, req.Commit, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("failed to read file: %w", err))
		return
	}
	content := string(bContent)

	bundle, err := qa.ResolveAnyFlowDeclaration([]byte(content), 0, func(p string) (qa.FlowVersionBundle, bool) {
		b, _, _, _, ok, _ := s.lookupFlowBundle(ctx, p)
		return b, ok
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if isDryRun {
		sim, err := s.simulateDryRun(ctx, bundle.Resolved, bundle.Hash, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sim)
		return
	}

	version, err := s.tasks.Store.NextQAFlowVersionNumber(ctx, bundle.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bundle.Version = version

	if existing, err := s.tasks.Store.GetQAFlowVersion(ctx, bundle.Hash); err == nil {
		now := s.tasks.now()
		_, _ = s.tasks.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:         now,
			By:         actor.Actor,
			OnBehalfOf: actor.OnBehalfOf,
			Surface:    "qa_flow_version",
			Summary:    fmt.Sprintf("re-adopt %s@%d (%s)", existing.Key, existing.Version, existing.Hash),
		})
		created := existing.CreatedAt
		writeJSON(w, http.StatusOK, map[string]any{
			"version": formatFlowBundleMap(bundle, &created, existing.CreatedBy, existing.RetiredAt),
			"hash":    existing.Hash,
		})
		return
	}

	now := s.tasks.now()
	fv := model.QAFlowVersion{
		Hash:         bundle.Hash,
		Key:          bundle.Key,
		Version:      version,
		ParentHash:   bundle.ParentHash,
		Label:        bundle.Label,
		Summary:      bundle.Summary,
		SpecJSON:     bundle.SpecJSON,
		SourceYAML:   content,
		QAEngineRev:  qa.QAEngineRev,
		CreatedBy:    actor.Actor.ID,
		CreatedAt:    now,
		SchemaVer:    bundle.SchemaVer,
		SourceKind:   "adopt",
		SourceRepo:   req.Repo,
		SourcePath:   req.Path,
		SourceCommit: req.Commit,
	}

	if err := s.tasks.Store.InsertQAFlowVersion(ctx, fv); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_, _ = s.tasks.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:         now,
		By:         actor.Actor,
		OnBehalfOf: actor.OnBehalfOf,
		Surface:    "qa_flow_version",
		Summary:    fmt.Sprintf("adopt %s@%d (%s)", fv.Key, fv.Version, fv.Hash),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"version": formatFlowBundleMap(bundle, &now, fv.CreatedBy, nil),
		"hash":    fv.Hash,
	})
}
