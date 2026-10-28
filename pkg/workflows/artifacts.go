package workflows

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Artifacts are the files a job leaves behind: a job says which paths
// are worth keeping, and grain copies them out of the sandbox before the
// sandbox is destroyed, so that a reader of the failed job has the
// evidence as well as the log.
//
// # Why this exists at all
//
// A grain sandbox dies with its job. GitHub's ui-e2e job ends with
// `actions/upload-artifact` keeping `ui/test-results/` on failure;
// .grain/workflows/tests.yml used to say, in as many words, that "here
// that directory dies with the sandbox, so the suite's own log is all a
// reader gets". What that cost is grain/task-182: two `ui e2e` runs went
// red on two different chromium specs, both reports said only
// "element(s) not found, 5000ms", and the actual cause -- a TypeError
// that unmounted the React tree -- was in the Playwright trace both
// times. Finding it took reproducing the whole suite locally; with the
// trace in hand it was ten minutes. Two earlier attempts had already
// spent twenty-five minutes each re-running the commit to learn nothing.
//
// # What it is not
//
// It is not GitHub's artifact system. There is no sharing between jobs
// (a grain workflow has no `download-artifact`, and `needs` passes
// nothing but a verdict), no retention policy per artifact, and no
// upload from a step -- a step cannot reach this, only the job's own
// declaration can. It is one thing: a failing job's files, kept for a
// while, downloadable by whoever reads the run.
//
// # Failure only, on purpose
//
// A job that succeeded keeps nothing. GitHub spells that as `if:
// failure()` on the upload step, which this format cannot say -- it has
// no `if:` on a step at all, deliberately (see the package doc) -- so
// the rule is the declaration's own: artifacts are collected from a job
// that did not succeed, and a green job costs this nothing. That is the
// case the evidence is for, it is what keeps a green deployment's disk
// exactly where it was, and "collect this from a job that passed too" is
// a key this format can grow later without moving anything.
//
// # Bounded, like the cache
//
// A Playwright trace bundle is megabytes, and a directory that grows
// without a ceiling is a deployment that fills its data disk and stops
// dispatching. So the same three bounds DirCache has, for the same
// reason and with the same treatment of the one entry that is too big to
// be worth keeping: an age cap, a cap on the whole directory, and a cap
// on a single artifact -- plus a cap per run, since one run of a dozen
// jobs is the unit that can surprise a host. See DirArtifacts.

// Artifact is one artifact of one job of one run, as a reader sees it:
// which job produced it, whether there is anything to download, and if
// not, why not.
//
// A declaration that produced no file is still an Artifact, and that is
// the point of the Note field. "The trace was 280MB, over the 32MB an
// artifact may be, so it was not kept" is the single most useful thing
// this can tell somebody who came looking for it -- far better than the
// same reader finding nothing at all and wondering whether the feature
// works.
type Artifact struct {
	// Run, Job and Name are what identifies it -- the run id, the job's
	// id within its workflow, and the declaration's own name.
	Run  string `json:"run"`
	Job  string `json:"job"`
	Name string `json:"name"`
	// Stored is whether there are bytes behind this. False means Note
	// says why: nothing matched, it was over a bound, or the collection
	// itself failed.
	Stored bool `json:"stored"`
	// Bytes is the size of the .tar.gz as it is kept, zero when nothing
	// was.
	Bytes int64 `json:"bytes,omitempty"`
	// CollectedAt is when it was copied out of the sandbox.
	CollectedAt *time.Time `json:"collectedAt,omitempty"`
	// Note is why there is nothing to download, and is empty for an
	// artifact that was kept.
	Note string `json:"note,omitempty"`
}

// Ref is what names this artifact to a store.
func (a Artifact) Ref() ArtifactRef { return ArtifactRef{Run: a.Run, Job: a.Job, Name: a.Name} }

// ArtifactRef names one artifact: a run, a job within it, and the
// declaration's name.
type ArtifactRef struct {
	Run  string
	Job  string
	Name string
}

func (r ArtifactRef) String() string { return r.Run + "/" + r.Job + "/" + r.Name }

// Filename is what a download of this should arrive as. Every artifact
// is a gzipped tar of the paths that were collected -- one shape rather
// than "a file if you asked for one file and an archive otherwise",
// because a reader who has to know which of the two they are about to
// get is a reader who has to open it to find out.
func (r ArtifactRef) Filename() string {
	return r.Run + "-" + r.Job + "-" + r.Name + ArtifactExt
}

// ArtifactExt is the suffix every stored artifact has, on the daemon's
// disk and in a download alike.
const ArtifactExt = ".tar.gz"

// ArtifactContentType is what a download is served as.
const ArtifactContentType = "application/gzip"

// Validate reports whether a ref could name an artifact at all. It is
// the one place the characters a ref may hold are decided, because a ref
// arrives from a URL and becomes a path on the daemon's disk: a store
// that took this on trust would be one path traversal away from serving
// any file the daemon can read.
func (r ArtifactRef) Validate() error {
	switch {
	case !ArtifactRunPattern.MatchString(r.Run):
		return fmt.Errorf("ci: %q is not a run id an artifact can be filed under", r.Run)
	case !JobIDPattern.MatchString(r.Job):
		return fmt.Errorf("ci: %q is not a job id an artifact can be filed under", r.Job)
	case !ArtifactNamePattern.MatchString(r.Name):
		return fmt.Errorf("ci: %q is not an artifact name", r.Name)
	}
	return nil
}

// ArtifactRunPattern is what a run id may look like to this store.
// Service.newID makes hex, and a test makes whatever it likes; this is
// the widest set that is still a single path segment with no dot in it,
// since the dot is what separates the job from the name in a filename.
var ArtifactRunPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ArtifactStore is where collected artifacts live. DirArtifacts is the
// implementation a deployment gets; this is the seam a test replaces,
// and what an Engine with nowhere to put files simply does not have.
//
// The contract, which is the Cache's contract with one addition:
//
//   - Nothing here may fail a job. Save reports what it did with an
//     Artifact rather than refusing: an artifact over a bound, or one
//     whose paths did not exist, is a recorded note and not an error.
//     An error is a store that is broken as a whole, and the engine logs
//     it and carries on.
//   - Open answers for what is there now. An artifact swept since the
//     run finished is gone, and saying so is the honest answer; the run
//     itself is unaffected.
//   - MaxArtifactBytes is the addition, and it is on this interface so
//     the number that decides "too big to keep" is the same number the
//     collector tells the sandbox. Copying a 280MB trace out of a
//     sandbox in order to throw it away would be the one bound that
//     costs more to enforce than to ignore.
type ArtifactStore interface {
	Save(ctx context.Context, ref ArtifactRef, body []byte) (Artifact, error)
	List(ctx context.Context, runID string) ([]Artifact, error)
	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, Artifact, error)
	MaxArtifactBytes() int64
}

// ClearableArtifacts is an ArtifactStore that can also be emptied, split
// out of ArtifactStore for InspectableCache's reason: the engine needs
// Save and Open and nothing else, and a test's store should not have to
// implement a directory walk to satisfy the interface the collector
// takes.
//
// It exists for one caller -- the reclaim behind the disk floor
// (grain/task-213) -- and deliberately has no operator-facing button, as
// DirArtifacts.Clear's own comment says: these are the only copy of what
// a failing job left behind, so dropping them is a thing a deployment
// does to stay up rather than a thing a person does to tidy.
type ClearableArtifacts interface {
	ArtifactStore
	// Clear empties the store, returning how many artifacts went and how
	// many bytes that freed. A store it could only partly empty is an
	// error carrying the counts it did manage.
	Clear(ctx context.Context) (int, int64, error)
}

// InspectableArtifacts is an ArtifactStore that can say what it is
// holding, split out of ArtifactStore for ClearableArtifacts' reason:
// the engine needs Save and Open and nothing else.
//
// It is the half of that pair an operator is allowed to have. Clearing
// is a thing a deployment does to stay up (see ClearableArtifacts);
// *measuring* is the question "what is eating this volume, and what of
// it can I safely clear", which cannot be answered about a store whose
// size nothing reports. A pane that can say "2 GB of artifacts, and
// every one of them is the only copy of what a run produced" has told an
// operator more than a button would.
type InspectableArtifacts interface {
	ArtifactStore
	// Stats is what is stored and what bounds it, read-only: a stats call
	// that swept as a side effect would be a pane deleting files every
	// time it polled (DirArtifacts.Stats).
	Stats(ctx context.Context) (ArtifactStats, error)
}

// ArtifactStats is one artifact store as an operator sees it: what it is
// holding and the bounds it is being kept inside -- CacheStats' own
// shape and its own purpose, which is the question "is this about to
// fill the data disk".
type ArtifactStats struct {
	Dir       string
	Runs      int
	Artifacts int
	Bytes     int64
	Oldest    time.Time
	Newest    time.Time

	MaxAge           time.Duration
	MaxBytes         int64
	MaxRunBytes      int64
	MaxArtifactBytes int64
}

// ArtifactNote is the wording of a "there is nothing to download" note,
// kept in one place so the CLI, the API and the daemon log say the same
// thing about the same outcome.
func ArtifactNote(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}
