// Package lifecycle serves a repository's task lifecycle definition from
// one of grain's own sandboxes (docs/task-lifecycle-sandbox.md).
//
// It is the Definitions implementation pkg/statemachine's engine already
// takes, and nothing more than that: the engine, the store, the
// transition log, the effect outbox, the idempotency keys and the
// reducer.failed retry are all on the other side of an interface this
// package does not touch. What it owns is the two things the design
// changes -- where a definition runs, and where it is declared.
//
// # Where it runs
//
// Not a subprocess of the controller but a long-lived HTTP server in a
// sandbox with NetworkAccess none, no checkout, no capability material
// and no inbound route but one unix socket grain exec's its way to
// (pkg/mcp's DialStream). The reducer cannot reach the daemon, the store,
// the state repository or GitHub, because it cannot reach anything --
// which is also what makes it safe for the definition to be a file in a
// repository every dispatched agent can rewrite.
//
// Compare what it replaces: a reducer in the controller's own container,
// with the controller's filesystem, network and environment, next to the
// deployment's credentials.
//
// # Where it is declared
//
// `.grain/lifecycle.yml` in the repository whose tasks it governs
// (pkg/repodecl's LoadLifecycle), rather than a directory grain pins from
// the deployment's config repository. Reviewed in the pull request that
// changes the thing it describes, branching with it, live when it merges.
//
// # What holds rather than falls back
//
// Every failure here holds the repository's tasks with an error on the
// lifecycle, and none of them quietly reverts to the deployment default.
// A broken lifecycle build silently changing what "approved" means in a
// repository is the failure this package is most careful not to have.
package lifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/statemachine"
)

// DefaultIdleTTL is how long a serving sandbox is held with nothing to
// do. A setting rather than a constant in practice, because a deployment
// with fifty repositories and one with three want different answers.
const DefaultIdleTTL = 15 * time.Minute

// DefaultMaxInFlight caps concurrent reduces against one sandbox.
//
// One sandbox serves every task in its repository on its version, and the
// engine serializes per instance without serializing across them, so
// there is real concurrency here. Anything beyond the cap queues behind
// it rather than opening another exec per request: an exec is a process on
// the host, and a hundred of them is a worse answer than a queue.
const DefaultMaxInFlight = 8

// Version names one repository's definition at one immutable point, and
// is what Instance.Definition holds.
//
// Two shapes, because the design has two paths. A repository that
// declared an `image:` pins the OCI digest its release job published --
// better than any hash grain could compute over declared source paths,
// since a digest is a content hash of the thing that actually runs,
// covering the interpreter and every dependency the build resolved rather
// than the lockfile that was meant to pin them. A repository that
// declared no image is served from the stock guest with its own checkout,
// and pins the commit.
//
// The repository is in the string because Definitions.Reducer is handed a
// version and nothing else, and a bare digest cannot say whose
// declaration it belongs to or how to serve it. Instance.Definition is a
// string whose shape nothing depends on, so this costs no migration --
// what is written into it changes, not its width.
type Version struct {
	Repo   model.RepoRef
	Digest string // "sha256:..." when an image was declared
	Commit string // the pinned commit when it was not
}

// String is the stored form: "owner/name@sha256:..." or
// "owner/name@commit:<sha>".
func (v Version) String() string {
	if v.Digest != "" {
		return v.Repo.String() + "@" + v.Digest
	}
	return v.Repo.String() + "@commit:" + v.Commit
}

// Stock reports whether this version is served from the stock guest with
// the repository checked out, rather than from a published image.
func (v Version) Stock() bool { return v.Digest == "" }

// ParseVersion reads back what String wrote.
func ParseVersion(s string) (Version, error) {
	repo, pin, ok := strings.Cut(s, "@")
	owner, name, named := strings.Cut(repo, "/")
	if !ok || !named || owner == "" || name == "" || pin == "" {
		return Version{}, fmt.Errorf("version %q is not owner/name@<pin>", s)
	}
	v := Version{Repo: model.RepoRef{Owner: owner, Name: name}}
	switch {
	case strings.HasPrefix(pin, "sha256:"):
		v.Digest = pin
	case strings.HasPrefix(pin, "commit:"):
		v.Commit = strings.TrimPrefix(pin, "commit:")
		if v.Commit == "" {
			return Version{}, fmt.Errorf("version %q pins no commit", s)
		}
	default:
		return Version{}, fmt.Errorf("version %q pins neither a digest nor a commit", s)
	}
	return v, nil
}

// DeclarationSource reads one repository's lifecycle declaration at its
// current head, and the commit that head is.
//
// The commit is not decoration: it is what a repository with no `image:`
// is served from, so a declaration read without one could not be served
// at all.
type DeclarationSource interface {
	Lifecycle(ctx context.Context, repo model.RepoRef) (model.LifecycleDeclaration, string, error)
}

// Images is the registry half.
//
// Resolve is a `docker manifest inspect` of the tag this repository's
// release job last pushed, which needs no CI feature that does not exist
// -- there is no need for the job to report an output. Prepare is the
// pull and the assembly: grain never boots a repository's image, it
// assembles one from its own container half and the repository's guest
// disk (docs/task-lifecycle-sandbox.md, "The answer: grain assembles, the
// repository supplies a disk").
type Images interface {
	// Resolve reads the digest behind one tag of image. The tag is the
	// commit the release job published at, so there is no mutable tag in
	// this path at all: a release either published that commit or it did
	// not.
	Resolve(ctx context.Context, image, tag string) (digest string, err error)
	// Prepare makes digest servable on this host and returns the local
	// image to serve it from.
	Prepare(ctx context.Context, image, digest string) (local string, err error)
}

// Sandboxes serves one version.
type Sandboxes interface {
	Serve(ctx context.Context, req ServeRequest) (Serving, error)
}

// ServeRequest is one serving sandbox to start.
type ServeRequest struct {
	// Version is what this sandbox serves, and what every instance routed
	// to it pins.
	Version Version
	// Image is the assembled local image, or empty for the stock guest
	// with Version.Commit checked out.
	Image string
	// Declaration is the serve block: the command, the socket, the
	// literal environment and the sandbox's shape.
	Declaration model.LifecycleDeclaration
	// Token is the bearer token this sandbox's server must compare
	// against, passed in the serve command's environment.
	Token string
}

// ServingOutput is implemented by a Serving that can say what the serve
// command has written -- pkg/orchestrator's own serving sandbox, out of
// the guest's journal.
//
// Optional, because it is a diagnostic rather than a capability: a
// readiness timeout is a readiness timeout either way, and a backend that
// cannot produce the output should not therefore be unable to serve. What
// it buys is the difference between "not ready in 30s" and "not ready in
// 30s: ModuleNotFoundError: No module named 'lifecycle'", which is the
// difference between an operator opening a shell and an operator fixing
// their Dockerfile.
type ServingOutput interface {
	Output(ctx context.Context) string
}

// Serving is a started sandbox: something to dial, and something to
// release.
type Serving interface {
	// DialStream opens one connection by exec'ing argv in the guest.
	DialStream(ctx context.Context, argv []string) (net.Conn, error)
	// Release gives the sandbox back.
	Release(ctx context.Context) error
}

// Census is what the store knows about instances, which this package
// needs for two things and reads for nothing else: an operator's "how
// much of this repository has moved to the version I just published", and
// the hold that keeps a sandbox warm while somebody is about to act.
type Census interface {
	Pinned(ctx context.Context, repo model.RepoRef, version string) (Pins, error)
}

// Pins counts one repository's instances against one version.
type Pins struct {
	// Here is instances pinned to the version asked about.
	Here int
	// Older is instances pinned to any other version -- what an operator
	// is watching drain after a release.
	Older int
	// Attention is instances on the version asked about that are active
	// with attention set, which is precisely the state a person is about
	// to act on. A non-zero count holds the sandbox past its idle TTL.
	Attention int
}

// SQLCensus implements Census against a statemachine SQL store or database.
type SQLCensus struct {
	store *statemachine.SQLStore
}

func NewSQLCensus(db *sql.DB) *SQLCensus {
	return &SQLCensus{store: statemachine.NewSQLStore(db)}
}

func (c *SQLCensus) Pinned(ctx context.Context, repo model.RepoRef, version string) (Pins, error) {
	pins, err := c.store.Pins(ctx, repo.String(), version)
	if err != nil {
		return Pins{}, err
	}
	return Pins{
		Here:      pins.Here,
		Older:     pins.Older,
		Attention: pins.Attention,
	}, nil
}

// ForwarderCommand is the guest-side program a dial exec's. Grain's own,
// installed in the stock sandbox guest, and never something a declaration
// supplies -- see cmd/grain-connect.
const ForwarderCommand = "grain-connect"

// endpointHost is the label every lifecycle URL uses for a host.
//
// Nothing resolves it and nothing should: the transport ignores the
// address it is handed and dials the sandbox it was built for, because
// there is no address for a guest. A name that looked resolvable would
// invite somebody to try.
const endpointHost = "lifecycle.sandbox.invalid"

// Registry is the design's RepoLifecycles: declarations, resolved
// digests, and the sandbox pool.
//
// One serving sandbox per (repo, version), shared by every task of that
// repository on that version.
type Registry struct {
	Declarations DeclarationSource
	Images       Images
	Sandboxes    Sandboxes
	Census       Census

	// IdleTTL is how long a sandbox with nothing to do is held. Zero is
	// DefaultIdleTTL.
	IdleTTL time.Duration
	// MaxInFlight caps concurrent reduces per sandbox. Zero is
	// DefaultMaxInFlight.
	MaxInFlight int
	// Now is the clock, for tests.
	Now func() time.Time
	// NewToken mints one sandbox's bearer token. Zero is crypto/rand.
	NewToken func() (string, error)
	// Log is optional.
	Log func(format string, args ...any)

	mu       sync.Mutex
	repos    map[string]*repoLifecycle
	serving  map[string]*servingSandbox
	starting map[string]chan struct{}
}

// repoLifecycle is what the registry remembers about one repository.
type repoLifecycle struct {
	repo       model.RepoRef
	declared   model.LifecycleDeclaration
	version    Version
	local      string
	state      model.LifecycleState
	err        string
	resolvedAt time.Time
}

// servingSandbox is one live sandbox and the client that reaches it.
type servingSandbox struct {
	version     Version
	declaration model.LifecycleDeclaration
	serving     Serving
	client      *http.Client
	token       string
	since       time.Time
	lastUsed    time.Time
	inFlight    int
}

// Resolve brings repo's lifecycle up to date and returns the version a
// new instance should pin.
//
// This is the whole of the pinning rule: read the declaration, resolve
// `image:` to the digest this repository last published, make sure that
// digest is here and assembled, and hand back what to pin. A repository
// with no declaration resolves to the zero Version and no error, which is
// the deployment default applying -- what every repository gets today.
//
// Nothing falls back. If the release job failed, or the tag does not
// resolve, or the pulled image is refused, the repository's tasks hold
// with the error on the lifecycle, the way sandboximage.Image blocks
// rather than handing back the stock guest.
func (r *Registry) Resolve(ctx context.Context, repo model.RepoRef) (Version, error) {
	declared, commit, err := r.Declarations.Lifecycle(ctx, repo)
	if err != nil {
		r.record(repo, func(l *repoLifecycle) {
			l.state, l.err = model.LifecycleFailed, err.Error()
		})
		return Version{}, fmt.Errorf("reading %s's lifecycle declaration: %w", repo, err)
	}
	if !declared.Declared() {
		r.record(repo, func(l *repoLifecycle) {
			*l = repoLifecycle{repo: repo, state: model.LifecycleNone}
		})
		return Version{}, nil
	}

	if declared.Image == "" {
		// The zero-config path: the stock guest with the repository
		// checked out at the pinned commit. No registry, no release job,
		// no capability, and nothing to pull.
		if strings.TrimSpace(commit) == "" {
			held := errors.New("no commit to pin -- a lifecycle served from the stock guest is " +
				"the repository's own checkout, so there has to be one")
			r.record(repo, func(l *repoLifecycle) {
				l.declared, l.state, l.err = declared, model.LifecycleFailed, held.Error()
			})
			return Version{}, held
		}
		version := Version{Repo: repo, Commit: commit}
		r.record(repo, func(l *repoLifecycle) {
			*l = repoLifecycle{repo: repo, declared: declared, version: version,
				state: model.LifecycleReady, resolvedAt: r.now()}
		})
		return version, nil
	}

	// The namespace check has already happened, in pkg/repodecl, when the
	// declaration was read -- so the error arrived in the pull request
	// that wrote it. Repeated here because a DeclarationSource is an
	// interface and this is the one refusal that must not be reachable by
	// implementing it differently.
	if err := model.ValidateLifecycleImage(declared.Image, repo); err != nil {
		r.record(repo, func(l *repoLifecycle) {
			l.declared, l.state, l.err = declared, model.LifecycleRefused, err.Error()
		})
		return Version{}, fmt.Errorf("%s's lifecycle image: %w", repo, err)
	}

	if strings.TrimSpace(commit) == "" {
		held := errors.New("no commit to resolve an image at -- a lifecycle image is published by " +
			"this repository's own release job, at the commit it released")
		r.record(repo, func(l *repoLifecycle) {
			l.declared, l.state, l.err = declared, model.LifecycleFailed, held.Error()
		})
		return Version{}, held
	}
	digest, err := r.Images.Resolve(ctx, declared.Image, commit)
	if err != nil {
		// The tag has never resolved: declared, and not published yet.
		// Distinct from failed, because the thing to do about it is a
		// release run rather than a fix.
		r.record(repo, func(l *repoLifecycle) {
			l.declared, l.state, l.err = declared, model.LifecycleUnpublished, err.Error()
		})
		return Version{}, fmt.Errorf("resolving %s: %w", declared.Image, err)
	}

	r.record(repo, func(l *repoLifecycle) {
		l.declared, l.state, l.err = declared, model.LifecyclePulling, ""
	})
	local, err := r.Images.Prepare(ctx, declared.Image, digest)
	if err != nil {
		state := model.LifecycleFailed
		var refusal *Refusal
		if errors.As(err, &refusal) {
			state = model.LifecycleRefused
		}
		r.record(repo, func(l *repoLifecycle) {
			l.declared, l.state, l.err = declared, state, err.Error()
		})
		return Version{}, fmt.Errorf("preparing %s: %w", digest, err)
	}

	version := Version{Repo: repo, Digest: digest}
	r.record(repo, func(l *repoLifecycle) {
		*l = repoLifecycle{repo: repo, declared: declared, version: version,
			local: local, state: model.LifecycleReady, resolvedAt: r.now()}
	})
	return version, nil
}

// Reducer implements statemachine.Definitions.
//
// Acquiring a warm sandbox is why that method takes a context and returns
// an error, and why Engine.Create calling it to validate a version before
// creating an instance becomes a real readiness check rather than an
// os.Stat.
func (r *Registry) Reducer(ctx context.Context, version string) (statemachine.Reducer, error) {
	parsed, err := ParseVersion(version)
	if err != nil {
		return nil, err
	}
	sandbox, err := r.acquire(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return statemachine.HTTPReducer{
		Client:   sandbox.client,
		Endpoint: "http://" + endpointHost + statemachine.ReducePath,
		Token:    sandbox.token,
		Timeout:  sandbox.declaration.Serve.RequestTimeout,
	}, nil
}

// Prewarm starts a repository's serving sandbox before anything needs it.
//
// Cold start is the cost this design adds that a subprocess reducer does
// not have -- a fresh python3 is tens of milliseconds and a fresh sandbox
// is seconds to tens of seconds -- and it falls on the first event after a
// quiet period, which is very often a person clicking a button and
// watching. So it is worth paying at dispatch, in parallel with the run's
// own sandbox, rather than at the first observation.
func (r *Registry) Prewarm(ctx context.Context, repo model.RepoRef) error {
	version, err := r.Resolve(ctx, repo)
	if err != nil {
		return err
	}
	if version == (Version{}) {
		return nil
	}
	_, err = r.acquire(ctx, version)
	return err
}

// acquire returns the sandbox serving version, starting it if nothing is.
func (r *Registry) acquire(ctx context.Context, version Version) (*servingSandbox, error) {
	key := version.String()
	for {
		r.mu.Lock()
		if sandbox, ok := r.serving[key]; ok {
			sandbox.lastUsed = r.now()
			r.mu.Unlock()
			return sandbox, nil
		}
		// Somebody else is booting this one. Waiting on them rather than
		// booting a second is the whole of "one serving sandbox per (repo,
		// version)": a burst of observations for one repository must not
		// each start a VM.
		if wait, ok := r.starting[key]; ok {
			r.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		started := make(chan struct{})
		if r.starting == nil {
			r.starting = map[string]chan struct{}{}
		}
		r.starting[key] = started
		r.mu.Unlock()

		sandbox, err := r.start(ctx, version)

		r.mu.Lock()
		delete(r.starting, key)
		if err == nil {
			if r.serving == nil {
				r.serving = map[string]*servingSandbox{}
			}
			r.serving[key] = sandbox
		}
		r.mu.Unlock()
		close(started)
		return sandbox, err
	}
}

// start boots one serving sandbox and waits for its definition to say it
// is ready.
func (r *Registry) start(ctx context.Context, version Version) (*servingSandbox, error) {
	r.mu.Lock()
	state := r.repos[repoKey(version.Repo)]
	r.mu.Unlock()
	if state == nil || !state.declared.Declared() {
		// A version nobody has resolved. Resolving it here would serve
		// whatever the repository declares *now*, which is not what this
		// instance pinned, so it is a refusal instead.
		return nil, fmt.Errorf("%s has no resolved lifecycle -- resolve it before serving a version of it",
			version.Repo)
	}
	if state.version != version {
		return nil, fmt.Errorf("%s's lifecycle is at %s, and %s is not a version this host has prepared: "+
			"an in-flight task keeps its digest across releases, so the image it pins has to be kept too",
			version.Repo, state.version, version)
	}

	token, err := r.newToken()
	if err != nil {
		return nil, fmt.Errorf("minting a lifecycle token: %w", err)
	}
	serving, err := r.Sandboxes.Serve(ctx, ServeRequest{
		Version: version, Image: state.local, Declaration: state.declared, Token: token,
	})
	if err != nil {
		r.record(version.Repo, func(l *repoLifecycle) {
			l.state, l.err = model.LifecycleFailed, err.Error()
		})
		return nil, fmt.Errorf("serving %s: %w", version, err)
	}

	sandbox := &servingSandbox{
		version: version, declaration: state.declared, serving: serving, token: token,
		since: r.now(), lastUsed: r.now(),
	}
	sandbox.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return serving.DialStream(ctx, []string{ForwarderCommand, state.declared.Serve.Listen})
		},
		MaxConnsPerHost: r.maxInFlight(),
		// One exec per connection, so an idle one is a process on the
		// host. Reaped well inside the sandbox's own idle TTL.
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}}

	if err := r.waitReady(ctx, sandbox); err != nil {
		_ = serving.Release(context.WithoutCancel(ctx))
		r.record(version.Repo, func(l *repoLifecycle) {
			l.state, l.err = model.LifecycleFailed, err.Error()
		})
		return nil, err
	}
	r.logf("lifecycle: %s serving %s", version.Repo, version)
	return sandbox, nil
}

// waitReady polls GET /v1/health until it says ready, or until
// ready-timeout-seconds runs out.
//
// A connection refused while the process is still starting is "not yet"
// rather than a failure, which is what makes this a poll. A protocol
// mismatch is not: a server answering a contract version this grain does
// not speak will not start speaking it, so it fails immediately and names
// both versions.
func (r *Registry) waitReady(ctx context.Context, sandbox *servingSandbox) error {
	deadline := sandbox.declaration.Serve.ReadyTimeout
	if deadline <= 0 {
		deadline = model.DefaultLifecycleReadyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	reducer := statemachine.HTTPReducer{
		Client:   sandbox.client,
		Endpoint: "http://" + endpointHost + statemachine.ReducePath,
		Token:    sandbox.token,
	}
	var last error
	for {
		report, err := reducer.Health(ctx)
		switch {
		case err == nil:
			r.logf("lifecycle: %s ready (%s)", sandbox.version, report.Definition)
			return nil
		case !errors.Is(err, statemachine.ErrReducerUnavailable):
			// A definition that is answering something unusable, rather
			// than one that has not answered yet.
			return err
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s's definition was not ready within %s (serve.ready-timeout-seconds): %w%s",
				sandbox.version.Repo, deadline, last, servedOutput(ctx, sandbox.serving))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// servedOutput is what the serve command said, for an error that is about
// to be shown to somebody.
//
// Detached from ctx, because every caller is here *because* ctx is done --
// a readiness timeout expires the context and then wants to explain
// itself, so asking on the expired one would always return nothing.
func servedOutput(ctx context.Context, serving Serving) string {
	speaker, ok := serving.(ServingOutput)
	if !ok {
		return ""
	}
	asked, cancel := context.WithTimeout(context.WithoutCancel(ctx), outputGrace)
	defer cancel()
	text := strings.TrimSpace(speaker.Output(asked))
	if text == "" {
		return ""
	}
	return ". Its last output was: " + text
}

// outputGrace bounds the ask. The error it decorates is already being
// held open for it.
const outputGrace = 15 * time.Second

// Sweep releases sandboxes nothing is using.
//
// Called on a timer. The hold is the interesting half: a version with an
// instance that is active with attention set is one somebody is about to
// act on, so it keeps its sandbox however idle the link has been.
func (r *Registry) Sweep(ctx context.Context) {
	ttl := r.idleTTL()
	now := r.now()

	r.mu.Lock()
	candidates := make([]*servingSandbox, 0, len(r.serving))
	for _, sandbox := range r.serving {
		if sandbox.inFlight == 0 && now.Sub(sandbox.lastUsed) >= ttl {
			candidates = append(candidates, sandbox)
		}
	}
	r.mu.Unlock()

	for _, sandbox := range candidates {
		if r.Census != nil {
			pins, err := r.Census.Pinned(ctx, sandbox.version.Repo, sandbox.version.String())
			if err != nil {
				// Not knowing is a reason to keep it, not to drop it: the
				// cost of holding a sandbox for another sweep is minutes
				// of one VM, and the cost of dropping one somebody is
				// waiting on is a cold start they watch.
				r.logf("lifecycle: keeping %s -- its instances could not be counted: %v", sandbox.version, err)
				continue
			}
			if pins.Attention > 0 {
				continue
			}
		}
		r.Release(ctx, sandbox.version)
	}
}

// Release gives one version's sandbox back. A sandbox that stops
// answering is replaced rather than retried in place, which is this
// followed by the next acquire.
func (r *Registry) Release(ctx context.Context, version Version) {
	key := version.String()
	r.mu.Lock()
	sandbox := r.serving[key]
	delete(r.serving, key)
	r.mu.Unlock()
	if sandbox == nil {
		return
	}
	sandbox.client.CloseIdleConnections()
	if err := sandbox.serving.Release(ctx); err != nil {
		r.logf("lifecycle: releasing %s: %v", version, err)
	}
}

// Status is one repository's lifecycle as an operator sees it.
func (r *Registry) Status(ctx context.Context, repo model.RepoRef) model.LifecycleStatus {
	r.mu.Lock()
	state := r.repos[repoKey(repo)]
	var sandbox *servingSandbox
	if state != nil {
		sandbox = r.serving[state.version.String()]
	}
	var snapshot repoLifecycle
	if state != nil {
		snapshot = *state
	} else {
		snapshot = repoLifecycle{repo: repo, state: model.LifecycleNone}
	}
	var info *model.LifecycleSandboxInfo
	if sandbox != nil {
		since := sandbox.since
		info = &model.LifecycleSandboxInfo{State: "serving", Since: &since, InFlight: sandbox.inFlight}
	}
	r.mu.Unlock()

	status := model.LifecycleStatus{
		Declared: snapshot.declared.Declared(),
		Name:     snapshot.declared.Name,
		Image:    snapshot.declared.Image,
		State:    snapshot.state,
		Error:    snapshot.err,
		Sandbox:  info,
	}
	if status.State == "" {
		status.State = model.LifecycleNone
	}
	if snapshot.version != (Version{}) {
		status.Version = snapshot.version.String()
	}
	if !snapshot.resolvedAt.IsZero() {
		at := snapshot.resolvedAt
		status.ResolvedAt = &at
	}
	if r.Census != nil {
		if pins, err := r.Census.Pinned(ctx, repo, status.Version); err == nil {
			status.Instances = model.LifecycleInstances{PinnedHere: pins.Here, OnOlderVersions: pins.Older}
		}
	}
	return status
}

// Refresh re-resolves the tag and pulls, and is what an operator needs
// after publishing an image out of band.
//
// Deliberately not a build. A build is a release run, so it is already an
// object with a log and a live view, reachable where every other run of
// that repository is; rebuilding is POST
// /api/repos/{owner}/{name}/release-runs, which already exists and is
// already the one caller grain trusts to say `release` besides its own
// merge reconciler.
func (r *Registry) Refresh(ctx context.Context, repo model.RepoRef) (model.LifecycleStatus, error) {
	if _, err := r.Resolve(ctx, repo); err != nil {
		return r.Status(ctx, repo), err
	}
	return r.Status(ctx, repo), nil
}

// Close releases every sandbox. A host restart is this and then nothing:
// instances reload from the store, sandboxes are started again on demand,
// and the outbox redelivers unacknowledged effects by key.
func (r *Registry) Close(ctx context.Context) {
	r.mu.Lock()
	versions := make([]Version, 0, len(r.serving))
	for _, sandbox := range r.serving {
		versions = append(versions, sandbox.version)
	}
	r.mu.Unlock()
	for _, version := range versions {
		r.Release(ctx, version)
	}
}

func (r *Registry) record(repo model.RepoRef, apply func(*repoLifecycle)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos == nil {
		r.repos = map[string]*repoLifecycle{}
	}
	state := r.repos[repoKey(repo)]
	if state == nil {
		state = &repoLifecycle{repo: repo, state: model.LifecycleNone}
		r.repos[repoKey(repo)] = state
	}
	apply(state)
	if state.repo == (model.RepoRef{}) {
		state.repo = repo
	}
}

func repoKey(repo model.RepoRef) string {
	return strings.ToLower(repo.Owner) + "/" + strings.ToLower(repo.Name)
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) idleTTL() time.Duration {
	if r.IdleTTL > 0 {
		return r.IdleTTL
	}
	return DefaultIdleTTL
}

func (r *Registry) maxInFlight() int {
	if r.MaxInFlight > 0 {
		return r.MaxInFlight
	}
	return DefaultMaxInFlight
}

func (r *Registry) newToken() (string, error) {
	if r.NewToken != nil {
		return r.NewToken()
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (r *Registry) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}
