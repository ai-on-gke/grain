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

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// DefaultTranscriptSyncInterval is how often live run event streams in
// <data-dir>/state/transcripts/ are flushed to the object store while runs are
// executing.
const DefaultTranscriptSyncInterval = 5 * time.Second

// LiveTranscriptDir returns the ui.LiveTranscript reader for transcriptDir.
// It can no longer be one framework's reader chosen at startup: a task carries
// its own model.Task.AgentFramework now, so two runs live in this same
// directory in two different formats (RunConfig.TranscriptPath's own doc
// comment), and picking by deployment default would leave every overriding
// task's live transcript blank until it finished.
//
// So the format is decided per file, by what is in it, rather than per
// deployment -- see liveTranscripts.Tail.
func LiveTranscriptDir(transcriptDir string) ui.LiveTranscript {
	return LiveTranscriptDirWithStore(transcriptDir, nil)
}

// LiveTranscriptDirWithStore returns a ui.LiveTranscript that reads local
// transcriptDir files first and, when store is non-nil and a run's live file is
// not on this host's local disk, falls back to the latest <runID>.jsonl flushed
// to the object store.
func LiveTranscriptDirWithStore(transcriptDir string, store *TranscriptObjectStore) ui.LiveTranscript {
	return liveTranscripts{
		dir:         transcriptDir,
		claude:      claude.LiveTranscriptDir{Dir: transcriptDir},
		antigravity: antigravity.LiveTranscriptDir{Dir: transcriptDir},
		codex:       codex.LiveTranscriptDir{Dir: transcriptDir},
		remote:      store,
	}
}

// liveTranscripts reads a still-running run's transcript-in-progress
// with whichever framework's reader matches the file it finds.
type liveTranscripts struct {
	dir         string
	claude      claude.LiveTranscriptDir
	antigravity antigravity.LiveTranscriptDir
	codex       codex.LiveTranscriptDir
	remote      *TranscriptObjectStore
}

// Tail sniffs the file rather than being told which framework wrote it,
// the daemon having no per-run record of the framework to consult.
func (l liveTranscripts) Tail(runID string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(l.dir, runID))
	if os.IsNotExist(err) {
		if l.remote != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return l.remote.TailRemoteRaw(ctx, runID)
		}
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch transcriptFramework(string(data)) {
	case model.AgentFrameworkClaude:
		return l.claude.Tail(runID)
	case model.AgentFrameworkCodex:
		return l.codex.Tail(runID)
	default:
		return l.antigravity.Tail(runID)
	}
}

func renderRawTranscript(raw string) string {
	switch transcriptFramework(raw) {
	case model.AgentFrameworkClaude:
		return claude.PartialTranscript(raw)
	case model.AgentFrameworkCodex:
		return codex.PartialTranscript(raw)
	default:
		return antigravity.PartialTranscript(raw)
	}
}

// transcriptFramework names which framework wrote a capture, by the
// shape of the first line of it that parses -- not simply the first
// line, which is what keeps a file caught mid-write readable.
func transcriptFramework(data string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var tagged struct {
			Type  string          `json:"type"`
			Event string          `json:"event"`
			Msg   json.RawMessage `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &tagged); err != nil {
			continue
		}
		switch {
		case tagged.Event != "":
			return model.AgentFrameworkAntigravity
		case len(tagged.Msg) > 0:
			return model.AgentFrameworkCodex
		case strings.Contains(tagged.Type, "."):
			return model.AgentFrameworkCodex
		case tagged.Type != "":
			return model.AgentFrameworkClaude
		}
	}
	return ""
}

// TranscriptObjectStore flushes live run event streams (<runID>.jsonl) and
// finished redacted narrative transcripts (<runID>.md) to an objectstore.Store
// under "transcripts/", serving reads through a local read-through cache.
type TranscriptObjectStore struct {
	scoped  objectstore.Store
	cache   *objectstore.ReadThroughCache
	liveDir string
	redact  func(string) string

	mu         sync.Mutex
	lastSynced map[string]transcriptFileStamp
}

type transcriptFileStamp struct {
	size    int64
	modNano int64
}

// NewTranscriptObjectStore constructs a TranscriptObjectStore scoped under
// "transcripts/" inside baseStore, caching finished transcripts locally in
// cacheDir and syncing live streams from liveDir.
func NewTranscriptObjectStore(baseStore objectstore.Store, cacheDir, liveDir string, redact func(string) string) (*TranscriptObjectStore, error) {
	if baseStore == nil {
		return nil, errors.New("daemon: transcript object store is nil")
	}
	scoped := objectstore.WithPrefix(baseStore, "transcripts")
	cache, err := objectstore.NewReadThroughCache(scoped, cacheDir)
	if err != nil {
		return nil, err
	}
	return &TranscriptObjectStore{
		scoped:     scoped,
		cache:      cache,
		liveDir:    liveDir,
		redact:     redact,
		lastSynced: make(map[string]transcriptFileStamp),
	}, nil
}

// Cache returns the local read-through cache for finished transcripts.
func (t *TranscriptObjectStore) Cache() *objectstore.ReadThroughCache {
	if t == nil {
		return nil
	}
	return t.cache
}

func validTranscriptRunID(id string) bool {
	if id == "" || len(id) > 128 || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// StartPeriodicSync launches a background loop that flushes modified live
// transcripts in liveDir every interval until ctx is cancelled or stop is
// called.
func (t *TranscriptObjectStore) StartPeriodicSync(ctx context.Context, interval time.Duration) (stop func()) {
	if t == nil || t.liveDir == "" {
		return func() {}
	}
	if interval <= 0 {
		interval = DefaultTranscriptSyncInterval
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := t.SyncChanged(loopCtx); err != nil && loopCtx.Err() == nil {
					log.Printf("grain daemon: syncing live transcripts to object store: %v", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// SyncChanged uploads any live transcript file in liveDir whose size or mtime
// changed since the previous sync.
func (t *TranscriptObjectStore) SyncChanged(ctx context.Context) error {
	return t.syncLiveDir(ctx, false)
}

// FlushAll unconditionally uploads every live transcript file currently in
// liveDir (for example during daemon shutdown / VM drain).
func (t *TranscriptObjectStore) FlushAll(ctx context.Context) error {
	return t.syncLiveDir(ctx, true)
}

func (t *TranscriptObjectStore) syncLiveDir(ctx context.Context, force bool) error {
	if t == nil || t.liveDir == "" {
		return nil
	}
	entries, err := os.ReadDir(t.liveDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(entries))
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !validTranscriptRunID(e.Name()) {
			continue
		}
		runID := e.Name()
		present[runID] = true
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		stamp := transcriptFileStamp{size: info.Size(), modNano: info.ModTime().UnixNano()}
		t.mu.Lock()
		prev, seen := t.lastSynced[runID]
		t.mu.Unlock()
		if !force && seen && prev == stamp {
			continue
		}
		data, err := os.ReadFile(filepath.Join(t.liveDir, runID))
		if err != nil || len(data) == 0 {
			continue
		}
		if err := t.scoped.Put(ctx, runID+".jsonl", bytes.NewReader(data)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		t.mu.Lock()
		t.lastSynced[runID] = stamp
		t.mu.Unlock()
	}
	t.mu.Lock()
	for id := range t.lastSynced {
		if !present[id] {
			delete(t.lastSynced, id)
		}
	}
	t.mu.Unlock()
	return firstErr
}

// FlushRunEnd implements orchestrator.TranscriptSink.
func (t *TranscriptObjectStore) FlushRunEnd(ctx context.Context, runID, rawPath, narrative string) (savedNarrative bool, err error) {
	if t == nil || !validTranscriptRunID(runID) {
		return false, nil
	}
	var errs []error
	if rawPath != "" {
		if data, readErr := os.ReadFile(rawPath); readErr == nil && len(data) > 0 {
			if putErr := t.scoped.Put(ctx, runID+".jsonl", bytes.NewReader(data)); putErr != nil {
				errs = append(errs, putErr)
			}
		}
		t.mu.Lock()
		delete(t.lastSynced, runID)
		t.mu.Unlock()
	}
	if narrative != "" {
		if putErr := t.cache.PutBytes(ctx, runID+".md", []byte(narrative)); putErr != nil {
			errs = append(errs, putErr)
		} else {
			savedNarrative = true
		}
	}
	return savedNarrative, errors.Join(errs...)
}

// RestoreRaw implements orchestrator.TranscriptSink.
func (t *TranscriptObjectStore) RestoreRaw(ctx context.Context, runID, destPath string) error {
	if t == nil || !validTranscriptRunID(runID) || destPath == "" {
		return nil
	}
	rc, _, err := t.scoped.Get(ctx, runID+".jsonl")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer rc.Close()

	parent := filepath.Dir(destPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".tmp-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, destPath)
}

// TailRemoteRaw renders a live transcript from <runID>.jsonl in the object store.
func (t *TranscriptObjectStore) TailRemoteRaw(ctx context.Context, runID string) (string, bool, error) {
	if t == nil || !validTranscriptRunID(runID) {
		return "", false, nil
	}
	rc, _, err := t.scoped.Get(ctx, runID+".jsonl")
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	raw, readErr := io.ReadAll(rc)
	rc.Close()
	if readErr != nil {
		return "", false, readErr
	}
	rendered := renderRawTranscript(string(raw))
	if rendered != "" && t.redact != nil {
		rendered = t.redact(rendered)
	}
	return rendered, rendered != "", nil
}

// Read implements ui.ArchivedTranscript, reading <runID>.md through the local
// read-through cache and falling back to rendering <runID>.jsonl when a run was
// interrupted before writing its final narrative.
func (t *TranscriptObjectStore) Read(ctx context.Context, runID string) (string, bool, error) {
	if t == nil || !validTranscriptRunID(runID) {
		return "", false, nil
	}
	data, err := t.cache.GetBytes(ctx, runID+".md")
	if err == nil {
		return string(data), len(data) > 0, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}

	// Fallback for crashed / preempted runs that flushed <runID>.jsonl
	// periodically or on drain without reaching FlushRunEnd's .md write.
	rendered, ok, rawErr := t.TailRemoteRaw(ctx, runID)
	if rawErr != nil || !ok {
		return "", false, rawErr
	}
	_ = t.cache.CacheLocalBytes(runID+".md", []byte(rendered))
	return rendered, true, nil
}

// Sizes implements ui.ArchivedTranscriptSizes.
func (t *TranscriptObjectStore) Sizes(ctx context.Context) ([]model.RunTranscript, error) {
	if t == nil {
		return nil, nil
	}
	items, err := t.scoped.List(ctx, "")
	if err != nil {
		return nil, err
	}
	mdSizes := make(map[string]int)
	rawSizes := make(map[string]int)
	for _, it := range items {
		switch {
		case strings.HasSuffix(it.Key, ".md"):
			id := strings.TrimSuffix(it.Key, ".md")
			if validTranscriptRunID(id) && it.Size > 0 {
				mdSizes[id] = int(it.Size)
			}
		case strings.HasSuffix(it.Key, ".jsonl"):
			id := strings.TrimSuffix(it.Key, ".jsonl")
			if validTranscriptRunID(id) && it.Size > 0 {
				rawSizes[id] = int(it.Size)
			}
		}
	}
	for id, sz := range rawSizes {
		if _, hasMD := mdSizes[id]; !hasMD {
			mdSizes[id] = sz
		}
	}
	out := make([]model.RunTranscript, 0, len(mdSizes))
	for id, sz := range mdSizes {
		out = append(out, model.RunTranscript{RunID: id, Bytes: sz})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}

// CheckObjectStoreURI validates cfg.ObjectStoreURI when set.
func CheckObjectStoreURI(cfg Config) error {
	if cfg.ObjectStoreURI == "" {
		return nil
	}
	if err := objectstore.ValidateURI(cfg.ObjectStoreURI); err != nil {
		return fmt.Errorf("-object-store: %w", err)
	}
	return nil
}
