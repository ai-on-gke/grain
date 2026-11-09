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
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
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
	var (
		rendered string
		ok       bool
	)
	switch transcriptFramework(string(data)) {
	case model.AgentFrameworkClaude:
		rendered, ok, err = l.claude.Tail(runID)
	case model.AgentFrameworkCodex:
		rendered, ok, err = l.codex.Tail(runID)
	default:
		rendered, ok, err = l.antigravity.Tail(runID)
	}
	if err != nil || !ok {
		return rendered, ok, err
	}
	if l.remote != nil {
		r, redactErr := l.remote.resolveRedactor()
		if redactErr != nil {
			return "", false, redactErr
		}
		rendered, _ = r.Text(rendered)
	}
	return rendered, rendered != "", nil
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

// TranscriptObjectStore flushes redacted live run event streams (<runID>.jsonl)
// and finished redacted narrative transcripts (<runID>.md) to an
// objectstore.Store under "transcripts/", serving reads through a local
// read-through cache.
type TranscriptObjectStore struct {
	scoped   objectstore.Store
	cache    *objectstore.ReadThroughCache
	liveDir  string
	redactor func() (*redact.Redactor, error)
	// unflushedDir holds the raw streams of finished runs whose run-end
	// upload failed (FlushRunEnd), under their run IDs, until the periodic
	// sync gets them into the object store. The orchestrator removes a
	// run's live file as soon as FlushRunEnd returns, so without this copy
	// a failed upload would lose the stream for good.
	unflushedDir string

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
//
// When redactor is non-nil, it is called before any object-store write or
// remote tail read and fails closed if it returns an error; when nil (in unit
// tests without a secret store), shape-based credential redaction still runs.
func NewTranscriptObjectStore(baseStore objectstore.Store, cacheDir, liveDir string, redactor func() (*redact.Redactor, error)) (*TranscriptObjectStore, error) {
	if baseStore == nil {
		return nil, errors.New("daemon: transcript object store is nil")
	}
	scoped := objectstore.WithPrefix(baseStore, "transcripts")
	cache, err := objectstore.NewReadThroughCache(scoped, cacheDir)
	if err != nil {
		return nil, err
	}
	t := &TranscriptObjectStore{
		scoped:     scoped,
		cache:      cache,
		liveDir:    liveDir,
		redactor:   redactor,
		lastSynced: make(map[string]transcriptFileStamp),
	}
	if liveDir != "" {
		t.unflushedDir = filepath.Clean(liveDir) + ".unflushed"
	}
	return t, nil
}

func (t *TranscriptObjectStore) resolveRedactor() (*redact.Redactor, error) {
	if t == nil || t.redactor == nil {
		return nil, nil
	}
	return t.redactor()
}

func transcriptObjectRedactor(cfg Config, secretStore *secrets.Store) func() (*redact.Redactor, error) {
	secretsDir := SecretsDir(cfg.DataDir)
	return func() (*redact.Redactor, error) {
		var known []redact.KnownValue
		if secretStore != nil {
			vals, err := KnownSecretValuesFromStore(context.Background(), secretStore)
			if err != nil {
				return nil, fmt.Errorf("reading secret store for transcript redaction: %w", err)
			}
			known = append(known, vals...)
		} else if secretsDir != "" {
			if _, statErr := os.Stat(filepath.Join(secretsDir, secrets.DefaultKeyFileName)); statErr == nil {
				vals, err := KnownSecretValues(secretsDir)
				if err != nil {
					return nil, fmt.Errorf("reading secrets directory for transcript redaction: %w", err)
				}
				known = append(known, vals...)
			}
		}
		if secretsDir != "" {
			known = append(known, knownGitHubTokens(secretsDir)...)
		}
		if len(cfg.HostSandboxEnv) > 0 {
			env, _, err := ResolveHostSandboxEnv(cfg.HostSandboxEnv, os.LookupEnv)
			if err != nil {
				return nil, fmt.Errorf("resolving host sandbox env for transcript redaction: %w", err)
			}
			for _, kv := range env {
				if name, val, ok := strings.Cut(kv, "="); ok {
					known = append(known, redact.KnownValue{Label: "sandbox env " + name, Value: val})
				}
			}
		}
		return redact.New(known), nil
	}
}

func redactJSONL(r *redact.Redactor, raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	lines := strings.Split(string(raw), "\n")
	anyChanged := false
	for i, line := range lines {
		if line == "" {
			continue
		}
		redactedLine, changed := redactJSONLLine(r, line)
		if changed {
			lines[i] = redactedLine
			anyChanged = true
		}
	}
	if !anyChanged {
		return raw
	}
	return []byte(strings.Join(lines, "\n"))
}

func redactJSONLLine(r *redact.Redactor, line string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil || dec.More() {
		out, n := r.Text(line)
		return out, n > 0
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		out, n := r.Text(line)
		return out, n > 0
	}
	redactedVal, changed := redactJSONValue(r, val)
	if !changed {
		return line, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactedVal); err != nil {
		out, _ := r.Text(line)
		return out, true
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}

func redactJSONValue(r *redact.Redactor, v any) (any, bool) {
	switch x := v.(type) {
	case string:
		out, n := r.Text(x)
		return out, n > 0
	case []any:
		changed := false
		out := make([]any, len(x))
		for i, elem := range x {
			re, c := redactJSONValue(r, elem)
			out[i] = re
			if c {
				changed = true
			}
		}
		return out, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(x))
		for k, elem := range x {
			rk, kc := r.Text(k)
			re, vc := redactJSONValue(r, elem)
			out[rk] = re
			if kc > 0 || vc {
				changed = true
			}
		}
		return out, changed
	default:
		return v, false
	}
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
// changed since the previous sync, and retries the raw stream of every
// finished run whose run-end upload failed.
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
	var (
		firstErr       error
		redactorLoaded bool
		redactor       *redact.Redactor
	)
	loadRedactor := func() (*redact.Redactor, error) {
		if !redactorLoaded {
			r, err := t.resolveRedactor()
			if err != nil {
				return nil, err
			}
			redactor = r
			redactorLoaded = true
		}
		return redactor, nil
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !validTranscriptRunID(e.Name()) {
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
		r, redactErr := loadRedactor()
		if redactErr != nil {
			return redactErr
		}
		redactedData := redactJSONL(r, data)
		if err := t.scoped.Put(ctx, runID+".jsonl", bytes.NewReader(redactedData)); err != nil {
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
	if err := t.retryUnflushed(ctx, present, loadRedactor); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// retryUnflushed uploads each finished run's raw stream and narrative
// transcript that FlushRunEnd could not, removing each local copy once it is
// in the object store. A run whose live file is back in liveDir (adopted
// again after a restart, say) is left to the live sync, and its older copy
// here dropped rather than let it overwrite the newer stream.
func (t *TranscriptObjectStore) retryUnflushed(ctx context.Context, live map[string]bool, loadRedactor func() (*redact.Redactor, error)) error {
	if t.unflushedDir == "" {
		return nil
	}
	entries, err := os.ReadDir(t.unflushedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		isNarrative := strings.HasSuffix(name, ".md")
		runID := strings.TrimSuffix(name, ".md")
		if !validTranscriptRunID(runID) {
			continue
		}
		path := filepath.Join(t.unflushedDir, name)
		if live[runID] {
			_ = os.Remove(path)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(data) == 0 {
			_ = os.Remove(path)
			continue
		}
		r, redactErr := loadRedactor()
		if redactErr != nil {
			return redactErr
		}
		if isNarrative {
			redactedNarrative, _ := r.Text(string(data))
			if err := t.cache.PutBytes(ctx, runID+".md", []byte(redactedNarrative)); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			log.Printf("grain daemon: uploaded run %s's narrative transcript to the object store on retry", runID)
			continue
		}
		if err := t.scoped.Put(ctx, runID+".jsonl", bytes.NewReader(redactJSONL(r, data))); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
		log.Printf("grain daemon: uploaded run %s's raw transcript stream to the object store on retry", runID)
	}
	return firstErr
}

// keepUnflushed keeps a copy of name (runID for a raw stream, or runID+".md"
// for a narrative) for retryUnflushed when a run-end upload failed. Written
// to a temporary name and renamed, so a retry never reads half a copy.
func (t *TranscriptObjectStore) keepUnflushed(name string, data []byte) error {
	if t.unflushedDir == "" {
		return errors.New("no directory to keep it in for a retry")
	}
	if err := os.MkdirAll(t.unflushedDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(t.unflushedDir, ".tmp-"+name+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(t.unflushedDir, name))
}

// FlushRunEnd implements orchestrator.TranscriptSink.
func (t *TranscriptObjectStore) FlushRunEnd(ctx context.Context, runID, rawPath, narrative string) (savedNarrative bool, err error) {
	if t == nil || !validTranscriptRunID(runID) {
		return false, nil
	}
	var raw []byte
	if rawPath != "" {
		if linfo, statErr := os.Lstat(rawPath); statErr == nil && linfo.Mode().IsRegular() {
			if data, readErr := os.ReadFile(rawPath); readErr == nil && len(data) > 0 {
				raw = data
			}
		}
		t.mu.Lock()
		delete(t.lastSynced, runID)
		t.mu.Unlock()
	}
	// The caller removes rawPath once this returns, and never writes narrative
	// to SQLite before secret-store redaction runs, so any raw stream or
	// narrative that cannot be redacted and uploaded now is kept in
	// unflushedDir for the periodic sync to retry (retryUnflushed).
	keepRaw := func(cause error) error {
		if raw == nil {
			return cause
		}
		if err := t.keepUnflushed(runID, raw); err != nil {
			return errors.Join(cause, fmt.Errorf("keeping run %s's raw transcript stream for a retry: %w", runID, err))
		}
		return fmt.Errorf("%w (raw transcript stream kept for a retry)", cause)
	}
	keepNarrative := func(cause error) error {
		if narrative == "" || t.unflushedDir == "" {
			return cause
		}
		if err := t.keepUnflushed(runID+".md", []byte(narrative)); err != nil {
			return errors.Join(cause, fmt.Errorf("keeping run %s's narrative transcript for a retry: %w", runID, err))
		}
		return fmt.Errorf("%w (narrative transcript kept for a retry)", cause)
	}
	r, redactErr := t.resolveRedactor()
	if redactErr != nil {
		err := keepRaw(redactErr)
		if narrative != "" && t.unflushedDir != "" {
			if nErr := t.keepUnflushed(runID+".md", []byte(narrative)); nErr != nil {
				err = errors.Join(err, fmt.Errorf("keeping run %s's narrative transcript for a retry: %w", runID, nErr))
			}
		}
		return false, err
	}
	var errs []error
	if raw != nil {
		if putErr := t.scoped.Put(ctx, runID+".jsonl", bytes.NewReader(redactJSONL(r, raw))); putErr != nil {
			errs = append(errs, keepRaw(putErr))
		} else if t.unflushedDir != "" {
			_ = os.Remove(filepath.Join(t.unflushedDir, runID))
		}
	}
	if narrative != "" {
		redactedNarrative, _ := r.Text(narrative)
		if putErr := t.cache.PutBytes(ctx, runID+".md", []byte(redactedNarrative)); putErr != nil {
			errs = append(errs, keepNarrative(putErr))
		} else {
			savedNarrative = true
			if t.unflushedDir != "" {
				_ = os.Remove(filepath.Join(t.unflushedDir, runID+".md"))
			}
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
	r, redactErr := t.resolveRedactor()
	if redactErr != nil {
		return "", false, redactErr
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
	if rendered != "" {
		rendered, _ = r.Text(rendered)
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
