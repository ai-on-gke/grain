package ci

import (
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// StorageBoundsFrom reads the seven stored settings as this package reads
// them -- model.Config.CICacheMB and its neighbours -- with the units
// applied (MiB, days) and nothing else resolved, so a 0 travels through
// as "grain's own default".
//
// One definition of that arithmetic, for StoredRunRetentionFrom's
// reason: the sweep applies these bounds and the System pane's Storage
// section draws its bars against them (through CacheStats and
// ArtifactStats, which report the bounds actually in force), and a pane
// whose MiB were shifted somewhere else would be free to disagree with
// the ceiling the sweep is holding.
//
// No negative reading, unlike the CI history's four. There, "keep every
// run forever" is an operator's decision about a database whose growth
// is visible in every diff of the state repository, and it costs
// storage that somebody has asked for. Here it would mean a directory
// with no ceiling on a volume the deployment dispatches out of, which is
// the exact failure DirCache and DirArtifacts were bounded to prevent --
// a full data disk stops the daemon admitting work. ui.UpdateSettings
// refuses a negative value rather than storing one, and anything that
// arrives here anyway (a hand-edited row, a settings file merged from
// somewhere with no validation in front of it) reads as the default.
func StorageBoundsFrom(cfg model.Config) StorageBounds {
	var bounds StorageBounds
	if cfg.CICacheMB > 0 {
		bounds.Cache.MaxBytes = int64(cfg.CICacheMB) << 20
	}
	if cfg.CICacheEntries > 0 {
		bounds.Cache.MaxEntries = cfg.CICacheEntries
	}
	if cfg.CICacheDays > 0 {
		bounds.Cache.MaxAge = time.Duration(cfg.CICacheDays) * 24 * time.Hour
	}
	if cfg.CIArtifactMB > 0 {
		bounds.Artifacts.MaxBytes = int64(cfg.CIArtifactMB) << 20
	}
	if cfg.CIArtifactRunMB > 0 {
		bounds.Artifacts.MaxRunBytes = int64(cfg.CIArtifactRunMB) << 20
	}
	if cfg.CIArtifactFileMB > 0 {
		bounds.Artifacts.MaxArtifactBytes = int64(cfg.CIArtifactFileMB) << 20
	}
	if cfg.CIArtifactDays > 0 {
		bounds.Artifacts.MaxAge = time.Duration(cfg.CIArtifactDays) * 24 * time.Hour
	}
	return bounds
}

// StorageBoundsStoredAs is StorageBoundsFrom the other way about: cfg with the seven
// settings set to these bounds, which is how a first start seeds the
// config row out of the -ci-cache-max-* and -ci-artifact-max-* flags it
// was given (cmd/grain's toModelConfig).
//
// Lossy, and deliberately in one direction. The columns are whole MiB
// and whole days because that is what an operator edits in a form, while
// the flags are bytes and a duration, so a bound finer than a MiB or a
// day is seeded as the whole unit below it -- never below 1, since a
// stored 0 reads back as "grain's own default" and would silently undo
// the only thing the unit file asked for. Rounding down rather than up
// because these are ceilings on somebody else's disk: seeding 96 MiB
// where 100 MB was asked for keeps a promise the other rounding breaks.
func StorageBoundsStoredAs(b StorageBounds, cfg model.Config) model.Config {
	cfg.CICacheMB = wholeMiB(b.Cache.MaxBytes)
	cfg.CICacheEntries = max(0, b.Cache.MaxEntries)
	cfg.CICacheDays = wholeDays(b.Cache.MaxAge)
	cfg.CIArtifactMB = wholeMiB(b.Artifacts.MaxBytes)
	cfg.CIArtifactRunMB = wholeMiB(b.Artifacts.MaxRunBytes)
	cfg.CIArtifactFileMB = wholeMiB(b.Artifacts.MaxArtifactBytes)
	cfg.CIArtifactDays = wholeDays(b.Artifacts.MaxAge)
	return cfg
}

// wholeMiB and wholeDays are StoredAs' rounding: 0 stays 0 ("grain's
// own default"), and anything positive is at least one whole unit.
func wholeMiB(bytes int64) int {
	if bytes <= 0 {
		return 0
	}
	return max(1, int(bytes>>20))
}

func wholeDays(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return max(1, int(d/(24*time.Hour)))
}

// Resolved is these bounds with every "grain's own default" reading
// replaced by the number in force, which is what each store applies and
// what the Storage section is shown. Every field that comes back out of
