package ui

// What this deployment's CI occupies, for the System pane's Storage
// section: the stored step output of its run history (grain/task-290),
// and the two directories it keeps on the volume itself -- the job cache
// and the collected artifacts (grain/task-276).
//
// The four settings that bound it are editable on Settings and take
// effect without a restart (grain/task-269), but a bound with no reading
// beside it is a number nobody can act on: an operator raising or
// lowering the step-output ceiling is guessing until they can see how
// much of it is in use, and whether the failure they will want to read
// on Monday is anywhere near being swept.
//
// It is a live reading rather than a setting, so it rides the Storage
// section's own poll beside the proxy cache's figures and not the
// settings form, for exactly the reason the proxy cache's did not stay
// on that form (packagecache.go, grain/task-266): a number that arrives
// with a form is only as fresh as the last time somebody opened it, and
// two readings of one volume taken at different moments disagree.

import (
	"context"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
)

// CIHistoryStorage is the stored step output of this deployment's CI
// history, held against the bounds that decide when it goes.
//
// Runs and Bytes are what is stored now; Ceiling and AgeDays are what
// this deployment has said it will keep, resolved -- a blank setting
// reads back here as grain's own default, because that is the number in
// force, and a bound an operator has switched off reads back as zero.
type CIHistoryStorage struct {
	// Bytes is the step output stored across every run that still has
	// any, measured as the sweep charges it. Runs and Steps are how many
	// of each hold it -- a run whose output has been swept counts in
	// neither, though its record, its jobs and every verdict on them
	// stand.
	Bytes int64 `json:"bytes"`
	Runs  int   `json:"runs"`
	Steps int   `json:"steps"`
	// OldestAt is when the oldest run that still has output started, and
	// absent when nothing does. This is "will the evidence still be there
	// on Monday" as a date: everything older than it has already been
	// swept.
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	// Ceiling is the byte bound Bytes is swept back to, zero where an
	// operator has asked for no ceiling at all. AgeDays is the other
	// bound: how old output may get whatever room there is under the
	// ceiling, zero where that one is off.
	Ceiling int64 `json:"ceiling,omitempty"`
	AgeDays int   `json:"ageDays,omitempty"`
	// Runs kept is the third setting, and the one that decides how far
	// back the records go once their logs are gone -- said here because
	// an operator reading "12 runs still have output" needs to know it is
	// 12 of 5000 rather than 12 of everything.
	RunsKept int `json:"runsKept,omitempty"`
}

// ciHistoryStorageTTL is how long one measurement is served for.
//
// The measurement sums the length of every stored step's output, which
// is a read of the whole of what the ceiling bounds -- tens of megabytes
// on a default deployment. The pane it feeds polls every five seconds,
// and every operator with it open is another poller, so measuring per
// request would put that read on the host whose disk pressure is
// generally why somebody is looking. The figure itself moves only as
// runs finish and as the sweep runs, so half a minute of staleness is
// invisible where a load average's would not be (sandboxHealthTTL makes
// the same trade over a smaller number).
const ciHistoryStorageTTL = 30 * time.Second

// ciHistoryStorageCache is the last measurement and when it expires.
type ciHistoryStorageCache struct {
	mu      sync.Mutex
	storage *CIHistoryStorage
	expires time.Time
}

// ciHistoryStorage measures the stored CI step output, or hands back the
// last measurement while it is still fresh.
//
// nil on a UI with no store behind it and on a measurement that fails:
// the Storage section shows one entry fewer rather than taking the pane
// -- its disk bars, its sandbox list, its daemon health -- down with it.
// A failure to count logs is not a reason to stop reporting the machine.
func (c *Client) ciHistoryStorage(ctx context.Context) *CIHistoryStorage {
	if c.Store == nil {
		return nil
	}
	c.ciStorage.mu.Lock()
	defer c.ciStorage.mu.Unlock()
	if c.ciStorage.storage != nil && c.now().Before(c.ciStorage.expires) {
		return c.ciStorage.storage
	}
	usage, err := c.Store.CIStepOutputUsage(ctx)
	if err != nil {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil
	}
	settings := model.Config{}
	if cfg != nil {
		// nil is a deployment that has never saved its settings, whose
		// bounds are therefore grain's own -- which is what a zero
		// model.Config resolves to, so it needs no case of its own here.
		settings = *cfg
	}
	storage := ciHistoryStorageOf(usage, settings)
	c.ciStorage.storage, c.ciStorage.expires = &storage, c.now().Add(ciHistoryStorageTTL)
	return &storage
}

// CIJobCacheStorage is what grain CI's job cache holds right now, held
// against the bounds that sweep it (ci.CacheStats, narrowed to the
// question this section asks).
//
// Narrowed rather than served entire, and not simply CICacheStats over
// again: that one is the cache pane's own reading, with its directory,
// its sweep times and the span of what it holds. This is the disk
// question -- how much of the volume it is occupying, how much it may
// occupy, and what emptying it would cost -- which is the one the
// Storage section is for.
type CIJobCacheStorage struct {
	// Bytes and Entries are what is stored now, anything already past
	// MaxAgeSeconds included: an entry the next sweep will drop is still
	// occupying the disk this is being asked about.
	Bytes   int64 `json:"bytes"`
	Entries int   `json:"entries"`
	// Dir is where they live, for an operator being told where to look.
	Dir string `json:"dir,omitempty"`
	// OldestAt is the mtime of the oldest entry, absent for an empty
	// cache -- what the next sweep drops first.
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	// The bounds, so that a size means something: the ceiling on the
	// directory, the ceiling on the file count (the entries are one flat
	// directory and a hit is one open in it), and how long an unasked-for
	// entry is kept.
	MaxBytes      int64   `json:"maxBytes,omitempty"`
	MaxEntries    int     `json:"maxEntries,omitempty"`
	MaxAgeSeconds float64 `json:"maxAgeSeconds,omitempty"`
	// Hits and Misses are Lookups since this daemon started, which is
	// what clearing this costs said as a number: a cache answering four
	// jobs in five is several sandbox-minutes per run, and an operator
	// deciding whether to empty it to free 200 MB should see that beside
	// the 200 MB. A restart leaves a full cache and empty counters
	// (ci.CacheStats.Hits).
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
}

// CIArtifactStorage is what this deployment's collected CI artifacts
// occupy, against the four bounds that sweep them (ci.ArtifactStats).
//
// There is no flush here and there is not meant to be. An artifact is
// the only copy of what a run produced -- the trace, the screenshot, the
// report a job was told to keep because its log could not carry it --
// and the one thing in this deployment that drops them is the reclaim
// behind the disk floor, on a host that is already refusing work
// (ci.ClearableArtifacts, grain/task-213). So what the pane can offer is
// this reading and the sentence that goes with it, which is worth more
// than a button that would be wrong to press.
type CIArtifactStorage struct {
	// Bytes is what is stored across every run that kept anything; Runs
	// and Artifacts are how many of each hold it.
	Bytes     int64 `json:"bytes"`
	Runs      int   `json:"runs"`
	Artifacts int   `json:"artifacts"`
	// Dir is the directory on the host, for an operator being told where
	// to look -- and, since there is no button, where to go if they mean
	// to act on this by hand.
	Dir string `json:"dir,omitempty"`
	// OldestAt is the oldest artifact still kept, absent when none is.
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	// The bounds: the ceiling on the whole directory, on any one run's
	// worth, on a single artifact, and how long any of it is kept.
	MaxBytes         int64   `json:"maxBytes,omitempty"`
	MaxRunBytes      int64   `json:"maxRunBytes,omitempty"`
	MaxArtifactBytes int64   `json:"maxArtifactBytes,omitempty"`
	MaxAgeSeconds    float64 `json:"maxAgeSeconds,omitempty"`
}

// ciDiskUsageCache is the last measurement of those two directories and
// when it expires. Both under one lock and one expiry: they are read
// together, by one poll of one pane, and two timers over one volume is
// how two figures on one screen come to be from different moments.
type ciDiskUsageCache struct {
	mu        sync.Mutex
	jobCache  *CIJobCacheStorage
	artifacts *CIArtifactStorage
	measured  bool
	expires   time.Time
}

// ciDiskUsage measures what CI's own two directories occupy, or hands
// back the last measurement while it is still fresh.
//
// Either half is nil on its own terms and neither failure costs the
// other: a deployment with no CI wired has neither, one running CI with
// no job cache behind it (every job runs, every time) has artifacts and
// no cache, and a directory that has stopped answering shows one entry
// fewer rather than taking the pane's disk bars down with it. That is
// ciHistoryStorage's bargain, made twice.
func (c *Client) ciDiskUsage(ctx context.Context) (*CIJobCacheStorage, *CIArtifactStorage) {
	runner := c.Config.CI
	if runner == nil {
		return nil, nil
	}
	c.ciDisk.mu.Lock()
	defer c.ciDisk.mu.Unlock()
	if c.ciDisk.measured && c.now().Before(c.ciDisk.expires) {
		return c.ciDisk.jobCache, c.ciDisk.artifacts
	}
	var jobCache *CIJobCacheStorage
	if stats, ok, err := runner.CacheStats(ctx); err == nil && ok {
		storage := ciJobCacheStorageOf(stats)
		jobCache = &storage
	}
	var artifacts *CIArtifactStorage
	if stats, ok, err := runner.ArtifactStats(ctx); err == nil && ok {
		storage := ciArtifactStorageOf(stats)
		artifacts = &storage
	}
	c.ciDisk.jobCache, c.ciDisk.artifacts = jobCache, artifacts
	c.ciDisk.measured, c.ciDisk.expires = true, c.now().Add(ciHistoryStorageTTL)
	return jobCache, artifacts
}

// ciJobCacheStorageOf is ci.CacheStats as this section reports it.
func ciJobCacheStorageOf(stats ci.CacheStats) CIJobCacheStorage {
	storage := CIJobCacheStorage{
		Bytes:      stats.Bytes,
		Entries:    stats.Entries,
		Dir:        stats.Dir,
		MaxBytes:   stats.MaxBytes,
		MaxEntries: stats.MaxEntries,
		Hits:       stats.Hits,
		Misses:     stats.Misses,
	}
	if stats.MaxAge > 0 {
		storage.MaxAgeSeconds = stats.MaxAge.Seconds()
	}
	if !stats.Oldest.IsZero() {
		oldest := stats.Oldest
		storage.OldestAt = &oldest
	}
	return storage
}

// ciArtifactStorageOf is ci.ArtifactStats as this section reports it.
func ciArtifactStorageOf(stats ci.ArtifactStats) CIArtifactStorage {
	storage := CIArtifactStorage{
		Bytes:            stats.Bytes,
		Runs:             stats.Runs,
		Artifacts:        stats.Artifacts,
		Dir:              stats.Dir,
		MaxBytes:         stats.MaxBytes,
		MaxRunBytes:      stats.MaxRunBytes,
		MaxArtifactBytes: stats.MaxArtifactBytes,
	}
	if stats.MaxAge > 0 {
		storage.MaxAgeSeconds = stats.MaxAge.Seconds()
	}
	if !stats.Oldest.IsZero() {
		oldest := stats.Oldest
		storage.OldestAt = &oldest
	}
	return storage
}

// ciHistoryStorageOf is the reading and the settings as one answer.
//
// The bounds are resolved through pkg/ci rather than read off the config
// row, so that what this reports as the ceiling is the ceiling the sweep
// applies -- units included, blanks included (ci.StoredRunRetentionFrom,
// and Resolved for the defaults a blank setting means).
func ciHistoryStorageOf(usage model.CIStepOutputUsage, cfg model.Config) CIHistoryStorage {
	keep := ci.StoredRunRetentionFrom(cfg).Resolved()
	storage := CIHistoryStorage{Bytes: usage.Bytes, Runs: usage.Runs, Steps: usage.Steps}
	if keep.OutputBytes > 0 {
		storage.Ceiling = keep.OutputBytes
	}
	if keep.OutputAge > 0 {
		storage.AgeDays = int(keep.OutputAge / (24 * time.Hour))
	}
	if keep.History > 0 {
		storage.RunsKept = keep.History
	}
	if !usage.Oldest.IsZero() {
		oldest := usage.Oldest
		storage.OldestAt = &oldest
	}
	return storage
}
