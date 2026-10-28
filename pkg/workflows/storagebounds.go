package workflows

import (
	"fmt"
	"time"
)

// it is positive.
func (b StorageBounds) Resolved() StorageBounds {
	return StorageBounds{Cache: b.Cache.Resolved(), Artifacts: b.Artifacts.Resolved()}
}

// Resolved fills in the cache's defaults for anything left unset -- or
// set to something a bound cannot be, which reads as unset here rather
// than as a directory nothing sweeps (StorageBoundsFrom's own note).
func (b CacheBounds) Resolved() CacheBounds {
	if b.MaxAge <= 0 {
		b.MaxAge = DefaultCacheMaxAge
	}
	if b.MaxBytes <= 0 {
		b.MaxBytes = DefaultCacheMaxBytes
	}
	if b.MaxEntries <= 0 {
		b.MaxEntries = DefaultCacheMaxEntries
	}
	if b.MaxEntryBytes <= 0 {
		b.MaxEntryBytes = DefaultCacheMaxEntryBytes
	}
	return b
}

// Resolved is CacheBounds.Resolved for the artifact store, plus the one
// correction a pair of bounds needs: a per-run ceiling below the
// per-artifact one is a wiring mistake rather than a policy, and honouring
// it would mean a store that declines the first artifact it is ever
// offered.
func (b ArtifactBounds) Resolved() ArtifactBounds {
	if b.MaxAge <= 0 {
		b.MaxAge = DefaultArtifactMaxAge
	}
	if b.MaxBytes <= 0 {
		b.MaxBytes = DefaultArtifactMaxBytes
	}
	if b.MaxRunBytes <= 0 {
		b.MaxRunBytes = DefaultArtifactMaxRunBytes
	}
	if b.MaxArtifactBytes <= 0 {
		b.MaxArtifactBytes = DefaultArtifactMaxArtifactBytes
	}
	if b.MaxRunBytes < b.MaxArtifactBytes {
		b.MaxRunBytes = b.MaxArtifactBytes
	}
	return b
}

// StorageBounds is what grain CI may occupy on the disk it is installed
// on: the job cache's bounds (DirCache) and the collected artifacts'
// (DirArtifacts), as one value.
//
// One value, and one provider function behind it, because the two
// directories are one decision about one volume -- an operator looking
// at the System pane's Storage section is reading both bars against the
// same free space, and moving either ceiling is the same act. It is also
// the whole of the mechanism that makes a moved ceiling take effect
// without a restart (grain/task-317): DirCacheOptions.Bounds and
// DirArtifactsOptions.Bounds take a `func() StorageBounds` and each
// store reads its own half of it every time it needs a bound, so a
// daemon hands the same closure to both and neither holds a number that
// has since been edited.
//
// Nothing in here can change an answer. A cache key is a promise that
// the work is identical, so there is no entry these numbers make wrong,
// and an artifact is evidence about a failure whose verdict is in the
// run either way (DirCache and DirArtifacts each say it at length). Every
// bound here trades disk against how often a job is re-run, or how often
// somebody has to reproduce a failure by hand.
type StorageBounds struct {
	Cache     CacheBounds
	Artifacts ArtifactBounds
}

// CacheBounds is what the CI job cache may hold. Every field has
// DirCache's own three readings: a positive number is the bound and zero
// is grain's own default (the Default* constants in dircache.go). Unlike
// StoredRunRetention's, a negative number is not "no bound at all" --
// see StorageBoundsFrom for why a directory on the data disk has no
// unbounded reading -- and is treated as a zero wherever one arrives.
type CacheBounds struct {
	// MaxAge is how long an unasked-for entry is kept.
	MaxAge time.Duration
	// MaxBytes and MaxEntries are the ceilings on the whole directory:
	// the oldest entries are swept until it is under both.
	MaxBytes   int64
	MaxEntries int
	// MaxEntryBytes is the largest single result worth keeping, and the
	// one bound in either group with no stored setting behind it: it is
	// about what one cache entry is worth rather than about the size of
	// the volume, and the Storage section shows no reading against it.
	// cmd/grain keeps it on -ci-cache-max-entry-bytes for that reason.
	MaxEntryBytes int64
}

// ArtifactBounds is what the collected CI artifacts may occupy, with
// CacheBounds' readings and one bound a cache has no equivalent of
// (MaxRunBytes).
type ArtifactBounds struct {
	// MaxAge is how long a run's artifacts are kept.
	MaxAge time.Duration
	// MaxBytes is the ceiling on the whole directory; the oldest runs'
	// artifacts are swept whole until it fits.
	MaxBytes int64
	// MaxRunBytes is the ceiling on one run's worth -- what a single push
	// of a workflow whose jobs all fail and all keep a trace can do to
	// the volume. A value below MaxArtifactBytes would mean a store that
	// refuses the first artifact it is offered, so Resolved raises it to
	// match rather than honouring it.
	MaxRunBytes int64
	// MaxArtifactBytes is the largest single artifact worth keeping, and
	// the number a job's sandbox is told before it sends anything, so
	// that a file which would be refused here is never copied out
	// (DirArtifacts.MaxArtifactBytes).
	MaxArtifactBytes int64
}

// DefaultStorageBounds is what a deployment that has chosen nothing runs
// with: grain's own numbers, resolved.
func DefaultStorageBounds() StorageBounds { return StorageBounds{}.Resolved() }

// The defaults, all of them about disk rather than about correctness.
const (
	// DefaultCacheMaxAge is how long an unasked-for entry is kept.
	DefaultCacheMaxAge = 30 * 24 * time.Hour
	// DefaultCacheMaxBytes is the ceiling on the whole directory.
	DefaultCacheMaxBytes int64 = 256 << 20
	// DefaultCacheMaxEntryBytes is the largest single result worth
	// keeping. A JobRun's steps are each bounded at MaxStepOutput
	// (256KB), so this is a job of a dozen or so maximally noisy steps:
	// past that, the entry costs more disk than the run it saves.
	DefaultCacheMaxEntryBytes int64 = 4 << 20
	// DefaultCacheMaxEntries bounds the file count as well as the byte
	// count, because the entries are one flat directory and a hit is one
	// open() in it. Small entries would otherwise reach six figures long
	// before they reached DefaultCacheMaxBytes.
	DefaultCacheMaxEntries = 4096
)

// The defaults, all of them about disk.
const (
	// DefaultArtifactMaxAge is how long a run's artifacts are kept. Much
	// shorter than DefaultCacheMaxAge, because the two answer different
	// questions: a cache entry a month old still saves a re-run, while an
	// artifact is evidence about a failure somebody is investigating now.
	// A fortnight covers a task that sat in the queue over a holiday and
	// nothing beyond that.
	DefaultArtifactMaxAge = 14 * 24 * time.Hour
	// DefaultArtifactMaxBytes is the ceiling on the whole directory.
	DefaultArtifactMaxBytes int64 = 2 << 30
	// DefaultArtifactMaxRunBytes is the ceiling on one run's worth.
	DefaultArtifactMaxRunBytes int64 = 128 << 20
	// DefaultArtifactMaxArtifactBytes is the largest single artifact
	// worth keeping, and the number a job's sandbox is told before it
	// sends anything. Generous enough for the case this was built for --
	// a Playwright run of a failing spec writes a trace bundle of a few
	// megabytes per failure -- and small enough that one job cannot hand
	// the daemon a heap it has to hold while it writes it down.
	DefaultArtifactMaxArtifactBytes int64 = 32 << 20
)

// HumanBytes is a size as a person reads one. Sizes here span kilobytes
// to gigabytes and every one of them appears in a sentence somebody has
// to act on ("280MB is over the 32MB an artifact may be"), which a raw
// byte count is no use for. Shared with the run store's own sweep
// (runstore_stored.go), which reports the same kind of number for the
// same kind of reason.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
