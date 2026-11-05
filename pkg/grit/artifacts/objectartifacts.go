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

package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// ObjectArtifacts is an ArtifactStore backed by an objectstore.Store (for
// example gs://<bucket>/instances/<name>/ci-artifacts/) with a bounded local
// directory serving as a read-through cache.
//
// Save writes artifacts to both the backing object store and the local cache
// directory. Open serves local cache hits directly from disk and populates the
// local cache atomically on misses (such as after an instance VM is replaced).
// Clear reclaims local cache disk space without deleting durable objects from
// the backing object store.
type ObjectArtifacts struct {
	store objectstore.Store
	cache *DirArtifacts
}

// OpenObjectArtifacts opens cacheDir as a bounded local read-through cache in
// front of store.
func OpenObjectArtifacts(store objectstore.Store, cacheDir string, opts DirArtifactsOptions) (*ObjectArtifacts, error) {
	if store == nil {
		return nil, errors.New("ci: object artifact store is nil")
	}
	cache, err := OpenDirArtifacts(cacheDir, opts)
	if err != nil {
		return nil, err
	}
	return &ObjectArtifacts{
		store: store,
		cache: cache,
	}, nil
}

// Dir returns the local read-through cache directory.
func (a *ObjectArtifacts) Dir() string { return a.cache.Dir() }

// Store returns the backing objectstore.Store.
func (a *ObjectArtifacts) Store() objectstore.Store { return a.store }

// MaxArtifactBytes implements workflows.ArtifactStore.
func (a *ObjectArtifacts) MaxArtifactBytes() int64 { return a.cache.MaxArtifactBytes() }

func artifactObjectKey(ref workflows.ArtifactRef) string {
	return ref.Run + "/" + artifactFileName(ref)
}

func parseArtifactObjectKey(key string) (runID, job, name string, ok bool) {
	runID, file, found := strings.Cut(key, "/")
	if !found || strings.Contains(file, "/") {
		return "", "", "", false
	}
	if !workflows.ArtifactRunPattern.MatchString(runID) {
		return "", "", "", false
	}
	job, name, ok = parseArtifactFileName(file)
	if !ok {
		return "", "", "", false
	}
	return runID, job, name, true
}

// Save implements workflows.ArtifactStore.
func (a *ObjectArtifacts) Save(ctx context.Context, ref workflows.ArtifactRef, body []byte) (workflows.Artifact, error) {
	if err := ref.Validate(); err != nil {
		return workflows.Artifact{}, err
	}
	now := a.cache.clock()
	record := workflows.Artifact{Run: ref.Run, Job: ref.Job, Name: ref.Name}
	bounds := a.cache.bounds()
	if int64(len(body)) > bounds.MaxArtifactBytes {
		record.Note = workflows.ArtifactNote("%s is over the %s a single artifact may be, so it was not kept",
			workflows.HumanBytes(int64(len(body))), workflows.HumanBytes(bounds.MaxArtifactBytes))
		a.cache.logf("ci: artifacts: %s: %s", ref, record.Note)
		return record, nil
	}

	targetKey := artifactObjectKey(ref)
	held, err := a.remoteRunBytes(ctx, ref.Run, targetKey)
	if err != nil {
		return workflows.Artifact{}, fmt.Errorf("ci: checking %s's stored artifacts: %w", ref.Run, err)
	}
	if held+int64(len(body)) > bounds.MaxRunBytes {
		record.Note = workflows.ArtifactNote("run %s has already kept %s of artifacts, which is the %s one run may have, so this one was not kept",
			ref.Run, workflows.HumanBytes(held), workflows.HumanBytes(bounds.MaxRunBytes))
		a.cache.logf("ci: artifacts: %s: %s", ref, record.Note)
		return record, nil
	}

	if err := a.store.Put(ctx, targetKey, bytes.NewReader(body)); err != nil {
		return workflows.Artifact{}, fmt.Errorf("ci: uploading artifact %s: %w", ref, err)
	}

	// Best-effort populate the local read-through cache so immediate downloads
	// never need a round trip to the object store.
	runDir := filepath.Join(a.cache.dir, ref.Run)
	if mkErr := os.MkdirAll(runDir, 0o700); mkErr == nil {
		path := filepath.Join(runDir, artifactFileName(ref))
		if writeErr := a.cache.writeFile(runDir, path, body); writeErr != nil {
			a.cache.logf("ci: artifacts: caching %s locally: %v", ref, writeErr)
		} else {
			_ = os.Chtimes(path, now, now)
		}
		a.cache.maybeSweep()
	}

	record.Stored = true
	record.Bytes = int64(len(body))
	record.CollectedAt = &now
	return record, nil
}

func (a *ObjectArtifacts) remoteRunBytes(ctx context.Context, runID, excludeKey string) (int64, error) {
	items, err := a.store.List(ctx, runID+"/")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, it := range items {
		if it.Key == excludeKey {
			continue
		}
		if _, _, _, ok := parseArtifactObjectKey(it.Key); !ok {
			continue
		}
		total += it.Size
	}
	return total, nil
}

// List implements workflows.ArtifactStore.
func (a *ObjectArtifacts) List(ctx context.Context, runID string) ([]workflows.Artifact, error) {
	if !workflows.ArtifactRunPattern.MatchString(runID) {
		return nil, fmt.Errorf("ci: %q is not a run id an artifact can be filed under", runID)
	}
	now := a.cache.clock()
	bounds := a.cache.bounds()
	items, err := a.store.List(ctx, runID+"/")
	if err != nil {
		return nil, fmt.Errorf("ci: listing artifacts for %s: %w", runID, err)
	}
	var out []workflows.Artifact
	for _, it := range items {
		_, job, name, ok := parseArtifactObjectKey(it.Key)
		if !ok {
			continue
		}
		if bounds.MaxAge > 0 && !it.Updated.IsZero() && now.Sub(it.Updated) > bounds.MaxAge {
			continue
		}
		ts := it.Updated
		var collected *time.Time
		if !ts.IsZero() {
			collected = &ts
		}
		out = append(out, workflows.Artifact{
			Run:         runID,
			Job:         job,
			Name:        name,
			Stored:      true,
			Bytes:       it.Size,
			CollectedAt: collected,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Job != out[j].Job {
			return out[i].Job < out[j].Job
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Open implements workflows.ArtifactStore, serving from the local read-through
// cache when present and fetching from the backing object store on misses.
func (a *ObjectArtifacts) Open(ctx context.Context, ref workflows.ArtifactRef) (io.ReadCloser, workflows.Artifact, error) {
	if err := ref.Validate(); err != nil {
		return nil, workflows.Artifact{}, err
	}
	now := a.cache.clock()
	bounds := a.cache.bounds()

	// Fast path: check local read-through cache first.
	localPath := filepath.Join(a.cache.dir, ref.Run, artifactFileName(ref))
	if f, err := os.Open(localPath); err == nil {
		if info, statErr := f.Stat(); statErr == nil && !info.IsDir() {
			mod := info.ModTime()
			if bounds.MaxAge <= 0 || now.Sub(mod) <= bounds.MaxAge {
				return f, workflows.Artifact{
					Run:         ref.Run,
					Job:         ref.Job,
					Name:        ref.Name,
					Stored:      true,
					Bytes:       info.Size(),
					CollectedAt: &mod,
				}, nil
			}
		}
		f.Close()
	}

	rc, info, err := a.store.Get(ctx, artifactObjectKey(ref))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: no artifact %s: it was never kept, or it has been swept (artifacts are kept for %s): %w",
			ref, bounds.MaxAge, fs.ErrNotExist)
	}
	if err != nil {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: reading artifact %s: %w", ref, err)
	}
	defer rc.Close()

	if bounds.MaxAge > 0 && !info.Updated.IsZero() && now.Sub(info.Updated) > bounds.MaxAge {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: no artifact %s: it was never kept, or it has been swept (artifacts are kept for %s): %w",
			ref, bounds.MaxAge, fs.ErrNotExist)
	}

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: downloading artifact %s: %w", ref, err)
	}
	collected := info.Updated
	if collected.IsZero() {
		collected = now
	}

	runDir := filepath.Join(a.cache.dir, ref.Run)
	if mkErr := os.MkdirAll(runDir, 0o700); mkErr == nil {
		if writeErr := a.cache.writeFile(runDir, localPath, data); writeErr == nil {
			_ = os.Chtimes(localPath, collected, collected)
			a.cache.maybeSweep()
			if f, openErr := os.Open(localPath); openErr == nil {
				return f, workflows.Artifact{
					Run:         ref.Run,
					Job:         ref.Job,
					Name:        ref.Name,
					Stored:      true,
					Bytes:       int64(len(data)),
					CollectedAt: &collected,
				}, nil
			}
		}
	}

	return io.NopCloser(bytes.NewReader(data)), workflows.Artifact{
		Run:         ref.Run,
		Job:         ref.Job,
		Name:        ref.Name,
		Stored:      true,
		Bytes:       int64(len(data)),
		CollectedAt: &collected,
	}, nil
}

// Stats implements workflows.InspectableArtifacts against the backing object store.
func (a *ObjectArtifacts) Stats(ctx context.Context) (workflows.ArtifactStats, error) {
	bounds := a.cache.bounds()
	now := a.cache.clock()
	stats := workflows.ArtifactStats{
		Dir:              a.cache.dir,
		MaxAge:           bounds.MaxAge,
		MaxBytes:         bounds.MaxBytes,
		MaxRunBytes:      bounds.MaxRunBytes,
		MaxArtifactBytes: bounds.MaxArtifactBytes,
	}
	items, err := a.store.List(ctx, "")
	if err != nil {
		return workflows.ArtifactStats{}, fmt.Errorf("ci: listing object artifacts: %w", err)
	}
	runsSeen := make(map[string]struct{})
	for _, it := range items {
		runID, _, _, ok := parseArtifactObjectKey(it.Key)
		if !ok {
			continue
		}
		if bounds.MaxAge > 0 && !it.Updated.IsZero() && now.Sub(it.Updated) > bounds.MaxAge {
			continue
		}
		runsSeen[runID] = struct{}{}
		stats.Artifacts++
		stats.Bytes += it.Size
		if !it.Updated.IsZero() {
			if stats.Oldest.IsZero() || it.Updated.Before(stats.Oldest) {
				stats.Oldest = it.Updated
			}
			if it.Updated.After(stats.Newest) {
				stats.Newest = it.Updated
			}
		}
	}
	stats.Runs = len(runsSeen)
	return stats, nil
}

// Clear implements workflows.ClearableArtifacts by emptying the local
// read-through cache directory to reclaim VM scratch disk without deleting
// durable artifacts from the backing object store.
func (a *ObjectArtifacts) Clear(ctx context.Context) (int, int64, error) {
	return a.cache.Clear(ctx)
}

// FlushLocal uploads any artifact present in the local cache directory that is
// missing from the backing object store.
func (a *ObjectArtifacts) FlushLocal(ctx context.Context) error {
	runs, err := os.ReadDir(a.cache.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ci: reading local artifact cache %s: %w", a.cache.dir, err)
	}
	for _, run := range runs {
		if !run.IsDir() || !workflows.ArtifactRunPattern.MatchString(run.Name()) {
			continue
		}
		runDir := filepath.Join(a.cache.dir, run.Name())
		entries, err := os.ReadDir(runDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			job, name, ok := parseArtifactFileName(e.Name())
			if !ok {
				continue
			}
			ref := workflows.ArtifactRef{Run: run.Name(), Job: job, Name: name}
			key := artifactObjectKey(ref)
			if _, statErr := a.store.Stat(ctx, key); statErr == nil {
				continue
			}
			f, err := os.Open(filepath.Join(runDir, e.Name()))
			if err != nil {
				continue
			}
			putErr := a.store.Put(ctx, key, f)
			f.Close()
			if putErr != nil {
				return fmt.Errorf("ci: flushing artifact %s: %w", ref, putErr)
			}
		}
	}
	return nil
}
