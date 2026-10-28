package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// DirArtifacts is the ArtifactStore a deployment gets: one directory per
// run, one file per artifact under it.
//
// # Why a directory rather than a table in model.Store
//
// DirCache's own reason, only more so. Everything in model.Store is
// exported to the state repository as text and committed (pkg/staterepo);
// a Playwright trace bundle is several megabytes of binary, per failing
// job, and git never forgets a version of anything it has been shown.
// These files are disposable by definition -- losing one costs a re-run
// of the job that made it -- and a host restored from the state
// repository wants its tasks back, not last month's traces. It also
// keeps the whole of this out of the schema: no table, no
// SchemaVersion bump, no migration for a deployment that turns CI on
// later.
//
// # A directory per run
//
// Because a run is the unit a reader asks about ("what did run 9f2c1a
// leave behind"), the unit a per-run bound is counted over, and the unit
// eviction removes: when this store has to make room it drops whole
// runs, oldest first, rather than leaving a run with two of its three
// artifacts and no way to tell which one went. One readdir lists a run,
// one RemoveAll forgets it.
//
// # What bounds it
//
// Four bounds, three of them DirCache's:
//
//   - Age. Nothing is served or kept past MaxAge. Unlike a cache entry,
//     which is refused when it is stale, this is purely about disk: an
//     artifact does not go wrong with time, it stops being something
//     anybody is going to open.
//   - Size, of the whole directory (MaxBytes) and of a single artifact
//     (MaxArtifactBytes, which is also what the collector tells the
//     sandbox so an oversized one is never copied out at all).
//   - Size per run (MaxRunBytes), which a cache has no equivalent of and
//     this needs: a workflow of a dozen jobs that each fail and each
//     keep a trace is one commit's worth of CI, and the bound that
//     matters to whoever is watching the data disk is what one push can
//     do to it.
//
// The bounds are read through a provider rather than held, exactly as
// DirCache's are and from the same closure (StorageBounds), so a ceiling
// moved in the Settings pane binds the next sweep rather than the next
// restart (grain/task-317).
//
// Nothing here can fail a job. Save records what it declined, Open says
// "gone" for anything it cannot serve, and an error means the directory
// itself has stopped answering.
type DirArtifacts struct {
	dir string
	// bounds is what this store may occupy, asked afresh every time a
	// bound is needed rather than resolved once -- see
	// DirArtifactsOptions.Bounds, and DirCache, which reads its own half
	// of the same provider. Never nil: OpenDirArtifacts wraps a fixed
	// ArtifactBounds in a func when no provider was given.
	bounds func() workflows.ArtifactBounds
	now    func() time.Time
	log    func(format string, args ...any)

	mu        sync.Mutex
	lastSweep time.Time
}

// The sweep's clocks and the half-written entry's prefix, the same as
// DirCache's (pkg/grit/cache) and for the same reasons, which dircache.go
// gives.
const (
	sweepInterval = 5 * time.Minute
	tempEntryTTL  = time.Hour
	tempPrefix    = ".tmp-"
)

// DirArtifactsOptions are OpenDirArtifacts' knobs; every zero value
// means the corresponding default above.
type DirArtifactsOptions struct {
	MaxAge           time.Duration
	MaxBytes         int64
	MaxRunBytes      int64
	MaxArtifactBytes int64
	// Bounds, when set, is where this store's bounds come from instead of
	// the four fields above, and it is called every time one is needed:
	// on each Save -- including the per-artifact cap a job's sandbox is
	// told before it copies anything out (MaxArtifactBytes) -- on each
	// sweep, and by Stats.
	//
	// DirCacheOptions.Bounds is the same field, taking the same whole
	// StorageBounds, so that a daemon hands one closure to both stores
	// and the two halves of one operator decision cannot be wired up
	// differently.
	Bounds func() workflows.StorageBounds
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Log, when set, is told what was declined and what was swept. nil
	// discards.
	Log func(format string, args ...any)
}

// OpenDirArtifacts prepares dir and returns a store over it, sweeping
// once on the way in -- a daemon that has been down for a month should
// not start by advertising a directory of artifacts it is about to
// delete.
//
// It returns an error only for a directory it could not make or read at
// all. A caller that gets one should log it and run with no artifact
// store: every job runs exactly as it did before this existed, and a
// failing one keeps nothing.
func OpenDirArtifacts(dir string, opts DirArtifactsOptions) (*DirArtifacts, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("ci: an artifact store needs a directory to live in")
	}
	// 0o700 for DirCache's reason: what comes out of a job's sandbox is
	// that repository's own material, and nothing but the daemon has any
	// business reading it off the host.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ci: preparing the artifact directory %s: %w", dir, err)
	}
	a := &DirArtifacts{dir: dir, now: opts.Now, log: opts.Log}
	if opts.Bounds != nil {
		a.bounds = func() workflows.ArtifactBounds { return opts.Bounds().Artifacts.Resolved() }
	} else {
		// No provider: the four fields are this store's bounds for the
		// life of the process, resolved once here -- defaults filled in,
		// and a per-run bound below the per-artifact one corrected rather
		// than honoured (ArtifactBounds.Resolved) -- so that the read path
		// is the same one either way.
		fixed := workflows.ArtifactBounds{
			MaxAge:           opts.MaxAge,
			MaxBytes:         opts.MaxBytes,
			MaxRunBytes:      opts.MaxRunBytes,
			MaxArtifactBytes: opts.MaxArtifactBytes,
		}.Resolved()
		a.bounds = func() workflows.ArtifactBounds { return fixed }
	}
	if _, err := os.ReadDir(dir); err != nil {
		return nil, fmt.Errorf("ci: reading the artifact directory %s: %w", dir, err)
	}
	a.Sweep()
	return a, nil
}

// Dir is where the artifacts live -- for an operator being told where to
// look, and for a test.
func (a *DirArtifacts) Dir() string { return a.dir }

// MaxArtifactBytes implements ArtifactStore: the bound the collector
// hands to the sandbox, so a file that would be refused here is never
// copied out of there.
func (a *DirArtifacts) MaxArtifactBytes() int64 { return a.bounds().MaxArtifactBytes }

func (a *DirArtifacts) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *DirArtifacts) logf(format string, args ...any) {
	if a.log != nil {
		a.log(format, args...)
	}
}

// Save writes body under ref and reports what it did.
//
// A body over one of the bounds is *not* an error: it comes back as an
// Artifact with Stored false and a note saying which bound it was, which
// is what a reader who goes looking for the trace needs to be told. An
// error here means this store could not be written to at all.
func (a *DirArtifacts) Save(_ context.Context, ref workflows.ArtifactRef, body []byte) (workflows.Artifact, error) {
	if err := ref.Validate(); err != nil {
		return workflows.Artifact{}, err
	}
	now := a.clock()
	record := workflows.Artifact{Run: ref.Run, Job: ref.Job, Name: ref.Name}
	// One reading for this Save, so that the per-artifact cap and the
	// per-run one below it are the pair an operator stored rather than
	// two halves of an edit that landed between them.
	bounds := a.bounds()
	if int64(len(body)) > bounds.MaxArtifactBytes {
		record.Note = workflows.ArtifactNote("%s is over the %s a single artifact may be, so it was not kept",
			workflows.HumanBytes(int64(len(body))), workflows.HumanBytes(bounds.MaxArtifactBytes))
		a.logf("ci: artifacts: %s: %s", ref, record.Note)
		return record, nil
	}

	runDir := filepath.Join(a.dir, ref.Run)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return workflows.Artifact{}, fmt.Errorf("ci: preparing %s's artifact directory: %w", ref.Run, err)
	}
	// What this run already holds, plus what is about to be added,
	// against the per-run bound. Counted rather than remembered, so a
	// daemon restarted mid-run still knows what that run has spent.
	held, _ := a.runBytes(runDir)
	if held+int64(len(body)) > bounds.MaxRunBytes {
		record.Note = workflows.ArtifactNote("run %s has already kept %s of artifacts, which is the %s one run may have, so this one was not kept",
			ref.Run, workflows.HumanBytes(held), workflows.HumanBytes(bounds.MaxRunBytes))
		a.logf("ci: artifacts: %s: %s", ref, record.Note)
		return record, nil
	}

	path := filepath.Join(runDir, artifactFileName(ref))
	if err := a.writeFile(runDir, path, body); err != nil {
		return workflows.Artifact{}, err
	}
	record.Stored = true
	record.Bytes = int64(len(body))
	record.CollectedAt = &now
	a.maybeSweep()
	return record, nil
}

// List reports what is on disk for one run, by name.
//
// On disk rather than as recorded: a run whose artifacts have been swept
// lists none, which is the answer a reader wanting to download one
// needs. Anything in the run's directory that is not an artifact this
// store wrote is ignored rather than reported.
func (a *DirArtifacts) List(_ context.Context, runID string) ([]workflows.Artifact, error) {
	if !workflows.ArtifactRunPattern.MatchString(runID) {
		return nil, fmt.Errorf("ci: %q is not a run id an artifact can be filed under", runID)
	}
	runDir := filepath.Join(a.dir, runID)
	entries, err := os.ReadDir(runDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("ci: reading %s's artifacts: %w", runID, err)
	}
	var out []workflows.Artifact
	for _, e := range entries {
		job, name, ok := parseArtifactFileName(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // it went away underneath us, so it is not held any more
		}
		mod := info.ModTime()
		out = append(out, workflows.Artifact{
			Run: runID, Job: job, Name: name,
			Stored: true, Bytes: info.Size(), CollectedAt: &mod,
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

// Open hands back the bytes, and the record of what they are, for
// whatever is serving a download.
//
// The caller closes the reader. An artifact that is not there -- never
// kept, or swept since -- is fs.ErrNotExist wrapped in a sentence saying
// which of the two a reader should assume, because those are the two
// things that have actually happened to somebody clicking a link.
func (a *DirArtifacts) Open(_ context.Context, ref workflows.ArtifactRef) (io.ReadCloser, workflows.Artifact, error) {
	if err := ref.Validate(); err != nil {
		return nil, workflows.Artifact{}, err
	}
	path := filepath.Join(a.dir, ref.Run, artifactFileName(ref))
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: no artifact %s: it was never kept, or it has been swept (artifacts are kept for %s): %w",
			ref, a.bounds().MaxAge, err)
	}
	if err != nil {
		return nil, workflows.Artifact{}, fmt.Errorf("ci: reading artifact %s: %w", ref, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, workflows.Artifact{}, fmt.Errorf("ci: reading artifact %s: %w", ref, err)
	}
	mod := info.ModTime()
	return f, workflows.Artifact{
		Run: ref.Run, Job: ref.Job, Name: ref.Name,
		Stored: true, Bytes: info.Size(), CollectedAt: &mod,
	}, nil
}

// Stats is what this directory is holding and what it is allowed to
// hold -- DirCache.Stats' own shape, read-only for its own reason: a
// stats call that swept as a side effect would be a pane deleting files
// every time it polled.
func (a *DirArtifacts) Stats(_ context.Context) (workflows.ArtifactStats, error) {
	// The bounds as they stand now rather than as this store was opened
	// with, for DirCache.Stats' reason: the Storage section draws its bar
	// against what it is told here.
	bounds := a.bounds()
	stats := workflows.ArtifactStats{
		Dir:              a.dir,
		MaxAge:           bounds.MaxAge,
		MaxBytes:         bounds.MaxBytes,
		MaxRunBytes:      bounds.MaxRunBytes,
		MaxArtifactBytes: bounds.MaxArtifactBytes,
	}
	runs, err := os.ReadDir(a.dir)
	if err != nil {
		return workflows.ArtifactStats{}, fmt.Errorf("ci: reading the artifact directory %s: %w", a.dir, err)
	}
	for _, run := range runs {
		if !run.IsDir() || !workflows.ArtifactRunPattern.MatchString(run.Name()) {
			continue
		}
		held := 0
		entries, err := os.ReadDir(filepath.Join(a.dir, run.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if _, _, ok := parseArtifactFileName(e.Name()); !ok {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			held++
			stats.Artifacts++
			stats.Bytes += info.Size()
			mod := info.ModTime()
			if stats.Oldest.IsZero() || mod.Before(stats.Oldest) {
				stats.Oldest = mod
			}
			if mod.After(stats.Newest) {
				stats.Newest = mod
			}
		}
		if held > 0 {
			stats.Runs++
		}
	}
	return stats, nil
}

// runBytes is what one run's directory is holding, and how many
// artifacts that is.
func (a *DirArtifacts) runBytes(runDir string) (int64, int) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return 0, 0
	}
	var (
		total int64
		count int
	)
	for _, e := range entries {
		if _, _, ok := parseArtifactFileName(e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		count++
	}
	return total, count
}

// writeFile puts body at path atomically, through a temporary file in
// the same directory: a reader either sees no artifact or the whole of
// one, never half of a tarball that gunzip will report as corrupt.
func (a *DirArtifacts) writeFile(runDir, path string, body []byte) error {
	tmp, err := os.CreateTemp(runDir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("ci: writing an artifact in %s: %w", runDir, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename below has moved it
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("ci: writing the artifact %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ci: writing the artifact %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("ci: writing the artifact %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("ci: putting the artifact at %s: %w", path, err)
	}
	return nil
}

// maybeSweep runs a sweep if one is due, so a run of a dozen failing
// jobs walks the directory once rather than a dozen times.
func (a *DirArtifacts) maybeSweep() {
	now := a.clock()
	a.mu.Lock()
	due := now.Sub(a.lastSweep) >= sweepInterval
	if due {
		a.lastSweep = now
	}
	a.mu.Unlock()
	if due {
		a.Sweep()
	}
}

// Sweep brings the directory back inside its bounds: every run past
// MaxAge goes, then the oldest runs go until what is left is under
// MaxBytes.
//
// A whole run at a time, for the reason the layout is a run per
// directory: half a run's artifacts is a reader finding one of the two
// traces they came for and having no way to know the other ever existed.
//
// Every failure here is logged and stepped over -- a sweep that cannot
// finish leaves a directory that is too big, which is a disk to watch,
// where a sweep that gave up would leave the same directory and no note
// about it.
func (a *DirArtifacts) Sweep() {
	now := a.clock()
	// One reading for the whole sweep, so that the age pass and the size
	// pass cannot disagree about what this directory is allowed to be.
	bounds := a.bounds()
	runs, err := os.ReadDir(a.dir)
	if err != nil {
		a.logf("ci: artifacts: reading %s to sweep it: %v", a.dir, err)
		return
	}

	type item struct {
		path  string
		size  int64
		newer time.Time
	}
	var (
		kept    []item
		total   int64
		removed int
	)
	for _, run := range runs {
		name := run.Name()
		path := filepath.Join(a.dir, name)
		if !run.IsDir() {
			// A stray temporary file at the top level, or somebody else's
			// file in a directory that was mistyped in the wiring. Only
			// ours is touched, and only once no live Save can own it.
			if info, err := run.Info(); err == nil && strings.HasPrefix(name, tempPrefix) &&
				now.Sub(info.ModTime()) > tempEntryTTL {
				os.Remove(path)
			}
			continue
		}
		if !workflows.ArtifactRunPattern.MatchString(name) {
			continue // not a run of ours
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		var (
			size   int64
			newest time.Time
			held   int
		)
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if strings.HasPrefix(e.Name(), tempPrefix) {
				if now.Sub(info.ModTime()) > tempEntryTTL {
					os.Remove(filepath.Join(path, e.Name()))
				}
				continue
			}
			if _, _, ok := parseArtifactFileName(e.Name()); !ok {
				continue
			}
			held++
			size += info.Size()
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		switch {
		case held == 0:
			// An empty run directory: a run whose artifacts have already
			// been swept, or one whose Save never landed. Removed once it
			// is old enough that no live Save is between MkdirAll and
			// Rename in it.
			if info, err := run.Info(); err == nil && now.Sub(info.ModTime()) > tempEntryTTL {
				os.Remove(path)
			}
		case now.Sub(newest) > bounds.MaxAge:
			if err := os.RemoveAll(path); err != nil {
				a.logf("ci: artifacts: removing %s: %v", path, err)
				continue
			}
			removed += held
		default:
			kept = append(kept, item{path, size, newest})
			total += size
		}
	}

	sort.Slice(kept, func(i, j int) bool { return kept[i].newer.Before(kept[j].newer) })
	for i := 0; i < len(kept) && total > bounds.MaxBytes; i++ {
		if err := os.RemoveAll(kept[i].path); err != nil {
			a.logf("ci: artifacts: removing %s: %v", kept[i].path, err)
			continue
		}
		total -= kept[i].size
		removed++
	}
	if removed > 0 {
		a.logf("ci: artifacts: swept %d artifact(s) from %s; %dKB left", removed, a.dir, total/1024)
	}
}

// Clear empties the store, returning how many artifacts it removed and
// how many bytes that was -- DirCache.Clear's shape, for the caller that
// is reclaiming disk and needs to say how much it got back
// (grain/task-213).
//
// A whole run at a time, and only runs of ours: the sweep's own two
// rules, for the sweep's own reasons. A directory that was mistyped in
// the wiring should not become a directory this deletes, and half a
// run's artifacts is worse than none of them.
//
// What clearing costs is stated plainly because it is not nothing, and
// it is the difference between this and the cache beside it: a cache
// entry is a promise that identical work was already done, so an empty
// cache costs re-runs, while an artifact is the only copy of what a
// failing job left behind and nothing will make it again. That is why
// the caller sweeping a filling volume reaches for this after it has
// reclaimed everything that costs nothing, and why an operator gets no
// button for it at all.
//
// A removal that fails is reported rather than swallowed, with the
// counts of what did go: a caller about to write "reclaimed 2 GiB" in a
// journal should be able to tell that from "reclaimed 2 GiB of the 6
// that are there".
func (a *DirArtifacts) Clear(_ context.Context) (int, int64, error) {
	runs, err := os.ReadDir(a.dir)
	if err != nil {
		return 0, 0, fmt.Errorf("ci: reading the artifact directory %s: %w", a.dir, err)
	}
	var (
		removed int
		freed   int64
		failed  error
	)
	for _, run := range runs {
		if !run.IsDir() || !workflows.ArtifactRunPattern.MatchString(run.Name()) {
			continue
		}
		path := filepath.Join(a.dir, run.Name())
		size, held := a.runBytes(path)
		if err := os.RemoveAll(path); err != nil {
			if failed == nil {
				failed = fmt.Errorf("ci: clearing the artifacts in %s: %w", a.dir, err)
			}
			continue
		}
		removed += held
		freed += size
	}
	a.logf("ci: artifacts: cleared %d artifact(s), %dKB, from %s", removed, freed/1024, a.dir)
	return removed, freed, failed
}

// artifactFileName is the one place a ref becomes a filename: the job,
// a dot, the name, and the extension every artifact has.
//
// The dot is the separator because a job id cannot contain one
// (JobIDPattern) and an artifact name cannot either
// (ArtifactNamePattern), so the split back is unambiguous and no
// escaping is involved anywhere.
func artifactFileName(ref workflows.ArtifactRef) string {
	return ref.Job + "." + ref.Name + workflows.ArtifactExt
}

// parseArtifactFileName is artifactFileName backwards, and is how this
// store decides which files in its own directory are its own.
func parseArtifactFileName(file string) (job, name string, ok bool) {
	base, found := strings.CutSuffix(file, workflows.ArtifactExt)
	if !found {
		return "", "", false
	}
	job, name, found = strings.Cut(base, ".")
	if !found || !workflows.JobIDPattern.MatchString(job) || !workflows.ArtifactNamePattern.MatchString(name) {
		return "", "", false
	}
	return job, name, true
}
