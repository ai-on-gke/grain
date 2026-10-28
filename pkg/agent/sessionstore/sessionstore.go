// Package sessionstore is the directory an agent CLI's conversations
// live in between one dispatch of a task and the next, and the bounds
// grain keeps on it.
//
// # What it is for
//
// agent/claude can hand a task's next run the conversation its last run
// ended in (model.SuspendedSession) for one reason only: claude is exec'd
// with the controller's own environment, so its session store is the
// controller's own ~/.claude and is still there a day later.
// agent/antigravity and agent/codex each build a *private* home per run
// -- a temp directory holding the config naming this run's sandbox and,
// for codex, a link to a login -- and delete it the moment Run returns,
// which takes the run's thread or conversation with it. There was
// therefore nothing for a second dispatch to resume, and neither package
// filled agent.Result.ResumableSession in.
//
// This is the store that outlives the run. A task gets one directory
// here, per framework, and that directory *is* the private home its runs
// are given: the CLI keeps writing its conversations where it always did,
// and what changes is only that the directory is still there next time.
//
// # Why that does not give the isolation back
//
// Both packages' doc comments say what their private home buys, and it is
// worth reading them before moving anything into here: agy's and codex's
// MCP servers are registered per *user*, in a config file inside the home,
// so two runs dispatched concurrently against two different sandboxes
// would otherwise share one registration and whichever wrote it last would
// decide where both runs' tools landed. That is a statement about two runs
// at once, not about time: what must never be shared is the binding
// between a run and its sandbox.
//
// So the split this package draws is between the files *grain* writes
// into the home and the files the *CLI* writes there:
//
//   - grain's files are per-run without exception -- the config naming
//     this run's own MCP endpoint and bearer token, the settings and
//     hooks a run is launched under, the link to a credential, the
//     scratch directory a granule run's native tools are pointed at. Every
//     one of them is rewritten at the start of every run and removed when
//     it ends, exactly as it is today, so a run never starts against
//     another run's sandbox binding and never leaves a credential link
//     behind it.
//   - what the CLI wrote is the conversation, and nothing in it names a
//     sandbox grain has to keep anybody out of. That is what stays.
//
// Drawing the line there rather than at a named "sessions" or
// "conversations" subdirectory is deliberate: where each CLI keeps its
// rollouts is undocumented, has already moved once under codex (see
// `codex migrate-rollouts`, which exists to migrate "legacy local sessions
// to paginated thread history"), and a grain that guessed the path wrong
// would silently stop resuming. "Everything grain did not write" needs no
// such guess.
//
// The directory is per task as well as per framework, so what a run can
// be resumed into is its own task's earlier thinking and never another
// task's. And a run only ever *asks* to resume when it was given this
// store: a framework that fell back to a temp home passes no token and
// reports none, because a thread id from a directory that has been deleted
// buys the next dispatch a refused exec and shows a person a resume token
// that resumes nothing.
//
// # Who prunes it
//
// grain does, here, and that is the other half of why this package exists.
// A claude session id is a hint about somebody else's store -- nothing in
// grain created ~/.claude and nothing in grain prunes it, which is why
// model.SuspendedSession treats a token as a hint rather than a promise.
// A directory grain creates is grain's to bound, and an unbounded one
// grows by a task's whole conversation history per task, forever, on the
// same disk as the deployment's database.
//
// Open prunes before it hands anything back, so the bound is kept by the
// thing that uses the store rather than by a sweeper somebody has to
// remember to schedule: a deployment that stops dispatching stops growing
// this directory anyway. Three bounds, each answering a different way of
// growing (see the constants below for the numbers): a task's store is
// dropped once nothing has dispatched that task for TTL, the store keeps
// only the MaxTasks most recently used tasks, and any one task's store is
// dropped whole once it passes MaxTaskBytes. All three cost the same thing
// when they fire and it is a small thing: the next run of that task starts
// its conversation from nothing, which is what every run did before this
// existed, and the framework's own fallback reports it (agent.
// SuspendedResumeRefusedNote).
//
// # This runs on the controller
//
// Every Framework's Host is agent.LocalHost: the CLI is a child of the
// daemon, staged and run on the daemon's own disk (agent.Host's own doc
// comment says so, and no option to set another exists -- the "WithHost is
// one of them" comments in all three framework packages name an option
// that was never written). This package is written against that, with os
// rather than through a Host, because the two things it does that a Host
// has no word for -- taking an exclusive lock, and listing a directory in
// order to prune it -- would each be a new method on a seam whose own doc
// comment says it is "not a filesystem abstraction". A Host that really
// was another machine would have to bring its store with it and the
// frameworks would have to stop calling this; until one exists, that is
// the wrong seam to widen.
package sessionstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultTTL is how long a task's store survives without that task
	// being dispatched. A week is well past the point where resuming is
	// worth anything -- the branch has moved, the base has moved, and
	// model.SuspendedSession.At exists precisely so a reader can see
	// that an old session is probably not worth much -- and it is what
	// keeps a deployment that ran a hundred tasks in March from still
	// holding their conversations in September.
	DefaultTTL = 7 * 24 * time.Hour
	// DefaultMaxTasks bounds the store when a deployment is busy enough
	// that nothing ages out: only this many tasks' stores are kept, the
	// most recently dispatched first. A task outside that window is one
	// nothing has run for longer than sixty-three others took, which is
	// as good a definition of "done with" as this package can compute.
	DefaultMaxTasks = 64
	// DefaultMaxTaskBytes bounds one task's own store. Conversations are
	// JSON lines and a long run writes a few megabytes of them, so this
	// is a ceiling rather than a budget: what it is really there for is
	// the caches a CLI unpacks into a home it is given (agy puts a
	// browser cache in one), which have no reason to be small and no
	// reason to be kept.
	DefaultMaxTaskBytes int64 = 256 << 20
	// lockTTL is how long a lock file is believed. A run holds one for
	// as long as it runs, and a daemon killed mid-run leaves one behind
	// with nobody to remove it; a day is longer than any run this
	// deployment allows and short enough that a crash costs a task one
	// day of resumes rather than all of them.
	lockTTL = 24 * time.Hour
	// lockSuffix names the lock beside the directory it locks rather
	// than inside it, so that removing a task's store whole -- which
	// pruning does -- never removes a lock a live run is holding.
	lockSuffix = ".lock"
	// markerName is the file Open touches, and what "most recently used"
	// is read from. A directory's own mtime will not do: the CLIs write
	// into subdirectories of the home, which does not change the mtime
	// of the home itself, so a busy task's store would look untouched
	// since the day it was created.
	markerName = ".grain-last-used"
)

// Store is one root directory holding every task's agent home, arranged
// <root>/<framework>/<task id>. A nil *Store is a deployment with no
// store configured and answers every Open with "no": that is what a
// caller built without one gets, and what keeps this whole feature off
// unless a daemon asked for it.
type Store struct {
	root string
	// The bounds Open enforces, and the clock it reads them against.
	// Fields rather than options because nothing outside this package
	// sets them: the argument for each number is in its constant above,
	// and a deployment that wants another one is a change here rather
	// than two more settings nobody understands. The package's own tests
	// set them directly.
	ttl          time.Duration
	maxTasks     int
	maxTaskBytes int64
	now          func() time.Time
}

// New builds a Store rooted at dir, which the daemon owns and this
// package creates on demand. "" is a deployment that has not configured
// one, and gives a Store that never opens anything.
//
// A relative dir is made absolute against this process's working
// directory. What Open returns becomes a run's HOME, and the framework
// starts its CLI in the sandbox rather than here: agy handed a relative
// HOME resolved it against the sandbox, found none of the config grain
// had written -- the pre-tool hook that withholds its native tools
// included -- and the hook's record of corp-research calls went
// somewhere grain never read back. A daemon started with `-data-dir
// d/data` did exactly that (docs/test-runs/2026-10-15-corp-research-capability.md).
func New(dir string) *Store {
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return &Store{
		root:         dir,
		ttl:          DefaultTTL,
		maxTasks:     DefaultMaxTasks,
		maxTaskBytes: DefaultMaxTaskBytes,
		now:          time.Now,
	}
}

// Open is this task's home directory for this framework's CLI, plus the
// release func its caller defers and a flag saying whether there is a
// store here at all.
//
// ok is false for every reason a caller has to treat identically: no
// store configured, no task id (a caller with no task -- a test, an
// in-process caller -- has nothing to key a store on), a name that has no
// business being a path element, a directory that cannot be created, or
// another run of the same task holding the lock right now. In all of them
// the caller builds the private per-run home it always built, and its run
// is exactly the run it was before this package existed: no resume asked
// for, and no session reported back.
//
// The lock is what makes "one task, one home" safe without arguing about
// whether the orchestrator can ever dispatch a task twice at once. It
// can: a console session and a dispatched run are both a task, and a
// deployment running two daemons against one data directory is a
// misconfiguration this package should survive rather than corrupt a
// conversation over. Whoever gets there first gets the store, and the
// other run gets a temp home and today's behaviour.
func (s *Store) Open(framework, taskID string) (dir string, release func(), ok bool) {
	if s == nil || s.root == "" || !validName(framework) || !validName(taskID) {
		return "", nil, false
	}
	base := filepath.Join(s.root, framework)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, false
	}
	// Before this task's own directory is created, so that a store over
	// its bounds is brought back under them by the next dispatch rather
	// than by the one after it -- and so that this task's store is
	// itself subject to the same bounds as everybody else's.
	s.prune(base)

	release, ok = s.lock(base, taskID)
	if !ok {
		return "", nil, false
	}
	dir = filepath.Join(base, taskID)
	if s.oversized(dir) {
		// One task's store past the ceiling. Removed whole rather than
		// trimmed: what is in there is a CLI's own layout, and picking
		// files out of a conversation would leave a conversation that
		// half exists, which is worse than one that does not.
		os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		release()
		return "", nil, false
	}
	s.touch(dir)
	return dir, release, true
}

// lock takes the exclusive lock on one task's store, or reports that
// somebody else has it.
//
// A lock older than lockTTL is taken from whoever left it: it can only
// be a daemon that died mid-run, since a live run removes its own in a
// defer, and believing it forever would mean one crash costing that task
// its resumes for as long as the deployment lives.
func (s *Store) lock(base, taskID string) (func(), bool) {
	path := filepath.Join(base, taskID+lockSuffix)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) && s.stale(path) {
		os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, false
	}
	f.Close()
	return func() { os.Remove(path) }, true
}

// stale reports whether the lock file at path is old enough to take.
func (s *Store) stale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return s.now().Sub(info.ModTime()) > lockTTL
}

// locked reports whether a task's store is held by a live run right now,
// which is what keeps pruning off it. A stale lock is not held: it is
// the crash case lock() above takes over, and a store nobody is using
// must still be prunable after one.
func (s *Store) locked(base, taskID string) bool {
	path := filepath.Join(base, taskID+lockSuffix)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return !s.stale(path)
}

// prune brings one framework's store back inside its bounds: task stores
// nothing has used for ttl, then the oldest of whatever is left over
// maxTasks, then lock files nobody can still be holding.
//
// Every failure here is ignored on purpose. Pruning runs on the way to
// starting a run, and a directory that could not be removed is a run
// that should still happen: the bound is a bound on disk, not a
// precondition of dispatching anything.
func (s *Store) prune(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	now := s.now()
	type used struct {
		name string
		at   time.Time
	}
	var kept []used
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			// An orphan lock: its task's store has already been pruned,
			// or a daemon died between taking it and creating anything.
			if strings.HasSuffix(name, lockSuffix) && s.stale(filepath.Join(base, name)) {
				os.Remove(filepath.Join(base, name))
			}
			continue
		}
		if s.locked(base, name) {
			// A run has this one open right now. Nothing here is worth
			// deleting a live run's conversation out from under it.
			continue
		}
		at := s.lastUsed(filepath.Join(base, name))
		if now.Sub(at) > s.ttl {
			os.RemoveAll(filepath.Join(base, name))
			continue
		}
		kept = append(kept, used{name: name, at: at})
	}
	if len(kept) <= s.maxTasks {
		return
	}
	slices.SortFunc(kept, func(a, b used) int { return b.at.Compare(a.at) })
	for _, d := range kept[s.maxTasks:] {
		os.RemoveAll(filepath.Join(base, d.name))
	}
}

// lastUsed is when a task was last dispatched into this store -- what
// the marker Open touches says, then that file's own mtime, then the
// directory's, for a store whose marker did not survive whatever the CLI
// does in there.
//
// The timestamp *in* the marker is read before the one on it because
// this package's own clock is what the bounds are kept against, and a
// filesystem's mtime is not always the same thing: a data directory
// restored from a backup, or copied between machines, comes back with
// mtimes that say when it was copied.
func (s *Store) lastUsed(dir string) time.Time {
	marker := filepath.Join(dir, markerName)
	if b, err := os.ReadFile(marker); err == nil {
		if at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			return at
		}
	}
	if info, err := os.Stat(marker); err == nil {
		return info.ModTime()
	}
	if info, err := os.Stat(dir); err == nil {
		return info.ModTime()
	}
	// Unreadable, so unprunable by age: leave it to the count bound
	// rather than deleting something this cannot describe.
	return s.now()
}

// touch records that a run is starting in this store now. The timestamp
// is written into the file as well as onto its mtime, for the person who
// goes looking at this directory with `cat` rather than `stat`.
func (s *Store) touch(dir string) {
	os.WriteFile(filepath.Join(dir, markerName),
		[]byte(s.now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// oversized reports whether one task's store has passed maxTaskBytes.
//
// It walks rather than trusting anything to report a size, and it stops
// as soon as it is over: the answer is a yes/no, and a store that is
// enormous is exactly the one not to spend a full walk on. Symlinks are
// counted as the links they are and never followed -- what is in this
// directory was written by somebody else's binary, and a walk that
// followed a link out of it would be measuring, and could be made to
// delete, a part of the filesystem this package has no business in.
func (s *Store) oversized(dir string) bool {
	var total int64
	over := false
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if total > s.maxTaskBytes {
			over = true
			return filepath.SkipAll
		}
		return nil
	})
	return over
}

// validName is what may become one path element of this store. Task ids
// and framework names are grain's own and are already tame, which is the
// point: nothing about a path under the deployment's data directory
// should depend on that staying true, and a name this refuses costs its
// caller a temp home rather than an escape from the root.
func validName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
