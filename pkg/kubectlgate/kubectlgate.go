// Package kubectlgate paces the kubectl processes this daemon starts
// against one Kubernetes API server, and recognizes the failures of
// theirs that are worth trying again.
//
// Both halves exist for the same burst: a wave of CI jobs and agent
// runs starting on a Kubernetes backend at once. Each sandbox costs a
// `kubectl apply`, a few `kubectl get`s and a dozen `kubectl exec`s
// while it is being set up, every one of them its own process with its
// own TCP and TLS handshake to the control plane, and sixty sandboxes
// starting together is hundreds of handshakes in the same second. A
// managed control plane answers that with dropped SYNs, which kubectl
// reports as "dial tcp ...: connect: connection timed out" -- and one
// such failure in the middle of a sandbox's setup errors the whole job,
// frees its budget slot, and starts the next sandbox into the same
// storm.
//
// pkg/k8s (k8s.Client) and pkg/mcp (KubectlExecRunner) share the one
// Default gate, which is why it lives in a package of its own rather
// than in either of them: mcp has no reason to import all of k8s for a
// semaphore, and two gates of 20 would be one of 40.
package kubectlgate

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// DefaultLimit is how many kubectl processes Default lets be starting
// at once. Enough that an ordinary dispatch never waits on it; few
// enough that a burst queues in-process instead of at the API server's
// listen backlog.
const DefaultLimit = 20

// StartupGrace is how long one admitted kubectl holds its slot before
// the slot is handed on even though the process is still running. The
// gate is there to pace connection set-up, not to bound how many
// sessions are open: a `kubectl exec` running an agent's ten-minute
// build is one TCP connection that finished its handshake long ago, and
// letting it hold a slot for those ten minutes would let twenty long
// commands stall every sandbox behind them. A process that exits sooner
// gives its slot back when it exits.
//
// It is longer than kubectl's own 30s TCP dial timeout on purpose. A
// control plane shedding a burst drops SYNs, and a kubectl whose SYN was
// dropped sits in its dial for the full 30s before it fails; with a
// grace shorter than that, the gate handed those slots on while the
// dials were still pending, and admitted a second and third wave of
// handshakes on top of the first -- the opposite of pacing. At 35s a
// slot is held until its dial has either connected or given up.
const StartupGrace = 35 * time.Second

// Gate is a counting semaphore over kubectl start-ups. The zero value
// is not usable; build one with New.
type Gate struct {
	slots chan struct{}
	grace time.Duration
}

// New returns a gate admitting at most limit kubectl processes in
// their first grace of running. A limit below one is one.
func New(limit int, grace time.Duration) *Gate {
	if limit < 1 {
		limit = 1
	}
	return &Gate{slots: make(chan struct{}, limit), grace: grace}
}

// Default is the gate every kubectl this process starts goes through,
// unless a test hands its caller another.
var Default = New(DefaultLimit, StartupGrace)

// Acquire waits for a slot, or for ctx to end. The release it returns
// must be called once the process has exited; calling it more than once
// is harmless, and the slot is released on its own after the gate's
// grace if the process is still running then.
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
	var once sync.Once
	free := func() { once.Do(func() { <-g.slots }) }
	timer := time.AfterFunc(g.grace, free)
	return func() {
		timer.Stop()
		free()
	}, nil
}

// MaxAttempts is how many times a kubectl that failed to reach the API
// server is run in all, the first try included.
const MaxAttempts = 3

// retryBase is the first back-off; each later one doubles it. A var so
// a test does not have to sit out real back-offs.
var retryBase = 500 * time.Millisecond

// backendRetryBase is the first back-off after a failure IsBackendDial
// matches, doubled like retryBase. It is longer because the fault is
// longer: an API server whose own dial to the kubelet (through the
// konnectivity proxy, on GKE) has stalled stays stalled for seconds, not
// for the fraction of one a dropped SYN costs, and a retry 0.5s later
// only meets the same stall. At 2s base the three attempts span roughly
// 6-12s of back-off.
var backendRetryBase = 2 * time.Second

// SetBackoffForTest makes every back-off d (jitter included) until the
// restore it returns is called, so a test of another package's retry
// does not sit out real ones. Not for use outside tests.
func SetBackoffForTest(d time.Duration) (restore func()) {
	old, oldBackend := retryBase, backendRetryBase
	retryBase, backendRetryBase = d, d
	return func() { retryBase, backendRetryBase = old, oldBackend }
}

// Backoff is how long to wait before attempt (1 for the first retry):
// retryBase doubled per earlier retry, plus up to as much again of
// jitter, so the sandboxes that failed together in one burst do not all
// come back together in the next.
func Backoff(attempt int) time.Duration {
	return backoffFrom(retryBase, attempt)
}

// BackoffFor is Backoff for a retry of the failure stderr reports: the
// longer backendRetryBase schedule when IsBackendDial matches it, and
// Backoff's otherwise.
func BackoffFor(stderr string, attempt int) time.Duration {
	if IsBackendDial(stderr) {
		return backoffFrom(backendRetryBase, attempt)
	}
	return Backoff(attempt)
}

func backoffFrom(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base << (attempt - 1)
	return d + rand.N(d+1)
}

// Sleep waits d, or until ctx ends, and reports whether the whole wait
// went by.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// IsTransientDial reports whether stderr is kubectl saying it never got
// a connection to the API server at all -- a dial that timed out or a
// TLS handshake that did -- or that the API server never got one to the
// kubelet to start the exec on (IsBackendDial), as opposed to anything
// the server, or a command in a pod, said back.
//
// That distinction is what makes a retry safe for every verb, apply and
// exec included: a request that never left this host cannot have been
// half-applied, and a command that kubectl never got to start cannot
// have run. Refusals (RBAC, NotFound, a bad manifest) and a pod
// command's own failures are deliberately not matched; trying those
// again only gets the same answer later.
func IsTransientDial(stderr string) bool {
	if strings.Contains(stderr, "net/http: TLS handshake timeout") || IsBackendDial(stderr) {
		return true
	}
	if !strings.Contains(stderr, "dial tcp") {
		return false
	}
	return strings.Contains(stderr, "connect: connection timed out") ||
		strings.Contains(stderr, "i/o timeout")
}

// IsTransientExecStart reports whether stderr is kubectl saying an exec
// never started its command because a hop on the way to the container
// failed: everything IsTransientDial matches, the API server failing to
// reach the node's kubelet while upgrading the exec's connection, and
// the exec's streams timing out before the kubelet acknowledged them.
//
// "unable to upgrade connection: error dialing backend: dial timeout,
// backstop" is the API server's half of that: kubectl did reach it, but
// it could not dial the kubelet (directly, or through the konnectivity
// tunnel a managed control plane uses), so the request was never handed
// to a node and the command was never started. "error: Timeout occurred"
// is spdystream's, from the client side of the same upgrade: the kubelet
// starts the command only once every stream it was asked for exists, so
// a stream that was never acknowledged is a command that never ran.
//
// An upgrade the API server refused for a reason of its own -- a
// container that does not exist, an exec RBAC forbids -- says no dial
// failure and is not matched: trying it again gets the same answer.
func IsTransientExecStart(stderr string) bool {
	if IsTransientDial(stderr) || strings.Contains(stderr, "error: Timeout occurred") {
		return true
	}
	if !strings.Contains(stderr, "unable to upgrade connection") {
		return false
	}
	for _, s := range []string{
		"error dialing backend",
		"dial timeout",
		"backstop",
		"connection refused",
		"connection reset by peer",
		"i/o timeout",
		"EOF",
		"No agent available",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// IsTransientTransport reports whether text -- a kubectl exec's stderr,
// or an error built around one -- names a failure of the path from this
// daemon to a sandbox's container rather than anything the container or
// a command in it said: every failure IsRetryableIdempotentExec retries
// for a nonzero status. A sandbox's setup that failed this way failed
// because of the cluster, not the task, and is worth preparing again
// (orchestrator's transient setup retry).
func IsTransientTransport(text string) bool {
	return IsRetryableIdempotentExec(1, text)
}

// IsBackendDial reports whether stderr is kubectl saying the API server
// could not upgrade an exec (or attach, or port-forward) to a stream
// because its own dial to the pod's kubelet failed:
//
//	error: unable to upgrade connection: error dialing backend: dial timeout, backstop
//
// and its "connection refused" and "i/o timeout" variants. That dial is
// the API server's to the node -- through the konnectivity proxy on a
// GKE control plane -- and the upgrade failing on it means the kubelet
// was never asked to start the command, so it is as safe to run again as
// a dial of kubectl's own that never connected. Callers still apply
// their own "did not start" guards (an exec runner's -1 with no status
// trailer) before trusting it, since a command in a pod that runs
// kubectl itself can print the same words.
func IsBackendDial(stderr string) bool {
	return strings.Contains(stderr, "unable to upgrade connection") &&
		strings.Contains(stderr, "error dialing backend")
}

// IsRetryableIdempotentExec reports whether a kubectl exec that ended
// with exitCode and stderr may simply be run again by a caller whose
// command is idempotent -- one that overwrites a file with the same
// bytes, say, so running it twice is the same as running it once.
//
// It matches everything IsTransientExecStart does, and also the failures in
// which the command may have started but the transport, not the command,
// ended it: a session cut off before the command reported its status
// (pkg/mcp's SessionCutOffNotice, which is always -1), kubectl's own
// request timeout, an API server it could not reach at all, and any -1,
// which is how every exec runner reports "no status from the command".
// None of those are safe to retry for a command that is not idempotent,
// which is why this is a separate question from IsTransientExecStart.
func IsRetryableIdempotentExec(exitCode int, stderr string) bool {
	if exitCode == 0 {
		return false
	}
	if exitCode == -1 || IsTransientExecStart(stderr) {
		return true
	}
	for _, s := range []string{
		"the exec session ended before the command reported an exit status",
		"Unable to connect to the server",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// RetryIdempotent runs an idempotent remote command up to MaxAttempts
// times, backing off between tries, for as long as its failures are ones
// IsRetryableIdempotentExec says are the transport's. It returns the
// last attempt's result, so a caller reports the failure it would have
// reported without the retry. A ctx that ends during a back-off ends the
// retries.
func RetryIdempotent(ctx context.Context, run func() (stdout, stderr string, exitCode int)) (stdout, stderr string, exitCode int) {
	for attempt := 1; ; attempt++ {
		stdout, stderr, exitCode = run()
		if attempt >= MaxAttempts || !IsRetryableIdempotentExec(exitCode, stderr) {
			return stdout, stderr, exitCode
		}
		if !Sleep(ctx, BackoffFor(stderr, attempt)) {
			return stdout, stderr, exitCode
		}
	}
}
