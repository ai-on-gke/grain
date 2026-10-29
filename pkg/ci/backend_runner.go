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

package ci

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/gritapi"
	"github.com/ai-on-gke/grain/pkg/treeid"
	"github.com/ai-on-gke/grain/pkg/version"
)

// FileCursorStore persists a `(epoch, afterSeq)` event stream cursor on disk
// atomically via write-temp-and-rename.
type FileCursorStore struct {
	mu   sync.Mutex
	path string
}

type persistedCursor struct {
	Epoch    string `json:"epoch"`
	AfterSeq int64  `json:"afterSeq"`
}

// NewFileCursorStore constructs a file-backed `gritapi.CursorStore` at path.
func NewFileCursorStore(path string) *FileCursorStore {
	return &FileCursorStore{path: path}
}

// LoadCursor reads the persisted `(epoch, afterSeq)` cursor, returning `("", 0, nil)`
// if the file does not yet exist.
func (s *FileCursorStore) LoadCursor() (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	var c persistedCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", 0, err
	}
	return c.Epoch, c.AfterSeq, nil
}

// SaveCursor writes `(epoch, afterSeq)` atomically to disk.
func (s *FileCursorStore) SaveCursor(epoch string, afterSeq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(persistedCursor{Epoch: epoch, AfterSeq: afterSeq})
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type memCursorStore struct {
	mu    sync.Mutex
	epoch string
	seq   int64
}

func (m *memCursorStore) LoadCursor() (string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch, m.seq, nil
}

func (m *memCursorStore) SaveCursor(epoch string, seq int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch = epoch
	m.seq = seq
	return nil
}

// OutboxKind identifies the kind of pending mutation in BackendRunner's outbox.
type OutboxKind string

const (
	OutboxStart     OutboxKind = "start"
	OutboxCancel    OutboxKind = "cancel"
	OutboxRetrigger OutboxKind = "retrigger"
)

// OutboxEntry is a durable mutation queued for delivery to `gritapi.Backend`.
type OutboxEntry struct {
	Kind         OutboxKind                `json:"kind"`
	RequestID    string                    `json:"requestId,omitempty"`
	RunID        string                    `json:"runId,omitempty"`
	StartReq     *gritapi.StartRun         `json:"startReq,omitempty"`
	TreeArchive  []byte                    `json:"treeArchive,omitempty"`
	RetriggerReq *gritapi.RetriggerRequest `json:"retriggerReq,omitempty"`
	CancelReason string                    `json:"cancelReason,omitempty"`
	CancelBy     CancelSource              `json:"cancelBy,omitempty"`
	RequestedBy  string                    `json:"requestedBy,omitempty"`
	Recheck      bool                      `json:"recheck,omitempty"`
	CreatedAt    time.Time                 `json:"createdAt"`
}

type trackedRunMeta struct {
	RunID        string       `json:"runId"`
	RequestID    string       `json:"requestId"`
	Repo         string       `json:"repo"`
	Ref          string       `json:"ref"`
	Commit       string       `json:"commit"`
	Tree         treeid.ID    `json:"tree"`
	Event        Event        `json:"event"`
	RequestedBy  string       `json:"requestedBy,omitempty"`
	Recheck      bool         `json:"recheck,omitempty"`
	CancelReason string       `json:"cancelReason,omitempty"`
	CancelBy     CancelSource `json:"cancelBy,omitempty"`
	Concluded    bool         `json:"concluded,omitempty"`
}

// BackendRunnerConfig configures a `BackendRunner` backed by `gritapi.Backend`.
type BackendRunnerConfig struct {
	// Backend is the execution engine (`*grit.Local` or `*gritapi.Client`). Required.
	Backend gritapi.Backend
	// Source is an optional workflow source used for WorkflowOptions and tree resolution.
	Source Source
	// Store is where CI runs are stored for grain readers; nil uses NewRunStore(0).
	Store RunStore
	// Cursor persists the `/v1/events` `(epoch, afterSeq)` cursor.
	// If nil and StateDir is non-empty, a FileCursorStore in StateDir is used.
	Cursor gritapi.CursorStore
	// StateDir is an optional directory where the cursor, outbox, and tracked
	// run metadata are persisted across process restarts.
	StateDir string
	// VerifyKeys is an optional static set of attestation verification keys.
	// When empty, public keys are loaded from `Backend.Info(ctx)`.
	VerifyKeys attest.KeySet
	// TreeResolver resolves the canonical `(treeid.ID, ustarArchive)` for a Request.
	// When nil, a deterministic canonical ustar archive is synthesized from the
	// request's commit/workflows.
	TreeResolver func(ctx context.Context, req Request) (treeid.ID, []byte, error)
	// SecretResolver resolves per-request capability secrets into a `gritapi.SecretPayload`.
	SecretResolver func(ctx context.Context, req Request) (gritapi.SecretPayload, error)
	// OnVerdictVerified is an optional callback invoked after a completed run's
	// attestation is verified and its terminal verdict is recorded in Store.
	OnVerdictVerified func(run Run)
}

// BackendRunner implements `ci.Runner` over a `gritapi.Backend` (`grit.Local`
// in-process or `gritapi.Client` talking to `grit serve`), with a durable
// mutation outbox, persisted `/v1/events` cursor with `410 cursor_gone` resync,
// and DSSE attestation verification before recording terminal verdicts.
type BackendRunner struct {
	backend           gritapi.Backend
	source            Source
	store             RunStore
	cursor            gritapi.CursorStore
	stateDir          string
	verifyKeys        attest.KeySet
	treeResolver      func(ctx context.Context, req Request) (treeid.ID, []byte, error)
	secretResolver    func(ctx context.Context, req Request) (gritapi.SecretPayload, error)
	onVerdictVerified func(run Run)

	mu      sync.Mutex
	outbox  []OutboxEntry
	tracked map[string]trackedRunMeta // runID -> metadata
	byReqID map[string]string         // requestID -> runID
	syncMu  sync.Mutex
}

var _ Runner = (*BackendRunner)(nil)

// NewBackendRunner constructs a `BackendRunner` from cfg and loads any persisted
// outbox and tracked in-flight runs from `cfg.StateDir`.
func NewBackendRunner(cfg BackendRunnerConfig) *BackendRunner {
	store := cfg.Store
	if store == nil {
		store = NewRunStore(0)
	}
	cursor := cfg.Cursor
	if cursor == nil {
		if cfg.StateDir != "" {
			cursor = NewFileCursorStore(filepath.Join(cfg.StateDir, "grit-cursor.json"))
		} else {
			cursor = &memCursorStore{}
		}
	}
	r := &BackendRunner{
		backend:           cfg.Backend,
		source:            cfg.Source,
		store:             store,
		cursor:            cursor,
		stateDir:          cfg.StateDir,
		verifyKeys:        cfg.VerifyKeys,
		treeResolver:      cfg.TreeResolver,
		secretResolver:    cfg.SecretResolver,
		onVerdictVerified: cfg.OnVerdictVerified,
		tracked:           make(map[string]trackedRunMeta),
		byReqID:           make(map[string]string),
	}
	r.loadState()
	return r
}

func (r *BackendRunner) outboxPath() string {
	if r.stateDir == "" {
		return ""
	}
	return filepath.Join(r.stateDir, "grit-outbox.json")
}

func (r *BackendRunner) trackedPath() string {
	if r.stateDir == "" {
		return ""
	}
	return filepath.Join(r.stateDir, "grit-runs.json")
}

func (r *BackendRunner) loadState() {
	if p := r.outboxPath(); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(raw, &r.outbox)
		}
	}
	if p := r.trackedPath(); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			var list []trackedRunMeta
			if json.Unmarshal(raw, &list) == nil {
				for _, m := range list {
					r.tracked[m.RunID] = m
					if m.RequestID != "" {
						r.byReqID[m.RequestID] = m.RunID
					}
					if _, ok := r.store.Get(m.RunID); !ok && !m.Concluded {
						now := time.Now().UTC()
						r.store.Put(Run{
							ID:          m.RunID,
							Key:         runKeyForMeta(m),
							Repo:        m.Repo,
							Ref:         m.Ref,
							Commit:      m.Commit,
							Event:       m.Event,
							RequestedBy: m.RequestedBy,
							Recheck:     m.Recheck,
							StartedAt:   &now,
						})
					}
				}
			}
		}
	}
}

func (r *BackendRunner) saveOutboxLocked() {
	p := r.outboxPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if raw, err := json.Marshal(r.outbox); err == nil {
		tmp := p + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, p)
		}
	}
}

func (r *BackendRunner) saveTrackedLocked() {
	p := r.trackedPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	list := make([]trackedRunMeta, 0, len(r.tracked))
	for _, m := range r.tracked {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RunID < list[j].RunID })
	if raw, err := json.Marshal(list); err == nil {
		tmp := p + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, p)
		}
	}
}

func runKeyForMeta(m trackedRunMeta) Key {
	return HashOf("grit-run", struct {
		Repo, Ref, Commit string
		Tree              string
		Event             Event
	}{m.Repo, m.Ref, m.Commit, m.Tree.SHA256, m.Event})
}

// PendingOutbox returns a snapshot of the mutations still queued in the outbox.
func (r *BackendRunner) PendingOutbox() []OutboxEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]OutboxEntry, len(r.outbox))
	copy(out, r.outbox)
	return out
}

// OverrideExpectedTree overrides the expected `treeid.ID` for a tracked run
// (used in tests to verify that an attestation signed for another tree is
// rejected by the consumer before recording a verdict).
func (r *BackendRunner) OverrideExpectedTree(runID string, tree treeid.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.tracked[runID]; ok {
		m.Tree = tree
		r.tracked[runID] = m
		r.saveTrackedLocked()
	}
}

// Start plans and submits a run to `gritapi.Backend` via the durable outbox,
// negotiating and uploading the canonical tree archive and passing per-request
// resolved capability secrets.
func (r *BackendRunner) Start(ctx context.Context, req Request) (Run, error) {
	if r.backend == nil {
		return Run{}, errNoRunner
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return Run{}, err
	}

	treeID, archive, err := r.resolveTree(ctx, req)
	if err != nil {
		return Run{}, err
	}
	var secretsPtr *gritapi.SecretPayload
	if r.secretResolver != nil {
		secrets, err := r.secretResolver(ctx, req)
		if err != nil {
			return Run{}, fmt.Errorf("ci: resolving capability secrets: %w", err)
		}
		if len(secrets.Manifest) > 0 || len(secrets.Blobs) > 0 {
			secretsPtr = &secrets
		}
	}

	attempt := 1
	if req.Recheck {
		attempt = int(time.Now().UnixNano() & 0xfffffff)
		if attempt <= 1 {
			attempt = 2
		}
	}

	startReq := gritapi.StartRun{
		Repo:           req.Repo,
		Ref:            req.Ref,
		Commit:         req.Commit,
		Tree:           treeID,
		Event:          string(req.Event),
		TrustedRelease: req.TrustedRelease,
		Jobs:           req.Jobs,
		Secrets:        secretsPtr,
		Attempt:        attempt,
	}
	startReq.RequestID = gritapi.ComputeRequestID(startReq)

	r.mu.Lock()
	if existingID, ok := r.byReqID[startReq.RequestID]; ok {
		if existingRun, found := r.store.Get(existingID); found && !existingRun.Conclusion.Done() {
			r.mu.Unlock()
			return existingRun, nil
		}
	}
	entry := OutboxEntry{
		Kind:        OutboxStart,
		RequestID:   startReq.RequestID,
		StartReq:    &startReq,
		TreeArchive: archive,
		RequestedBy: req.RequestedBy,
		Recheck:     req.Recheck,
		CreatedAt:   time.Now().UTC(),
	}
	r.outbox = append(r.outbox, entry)
	r.saveOutboxLocked()
	r.mu.Unlock()

	if err := r.FlushOutbox(ctx); err != nil {
		return Run{}, err
	}

	r.mu.Lock()
	runID := r.byReqID[startReq.RequestID]
	r.mu.Unlock()
	if run, ok := r.store.Get(runID); ok {
		return run, nil
	}
	return Run{}, fmt.Errorf("ci: run for request %s was not recorded", startReq.RequestID)
}

func (r *BackendRunner) resolveTree(ctx context.Context, req Request) (treeid.ID, []byte, error) {
	if r.treeResolver != nil {
		return r.treeResolver(ctx, req)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := fmt.Sprintf("repo=%s\nref=%s\ncommit=%s\n", req.Repo, req.Ref, req.Commit)
	hdr := &tar.Header{
		Name:     ".grain-tree-meta",
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return treeid.ID{}, nil, err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return treeid.ID{}, nil, err
	}
	if err := tw.Close(); err != nil {
		return treeid.ID{}, nil, err
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	gitTree := strings.ToLower(strings.TrimSpace(req.Commit))
	if len(gitTree) != 40 {
		gitSum := sha256.Sum256(append([]byte("git-tree:"), raw...))
		gitTree = hex.EncodeToString(gitSum[:20])
	}
	return treeid.ID{
		GitTree: gitTree,
		SHA256:  hex.EncodeToString(sum[:]),
	}, raw, nil
}

// FlushOutbox delivers any pending mutations in the outbox to `gritapi.Backend`.
// Entries that succeed are removed from the persisted outbox; if an entry fails,
// it remains in the outbox for the next flush attempt.
func (r *BackendRunner) FlushOutbox(ctx context.Context) error {
	r.mu.Lock()
	pending := make([]OutboxEntry, len(r.outbox))
	copy(pending, r.outbox)
	r.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}

	var firstErr error
	var remaining []OutboxEntry
	for i, entry := range pending {
		if firstErr != nil {
			remaining = append(remaining, pending[i:]...)
			break
		}
		switch entry.Kind {
		case OutboxStart:
			if entry.StartReq == nil {
				continue
			}
			rs, err := r.submitStartEntry(ctx, *entry.StartReq, entry.TreeArchive)
			if err != nil {
				firstErr = err
				remaining = append(remaining, pending[i:]...)
				break
			}
			r.recordSubmittedRun(rs, *entry.StartReq, entry.RequestedBy, entry.Recheck)

		case OutboxCancel:
			if entry.RunID == "" {
				continue
			}
			rs, err := r.backend.CancelRun(ctx, entry.RunID)
			if err != nil {
				if apiErr, ok := err.(*gritapi.APIError); ok && apiErr.StatusCode == 404 {
					// Run unknown on backend; drop outbox entry.
					continue
				}
				firstErr = err
				remaining = append(remaining, pending[i:]...)
				break
			}
			r.applyCancelStatus(rs, entry.CancelReason, entry.CancelBy)

		case OutboxRetrigger:
			if entry.RunID == "" || entry.RetriggerReq == nil {
				continue
			}
			rs, err := r.backend.RetriggerRun(ctx, entry.RunID, *entry.RetriggerReq)
			if err != nil {
				firstErr = err
				remaining = append(remaining, pending[i:]...)
				break
			}
			r.mu.Lock()
			meta := r.tracked[entry.RunID]
			meta.Concluded = false
			r.tracked[entry.RunID] = meta
			r.saveTrackedLocked()
			r.mu.Unlock()
			r.updateNonTerminalRun(rs, meta)
		}
	}

	r.mu.Lock()
	r.outbox = remaining
	r.saveOutboxLocked()
	r.mu.Unlock()
	return firstErr
}

func (r *BackendRunner) submitStartEntry(ctx context.Context, req gritapi.StartRun, archive []byte) (gritapi.RunStatus, error) {
	if req.Tree.SHA256 != "" && len(archive) > 0 {
		missing, err := r.backend.NegotiateTrees(ctx, []string{req.Tree.SHA256})
		if err != nil {
			return gritapi.RunStatus{}, err
		}
		if len(missing) > 0 {
			if err := r.backend.UploadTree(ctx, req.Tree.SHA256, archive); err != nil {
				return gritapi.RunStatus{}, err
			}
		}
	}
	rs, err := r.backend.StartRun(ctx, req)
	if missingSHA, ok := gritapi.IsTreeMissing(err); ok && len(archive) > 0 {
		if uErr := r.backend.UploadTree(ctx, missingSHA, archive); uErr != nil {
			return gritapi.RunStatus{}, uErr
		}
		return r.backend.StartRun(ctx, req)
	}
	return rs, err
}

func (r *BackendRunner) recordSubmittedRun(rs gritapi.RunStatus, req gritapi.StartRun, requestedBy string, recheck bool) {
	meta := trackedRunMeta{
		RunID:       rs.ID,
		RequestID:   req.RequestID,
		Repo:        req.Repo,
		Ref:         req.Ref,
		Commit:      req.Commit,
		Tree:        req.Tree,
		Event:       Event(req.Event),
		RequestedBy: requestedBy,
		Recheck:     recheck,
	}
	r.mu.Lock()
	if existing, ok := r.tracked[rs.ID]; ok {
		meta.Concluded = existing.Concluded
		meta.CancelReason = existing.CancelReason
		meta.CancelBy = existing.CancelBy
	}
	r.tracked[rs.ID] = meta
	if req.RequestID != "" {
		r.byReqID[req.RequestID] = rs.ID
	}
	r.saveTrackedLocked()
	r.mu.Unlock()

	if existingRun, ok := r.store.Get(rs.ID); ok && existingRun.Conclusion.Done() {
		return
	}
	r.updateNonTerminalRun(rs, meta)
}

func (r *BackendRunner) updateNonTerminalRun(rs gritapi.RunStatus, meta trackedRunMeta) {
	started := rs.StartedAt
	if started == nil && !rs.CreatedAt.IsZero() {
		t := rs.CreatedAt.UTC()
		started = &t
	}
	if started == nil {
		now := time.Now().UTC()
		started = &now
	}
	jobs := make([]JobRun, 0, len(rs.Jobs))
	for _, j := range rs.Jobs {
		jr := JobRun{
			JobID:      j.ID,
			Name:       j.ID,
			StartedAt:  j.StartedAt,
			FinishedAt: j.CompletedAt,
			Cached:     j.Cached,
		}
		if j.Status == "completed" {
			jr.Conclusion = mapConclusion(j.Conclusion)
		}
		jobs = append(jobs, jr)
	}
	run := Run{
		ID:           rs.ID,
		Key:          runKeyForMeta(meta),
		Repo:         meta.Repo,
		Ref:          meta.Ref,
		Commit:       meta.Commit,
		Event:        meta.Event,
		RequestedBy:  meta.RequestedBy,
		Recheck:      meta.Recheck,
		CancelReason: meta.CancelReason,
		CancelledBy:  meta.CancelBy,
		StartedAt:    started,
		Build:        BuildOf(version.Get()),
		Jobs:         jobs,
	}
	r.store.Put(run)
}

// SyncOnce flushes the outbox and consumes one batch of events from `/v1/events`
// using the persisted cursor, executing a `410 cursor_gone` or epoch-change
// resync over in-flight runs when needed, and verifying `run.attested` DSSE
// envelopes before recording terminal verdicts in `RunStore`.
func (r *BackendRunner) SyncOnce(ctx context.Context, wait time.Duration, limit int) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	if err := r.FlushOutbox(ctx); err != nil {
		return err
	}

	savedEpoch, afterSeq, err := r.cursor.LoadCursor()
	if err != nil {
		return err
	}

	batch, err := r.backend.Events(ctx, afterSeq, wait, limit)
	if goneErr, ok := gritapi.IsCursorGone(err); ok {
		return r.resyncInFlight(ctx, goneErr.Epoch)
	}
	if err != nil {
		return err
	}

	if savedEpoch != "" && batch.Epoch != "" && savedEpoch != batch.Epoch {
		return r.resyncInFlight(ctx, batch.Epoch)
	}

	curEpoch := batch.Epoch
	curSeq := afterSeq
	for _, ev := range batch.Events {
		if err := r.handleEvent(ctx, ev); err != nil {
			return err
		}
		if ev.Seq > curSeq {
			curSeq = ev.Seq
		}
		if err := r.cursor.SaveCursor(curEpoch, curSeq); err != nil {
			return err
		}
	}
	if len(batch.Events) == 0 && savedEpoch == "" && curEpoch != "" {
		if err := r.cursor.SaveCursor(curEpoch, curSeq); err != nil {
			return err
		}
	}
	return nil
}

// RunConsumer runs the background event consumer loop until ctx is cancelled.
func (r *BackendRunner) RunConsumer(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 200 * time.Millisecond
	}
	for {
		if ctx.Err() != nil {
			return
		}
		_ = r.SyncOnce(ctx, pollInterval, 100)
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (r *BackendRunner) inFlightRunIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, m := range r.tracked {
		if !m.Concluded {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (r *BackendRunner) resyncInFlight(ctx context.Context, newEpoch string) error {
	headBatch, err := r.backend.Events(ctx, 0, 0, 1)
	if err != nil {
		return err
	}
	highWater := headBatch.NextSeq - 1
	if highWater < 0 {
		highWater = 0
	}
	if newEpoch == "" {
		newEpoch = headBatch.Epoch
	}

	for _, runID := range r.inFlightRunIDs() {
		rs, rErr := r.backend.GetRun(ctx, runID)
		if rErr != nil {
			if apiErr, ok := rErr.(*gritapi.APIError); ok && apiErr.StatusCode == 404 {
				continue
			}
			return rErr
		}
		if err := r.reconcileRunStatus(ctx, rs); err != nil {
			return err
		}
	}

	latest, err := r.backend.Events(ctx, highWater, 0, 1000)
	if err == nil && latest.NextSeq > 1 {
		highWater = latest.NextSeq - 1
	}
	return r.cursor.SaveCursor(newEpoch, highWater)
}

func (r *BackendRunner) handleEvent(ctx context.Context, ev gritapi.Event) error {
	switch ev.Kind {
	case gritapi.EventRunQueued, gritapi.EventRunPlanned, gritapi.EventJobStarted, gritapi.EventJobAttemptCompleted, gritapi.EventJobCompleted:
		rs, err := r.backend.GetRun(ctx, ev.RunID)
		if err != nil {
			return nil
		}
		r.mu.Lock()
		meta, ok := r.tracked[ev.RunID]
		r.mu.Unlock()
		if !ok {
			meta = trackedRunMeta{
				RunID:     rs.ID,
				RequestID: rs.RequestID,
				Repo:      rs.Repo,
				Ref:       rs.Ref,
				Commit:    rs.Commit,
				Tree:      rs.Tree,
				Event:     Event(rs.Event),
			}
			r.mu.Lock()
			r.tracked[rs.ID] = meta
			r.saveTrackedLocked()
			r.mu.Unlock()
		}
		if !meta.Concluded {
			r.updateNonTerminalRun(rs, meta)
		}
		return nil

	case gritapi.EventRunAttested, gritapi.EventRunCompleted:
		rs, err := r.backend.GetRun(ctx, ev.RunID)
		if err != nil {
			return err
		}
		return r.reconcileRunStatus(ctx, rs)
	}
	return nil
}

func (r *BackendRunner) reconcileRunStatus(ctx context.Context, rs gritapi.RunStatus) error {
	r.mu.Lock()
	meta, ok := r.tracked[rs.ID]
	if !ok {
		meta = trackedRunMeta{
			RunID:     rs.ID,
			RequestID: rs.RequestID,
			Repo:      rs.Repo,
			Ref:       rs.Ref,
			Commit:    rs.Commit,
			Tree:      rs.Tree,
			Event:     Event(rs.Event),
		}
		r.tracked[rs.ID] = meta
		r.saveTrackedLocked()
	}
	r.mu.Unlock()

	if meta.Concluded {
		return nil
	}
	if rs.Status != "completed" {
		r.updateNonTerminalRun(rs, meta)
		return nil
	}
	return r.verifyAndConcludeRun(ctx, rs, meta)
}

func (r *BackendRunner) resolveKeySet(ctx context.Context) (attest.KeySet, error) {
	if len(r.verifyKeys) > 0 {
		return r.verifyKeys, nil
	}
	info, err := r.backend.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching grit public keys from /v1/info: %w", err)
	}
	pubMap := make(map[string]crypto.PublicKey, len(info.AttestationPublicKeys))
	for _, pk := range info.AttestationPublicKeys {
		if pk.PublicPEM == "" {
			continue
		}
		kid, pub, err := attest.ParsePublicKey([]byte(pk.PublicPEM))
		if err != nil {
			return nil, fmt.Errorf("parsing grit public key %s: %w", pk.KeyID, err)
		}
		if pk.KeyID != "" {
			kid = pk.KeyID
		}
		pubMap[kid] = pub
	}
	if len(pubMap) == 0 {
		return nil, fmt.Errorf("grit /v1/info returned no attestation public keys")
	}
	return attest.NewKeySet(pubMap)
}

func (r *BackendRunner) verifyAndConcludeRun(ctx context.Context, rs gritapi.RunStatus, meta trackedRunMeta) error {
	started := rs.StartedAt
	if started == nil && !rs.CreatedAt.IsZero() {
		t := rs.CreatedAt.UTC()
		started = &t
	}
	now := time.Now().UTC()
	if started == nil {
		started = &now
	}
	finished := rs.CompletedAt
	if finished == nil {
		finished = &now
	}

	jobs := make([]JobRun, 0, len(rs.Jobs))
	for _, j := range rs.Jobs {
		logBytes, _, _, _ := r.backend.GetLog(ctx, rs.ID, j.ID, 0, int64(MaxStoredStepOutput))
		jr := JobRun{
			JobID:      j.ID,
			Name:       j.ID,
			StartedAt:  j.StartedAt,
			FinishedAt: j.CompletedAt,
			Cached:     j.Cached,
			Conclusion: mapConclusion(j.Conclusion),
			Steps: []StepRun{
				{
					Name:       j.ID,
					StartedAt:  j.StartedAt,
					FinishedAt: j.CompletedAt,
					Conclusion: mapConclusion(j.Conclusion),
					Output:     string(logBytes),
				},
			},
		}
		jobs = append(jobs, jr)
	}

	conclusion := mapConclusion(rs.Conclusion)
	var runErr string

	// Cancelled runs may not have an attestation; all other completed verdicts
	// must pass DSSE signature and subject (tree SHA256 + gitTree + commit)
	// verification before being recorded in RunStore / ci.Gate.
	if conclusion != ConclusionCancelled {
		if err := r.verifyRunAttestation(ctx, rs, meta); err != nil {
			conclusion = ConclusionErrored
			runErr = fmt.Sprintf("attestation verification failed: %v", err)
			// No job's conclusion is vouched for either: keep a
			// "success" from the unsigned status out of the job rows,
			// where `grain ci list` would count it as green.
			for i := range jobs {
				if jobs[i].Conclusion == ConclusionSuccess {
					jobs[i].Conclusion = ConclusionErrored
					for k := range jobs[i].Steps {
						jobs[i].Steps[k].Conclusion = ConclusionErrored
					}
				}
			}
		}
	}

	run := Run{
		ID:           rs.ID,
		Key:          runKeyForMeta(meta),
		Repo:         meta.Repo,
		Ref:          meta.Ref,
		Commit:       meta.Commit,
		Event:        meta.Event,
		RequestedBy:  meta.RequestedBy,
		Recheck:      meta.Recheck,
		CancelReason: meta.CancelReason,
		CancelledBy:  meta.CancelBy,
		StartedAt:    started,
		FinishedAt:   finished,
		Conclusion:   conclusion,
		Error:        runErr,
		Build:        BuildOf(version.Get()),
		Jobs:         jobs,
	}
	r.store.Put(run)

	r.mu.Lock()
	meta.Concluded = true
	r.tracked[rs.ID] = meta
	r.saveTrackedLocked()
	r.mu.Unlock()

	if r.onVerdictVerified != nil && runErr == "" {
		r.onVerdictVerified(run)
	}
	return nil
}

func (r *BackendRunner) verifyRunAttestation(ctx context.Context, rs gritapi.RunStatus, meta trackedRunMeta) error {
	runID := rs.ID
	env, err := r.backend.GetAttestation(ctx, runID)
	if err != nil {
		return fmt.Errorf("fetching attestation for run %s: %w", runID, err)
	}
	keys, err := r.resolveKeySet(ctx)
	if err != nil {
		return err
	}
	stmt, err := attest.Verify(env, keys)
	if err != nil {
		return err
	}
	expectedTree := attest.Tree{
		SHA256:  meta.Tree.SHA256,
		GitTree: meta.Tree.GitTree,
	}
	if err := stmt.CheckSubject(expectedTree, meta.Commit); err != nil {
		return err
	}
	return checkAttestedVerdict(stmt, rs)
}

// checkAttestedVerdict binds the verdict about to be recorded to the one
// grit signed. The run status and its jobs' conclusions come from
// unsigned `GET /v1/runs/{id}` responses; only the attestation is signed,
// so a status that names another run, or a job set or conclusion the
// statement does not, is refused rather than recorded -- otherwise a
// verified attestation of a failing run would carry a "success" status
// through (docs/test-runs/2026-10-29-grit-backend-ci-shutdown.md, F2).
func checkAttestedVerdict(stmt *attest.Statement, rs gritapi.RunStatus) error {
	if got := stmt.Predicate.Request.Run; got != "" && got != rs.ID {
		return fmt.Errorf("attestation is for run %s, not %s", got, rs.ID)
	}
	attested := make(map[string]string, len(stmt.Predicate.Jobs))
	for _, j := range stmt.Predicate.Jobs {
		attested[j.ID] = strings.ToLower(strings.TrimSpace(j.Result.Conclusion))
	}
	if len(attested) != len(rs.Jobs) {
		return fmt.Errorf("attestation covers %d jobs, run status reports %d", len(attested), len(rs.Jobs))
	}
	for _, j := range rs.Jobs {
		want, ok := attested[j.ID]
		if !ok {
			return fmt.Errorf("job %s is not in the attestation", j.ID)
		}
		if got := strings.ToLower(strings.TrimSpace(j.Conclusion)); got != want {
			return fmt.Errorf("job %s: run status reports %q, attestation signs %q", j.ID, got, want)
		}
	}
	return nil
}

func mapConclusion(c string) Conclusion {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "success":
		return ConclusionSuccess
	case "failure":
		return ConclusionFailure
	case "cancelled", "canceled":
		return ConclusionCancelled
	case "skipped":
		return ConclusionSkipped
	case "errored", "error":
		return ConclusionErrored
	case "":
		return ConclusionPending
	default:
		return ConclusionErrored
	}
}

func (r *BackendRunner) applyCancelStatus(rs gritapi.RunStatus, reason string, by CancelSource) {
	r.mu.Lock()
	meta, ok := r.tracked[rs.ID]
	if ok {
		meta.CancelReason = reason
		meta.CancelBy = by
		meta.Concluded = true
		r.tracked[rs.ID] = meta
		r.saveTrackedLocked()
	}
	r.mu.Unlock()

	now := time.Now().UTC()
	r.store.Update(rs.ID, func(run *Run) {
		run.Conclusion = ConclusionCancelled
		run.CancelReason = reason
		run.CancelledBy = by
		if run.FinishedAt == nil {
			run.FinishedAt = &now
		}
	})
}

// WorkflowOptions reads matching workflows from Source if configured.
func (r *BackendRunner) WorkflowOptions(ctx context.Context, req Request) ([]WorkflowOption, error) {
	if r.source == nil {
		return nil, nil
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return nil, err
	}
	workflows, _, err := r.source.Workflows(ctx, req.Repo, req.Ref)
	if err != nil {
		return nil, err
	}
	options := make([]WorkflowOption, 0, len(workflows))
	for _, wf := range workflows {
		if _, matches := WorkflowEvent(wf, req); matches {
			options = append(options, WorkflowOption{Path: wf.Path, Name: wf.Name})
		}
	}
	return options, nil
}

// Get returns one run by id from Store.
func (r *BackendRunner) Get(id string) (Run, bool) {
	return r.store.Get(id)
}

// List returns the runs held in Store, newest first.
func (r *BackendRunner) List() []Run {
	return r.store.List()
}

// Find returns runs matching f up to limit.
func (r *BackendRunner) Find(f RunFilter, limit int) RunPage {
	return r.store.Find(f, limit)
}

// Cancel queues a cancellation in the outbox, flushes it to `gritapi.Backend`,
// and marks the run cancelled in `Store`.
func (r *BackendRunner) Cancel(id, reason string, by CancelSource) bool {
	existing, ok := r.store.Get(id)
	if !ok || existing.Conclusion.Done() {
		return false
	}
	r.mu.Lock()
	if meta, exists := r.tracked[id]; exists {
		meta.CancelReason = reason
		meta.CancelBy = by
		r.tracked[id] = meta
		r.saveTrackedLocked()
	}
	r.outbox = append(r.outbox, OutboxEntry{
		Kind:         OutboxCancel,
		RunID:        id,
		CancelReason: reason,
		CancelBy:     by,
		CreatedAt:    time.Now().UTC(),
	})
	r.saveOutboxLocked()
	r.mu.Unlock()

	now := time.Now().UTC()
	r.store.Update(id, func(run *Run) {
		run.Conclusion = ConclusionCancelled
		run.CancelReason = reason
		run.CancelledBy = by
		if run.FinishedAt == nil {
			run.FinishedAt = &now
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.FlushOutbox(ctx)
	return true
}

// MarkFlake marks jobs of run id that passed an uncached recheck as flakes.
func (r *BackendRunner) MarkFlake(id string, jobs []string, rechecks string) bool {
	if _, ok := r.store.Get(id); !ok {
		return false
	}
	want := map[string]bool{}
	for _, j := range jobs {
		want[j] = true
	}
	r.store.Update(id, func(run *Run) {
		for i := range run.Jobs {
			job := &run.Jobs[i]
			if job.Conclusion == ConclusionSuccess || !(want[job.JobID] || want[job.WorkflowPath+":"+job.JobID]) {
				continue
			}
			job.Flake = true
			job.Warning = fmt.Sprintf("flaky: failed here and passed an uncached recheck of the same tree (run %s)", rechecks)
		}
	})
	return true
}

func (r *BackendRunner) CacheStats(context.Context) (CacheStats, bool, error) {
	return CacheStats{}, false, nil
}

func (r *BackendRunner) ClearCache(context.Context) (int, bool, error) {
	return 0, false, nil
}

func (r *BackendRunner) EvictBaseTransitionFailures(context.Context, string) (int, bool, error) {
	return 0, false, nil
}

func (r *BackendRunner) ClearArtifacts(context.Context) (int, int64, bool, error) {
	return 0, 0, false, nil
}

func (r *BackendRunner) ArtifactStats(context.Context) (ArtifactStats, bool, error) {
	return ArtifactStats{}, false, nil
}

func (r *BackendRunner) RunArtifacts(_ context.Context, runID string) ([]Artifact, bool, error) {
	run, ok := r.store.Get(runID)
	if !ok {
		return nil, false, nil
	}
	var out []Artifact
	for _, j := range run.Jobs {
		out = append(out, j.Artifacts...)
	}
	return out, true, nil
}

func (r *BackendRunner) OpenArtifact(ctx context.Context, ref ArtifactRef) (io.ReadCloser, Artifact, error) {
	if r.backend == nil {
		return nil, Artifact{}, fmt.Errorf("no backend (%s/%s/%s): %w", ref.Run, ref.Job, ref.Name, fs.ErrNotExist)
	}
	raw, err := r.backend.GetArtifact(ctx, ref.Run, ref.Job, ref.Name)
	if err != nil {
		return nil, Artifact{}, fmt.Errorf("artifact %s/%s/%s: %w", ref.Run, ref.Job, ref.Name, fs.ErrNotExist)
	}
	art := Artifact{
		Run:    ref.Run,
		Job:    ref.Job,
		Name:   ref.Name,
		Stored: true,
		Bytes:  int64(len(raw)),
	}
	return io.NopCloser(bytes.NewReader(raw)), art, nil
}

func (r *BackendRunner) WithArtifacts(_ context.Context, run Run) Run {
	return run
}
