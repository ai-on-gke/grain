package mcp

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
	"github.com/bwsalmon/grain/pkg/procgroup"
)

// KubectlExecRunner runs argv inside a sandbox pod's container through
// `kubectl exec` -- the remoteRunner contract NewSSHSandboxTools,
// ConfigureGitCredentialsOverSSH and PlaceFileOverSSH take, satisfied
// against Kubernetes instead of against a kontur VM's own container
// (DockerExecRunner) or a real SSH connection.
//
// There is no SSH anywhere in this transport and no credential of its
// own: `kubectl exec` goes to the API server, which authenticates this
// process as whatever its kubeconfig or in-cluster ServiceAccount token
// says, and the kubelet does the exec. So a sandbox pod needs no sshd,
// no authorized_keys and no key material distributed into it -- three
// pieces of setup the kontur backend needs and this one simply does not
// have. What it needs instead is the RBAC to create pods/exec in the
// sandbox namespace, which deploy/kubernetes/ grants to grain's own
// ServiceAccount and to nothing else.
type KubectlExecRunner struct {
	// Kubectl is the binary to run; "kubectl" when empty. It is a field
	// rather than a constant for the same reason k8s.Client.Kubectl is:
	// a test can point it at a recorded stand-in with no cluster
	// anywhere.
	Kubectl string
	// Kubeconfig is an optional path to a kubeconfig file passed as
	// "--kubeconfig <path>".
	Kubeconfig string
	// Namespace is the pod's namespace. Required -- a kubectl left to
	// fall back on the kubeconfig's current namespace is one that execs
	// into the wrong pod on a shared cluster, or none.
	Namespace string
	// Pod is the sandbox pod to exec into.
	Pod string
	// Container names the container within it. Empty leaves kubectl's
	// own default (the first container), which is right for the
	// single-container pods pkg/k8s creates but wrong the moment a
	// deployment's pod template grows a sidecar.
	Container string
	// Gate paces these execs against the API server alongside every
	// other kubectl this process starts; kubectlgate.Default when nil.
	Gate *kubectlgate.Gate
	// Through is what the pod runs each command through on its way to
	// where it actually runs -- `granule exec --` for a pod whose
	// sandbox is the granule VM inside it -- and nil for a pod that is
	// the sandbox itself. It is a field rather than a prefix the caller
	// puts on argv because the exit-status trailer (see RunStream) has
	// to wrap the command at the far end of every hop, not just the
	// first: a guest session torn down under `granule exec` must not
	// read as a trailer the pod's own shell wrote.
	Through []string
}

// Run executes argv in the pod, piping stdin to it verbatim if
// non-empty, and is what satisfies the remoteRunner contract.
//
// An exec that never reached the API server -- kubectl's dial or TLS
// handshake timed out, which a burst of sandboxes starting at once
// makes common -- or whose stream upgrade failed because the API server
// could not dial the kubelet ("unable to upgrade connection: error
// dialing backend") is run again after a short back-off, up to
// kubectlgate.MaxAttempts in all. That is safe for any argv, setup
// writes included, because kubectl never got as far as starting the
// command: a sandbox's git credentials or CA certificate are not left
// half-written by the attempt that failed, only not yet written.
//
// argv is passed through as a real argv rather than shell-quoted into
// one string, the same as DockerExecRunner: `kubectl exec -- ...` takes
// its trailing arguments as the command to run, so quoting here would
// double-quote every argument.
//
// exitCode follows remoteRunner's convention: the container command's
// own status when it ran, and -1 when it never did. kubectl propagates
// the exit status of the process it started, so a real remote status
// arrives here intact. A failure *before* the command ran -- no such
// pod, an API server that is not answering, RBAC that forbids the exec
// -- exits 1 as well, which is why execFailedBeforeContainer has to tell
// the two apart by what was written to stderr rather than by status
// alone.
//
// A session cut off after the command started is never run again,
// whatever its stderr says: the command may have done half of what it
// does, and its own output can mention a dial timeout of its own.
func (r *KubectlExecRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	return r.RunStream(ctx, argv, stdin, nil)
}

// RunStream is Run with somebody watching: everything either stream
// produces is written to out as it arrives, as well as being collected
// into the strings Run returns. See DockerExecRunner.RunStream, whose
// contract this is; a nil out is Run.
//
// Like Run, RunStream retries pre-container transient API-server dial
// failures up to kubectlgate.MaxAttempts. While stdout is still silent,
// initial stderr is held back in memory until the container is known to
// be running or the final attempt finishes, so a transient dial error
// that a retry clears never pollutes the live out stream.
//
// It does wait at the gate, which only holds a slot for the first
// kubectlgate.StartupGrace of the exec: long enough to pace the
// connection set-up, not so long that a command running for minutes
// keeps another sandbox from starting.
//
// kubectl's own exit status is not trusted to mean the command
// succeeded. `kubectl exec` exits 0 whenever the exec's error stream
// closes empty, and an empty close is what a session torn down
// mid-command looks like as well as what a clean exit does -- the API
// server or kubelet dropping the stream, a connection reset, a node
// going away. So the command runs under a shell (statusTrailerScript)
// that writes its real exit status to stderr as the last thing it does,
// and that trailer is what a 0 has to agree with. A session that ends
// without one did not report how the command ended, and is reported as
// -1 with a note on stderr saying so rather than as a success: a CI step
// whose `make build` was cut off half-way used to "pass" this way.
func (r *KubectlExecRunner) RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int) {
	for attempt := 1; ; attempt++ {
		var cutOff bool
		var flush func()
		stdout, stderr, exitCode, cutOff, flush = r.runStream(ctx, argv, stdin, out)
		if exitCode != -1 || cutOff || attempt >= kubectlgate.MaxAttempts || !kubectlgate.IsTransientExecStart(stderr) {
			flush()
			return stdout, stderr, exitCode
		}
		if !kubectlgate.Sleep(ctx, kubectlgate.BackoffFor(stderr, attempt)) {
			flush()
			return stdout, stderr, exitCode
		}
	}
}

// SessionCutOffNotice is what stderr ends with when an exec session
// ended without the command's own exit status -- see RunStream.
const SessionCutOffNotice = "[grain] the exec session ended before the command reported an exit status; " +
	"the command may have been cut off part-way, so this is reported as a transport failure rather than as its result"

// runStream is one attempt of RunStream, also saying whether the session
// was cut off after the command started -- the -1 that RunStream must not
// retry -- and returning flush to release any held pre-container stderr
// to out once the caller decides not to retry.
func (r *KubectlExecRunner) runStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int, cutOff bool, flush func()) {
	gate := r.Gate
	if gate == nil {
		gate = kubectlgate.Default
	}
	release, err := gate.Acquire(ctx)
	if err != nil {
		return "", err.Error(), -1, false, func() {}
	}
	defer release()
	cmd := exec.CommandContext(ctx, r.binary(), r.execArgs(argv, stdin != "")...)
	procgroup.Prepare(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	outBuf, errBuf := newHeadTailBuffer(), newHeadTailBuffer()
	live := newGatedLiveStream(out)
	trailer := &statusTrailer{to: io.MultiWriter(errBuf, writerFunc(live.writeStderr))}
	cmd.Stdout = io.MultiWriter(outBuf, writerFunc(live.writeStdout))
	cmd.Stderr = trailer
	err = cmd.Run()
	trailer.flush()
	cutOffResult := func() (string, string, int, bool, func()) {
		note := SessionCutOffNotice
		if errStr := errBuf.String(); errStr != "" && !strings.HasSuffix(errStr, "\n") {
			note = "\n" + note
		}
		_, _ = io.MultiWriter(errBuf, writerFunc(live.writeStderr)).Write([]byte(note + "\n"))
		return outBuf.String(), errBuf.String(), -1, true, live.flush
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			// A trailer means the command ran to its end in the pod,
			// however its stderr opens -- an in-pod `kubectl get`
			// that could not reach its own cluster says "Unable to
			// connect to the server" too -- and a -1 here is one
			// RunStream would run it again for.
			if code == 1 && !trailer.seen && outBuf.String() == "" && execFailedBeforeContainer(errBuf.String()) {
				return outBuf.String(), errBuf.String(), -1, false, live.flush
			}
			if code == 1 && !trailer.seen && (execLostMidCommand(errBuf.String()) || execFailedBeforeContainer(errBuf.String())) {
				// kubectl's own complaint about the stream, after
				// the command had already said something: the 1 is
				// kubectl's, not the command's.
				return cutOffResult()
			}
			// The command's own status, trailer or not: a shell killed
			// outright (137 from the OOM killer) has no chance to write
			// one, and the kubelet still reports how it died.
			return outBuf.String(), errBuf.String(), code, false, live.flush
		}
		return outBuf.String(), err.Error(), -1, false, live.flush
	}
	if !trailer.seen {
		return cutOffResult()
	}
	// kubectl's 0 and the command's own status, which only disagree if
	// the shell writing the trailer lived past it; the command's wins.
	return outBuf.String(), errBuf.String(), trailer.status, false, live.flush
}

// preContainerStderrLimit bounds how much stderr gatedLiveStream holds
// back while stdout is silent. kubectl's own pre-container dial or API
// server errors are one or two lines (a few hundred bytes); past this
// bound the stream is from the container and is flushed live.
const preContainerStderrLimit = 4096

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// gatedLiveStream serializes stdout and stderr writes to a live follower
// while holding initial stderr back until either stdout produces bytes,
// stderr grows past preContainerStderrLimit, or the caller flushes at the
// end of the attempt. That keeps a retryable pre-container kubectl dial
// timeout out of the live stream when a retry succeeds.
type gatedLiveStream struct {
	mu      sync.Mutex
	to      io.Writer
	holding bool
	held    []byte
}

func newGatedLiveStream(to io.Writer) *gatedLiveStream {
	return &gatedLiveStream{to: to, holding: to != nil}
}

func (s *gatedLiveStream) writeStdout(p []byte) (int, error) {
	if s.to == nil {
		return len(p), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
	_, _ = s.to.Write(p)
	return len(p), nil
}

func (s *gatedLiveStream) writeStderr(p []byte) (int, error) {
	if s.to == nil {
		return len(p), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holding {
		s.held = append(s.held, p...)
		if len(s.held) <= preContainerStderrLimit {
			return len(p), nil
		}
		s.releaseLocked()
		return len(p), nil
	}
	_, _ = s.to.Write(p)
	return len(p), nil
}

func (s *gatedLiveStream) flush() {
	if s.to == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
}

func (s *gatedLiveStream) releaseLocked() {
	if !s.holding {
		return
	}
	s.holding = false
	if len(s.held) > 0 {
		_, _ = s.to.Write(s.held)
		s.held = nil
	}
}

func (r *KubectlExecRunner) binary() string {
	if r.Kubectl != "" {
		return r.Kubectl
	}
	return "kubectl"
}

// execArgs renders the `kubectl exec` arguments Run executes, kept
// separate from Run itself so a test can assert on the invocation
// without a cluster anywhere -- the same separation
// DockerExecRunner.execArgs makes.
//
// -i is added only when there is stdin to pipe through; without it
// kubectl gives the exec'd process no stdin at all, which is what a
// command reading from a closed stdin should see anyway. -t is never
// added: a TTY would merge stderr into stdout and translate newlines,
// and every caller here wants the two streams apart.
func (r *KubectlExecRunner) execArgs(argv []string, interactive bool) []string {
	args := r.baseExecArgs(interactive)
	args = append(args, "sh", "-c", statusTrailerScript, "grain-exec")
	return append(args, argv...)
}

// dialArgs renders the `kubectl exec` arguments DialStream executes:
// always interactive, with Through included so a granule pod dials inside
// its guest, and without statusTrailerScript because a connection's
// stderr is read for failure diagnostics rather than filtered by
// statusTrailer.
func (r *KubectlExecRunner) dialArgs(argv []string) []string {
	return append(r.baseExecArgs(true), argv...)
}

func (r *KubectlExecRunner) baseExecArgs(interactive bool) []string {
	var args []string
	if r.Kubeconfig != "" {
		args = append(args, "--kubeconfig", r.Kubeconfig)
	}
	args = append(args, "-n", r.Namespace, "exec")
	if interactive {
		args = append(args, "-i")
	}
	if r.Container != "" {
		args = append(args, "-c", r.Container)
	}
	args = append(args, r.Pod, "--")
	return append(args, r.Through...)
}

// statusTrailerMarker opens the line statusTrailerScript writes to
// stderr after the command: the record-separator control character, so
// that no ordinary output starts the same way, then a name, then the
// status in decimal and a newline.
const statusTrailerMarker = "\x1egrain-exec-status="

// statusTrailerScript runs its arguments as a command and writes that
// command's exit status after it, as the last thing on stderr -- see
// RunStream for why. The command is "$@" rather than a string, so argv
// reaches it with no quoting of its own; stdin, stdout and stderr are
// the command's untouched.
const statusTrailerScript = `"$@"; s=$?; printf '\036grain-exec-status=%d\n' "$s" >&2; exit "$s"`

// statusTrailer is the stderr writer that takes statusTrailerScript's
// trailer back out of the stream, recording the status it carries, so
// neither the collected stderr nor somebody following it live ever sees
// it. Bytes that could still turn out to be the start of a trailer are
// held back until they either are one or are not; flush gives back
// whatever is held when the stream has ended.
type statusTrailer struct {
	to     io.Writer
	held   []byte
	seen   bool
	status int
}

func (t *statusTrailer) Write(p []byte) (int, error) {
	t.held = append(t.held, p...)
	for len(t.held) > 0 {
		i := strings.IndexByte(string(t.held), statusTrailerMarker[0])
		if i < 0 {
			t.emit(len(t.held))
			break
		}
		t.emit(i)
		if !t.consumeTrailer() {
			break
		}
	}
	return len(p), nil
}

// consumeTrailer looks at held, which starts with the marker's first
// byte, and reports whether there is more to scan: true once a whole
// trailer has been taken out or the byte has turned out to be ordinary
// output (which is passed on), false while it could still be a trailer
// arriving in pieces.
func (t *statusTrailer) consumeTrailer() bool {
	n := len(statusTrailerMarker)
	if len(t.held) < n {
		if strings.HasPrefix(statusTrailerMarker, string(t.held)) {
			return false
		}
		t.emit(1)
		return true
	}
	if !strings.HasPrefix(string(t.held), statusTrailerMarker) {
		t.emit(1)
		return true
	}
	end := strings.IndexByte(string(t.held[n:]), '\n')
	if end < 0 {
		if len(t.held)-n > 4 {
			t.emit(1)
			return true
		}
		return false
	}
	code, err := strconv.Atoi(string(t.held[n : n+end]))
	if err != nil || end == 0 {
		t.emit(1)
		return true
	}
	t.seen, t.status = true, code
	t.held = t.held[n+end+1:]
	return true
}

// emit passes the first n held bytes on as ordinary output.
func (t *statusTrailer) emit(n int) {
	if n == 0 {
		return
	}
	// Errors are not the command's to fail on: the collecting writer
	// does not return one, and a live follower's are swallowed by
	// liveStream already.
	_, _ = t.to.Write(t.held[:n])
	t.held = t.held[n:]
}

// flush passes on whatever is still held: the stream has ended, so it
// was never going to be a trailer.
func (t *statusTrailer) flush() {
	t.emit(len(t.held))
}

// execLostMidCommand reports whether stderr's last line is kubectl
// complaining about the stream itself -- "error: ..." from kubectl, or
// a server error -- which, with output from the command before it, is
// a session lost after the command started.
func execLostMidCommand(stderr string) bool {
	trimmed := strings.TrimRight(stderr, "\n")
	i := strings.LastIndexByte(trimmed, '\n')
	if i < 0 {
		return false
	}
	return execFailedBeforeContainer(trimmed[i+1:])
}

// execFailedBeforeContainer reports whether stderr looks like it came
// from kubectl rather than from the command in the pod -- the
// distinction ssh's own reserved exit status would give for free but
// `kubectl exec` cannot provide, since it reports the exit status of
// whatever it started and both failures exit 1.
//
// kubectl's own failures are recognized by a prefix on the *first* line
// of stderr: "Error from server (...)" for anything the API server
// refused (a missing pod, a forbidden exec, a pod that is not running),
// and "error: " for what kubectl itself refused before it ever called
// (an unparseable argument, no kubeconfig), and "Unable to connect to
// the server" for a server kubectl could not reach at all. A
// pod command's own stderr would have to open with one of those to be
// misread, and the cost of that misreading is a -1 where a 1 belonged:
// the same "the command did not run" report an unreachable sandbox
// already gets, which is the safer of the two directions to err in.
//
// One more shape is recognized anywhere in stderr rather than on its
// first line: a dial to the API server that failed while posting the
// exec request itself. kubectl reports that with the request's own URL
// ("Post \"https://.../exec?command=...\": dial tcp ...: connect:
// connection timed out"), sometimes behind a klog-formatted line of its
// own, so no prefix finds it -- but a pod command would have to print a
// pods/exec URL and a dial failure to be mistaken for it.
//
// So is the API server failing to reach the kubelet once it has the
// request ("error: unable to upgrade connection: error dialing backend:
// dial timeout, backstop"), which can also follow a klog line.
func execFailedBeforeContainer(stderr string) bool {
	if strings.Contains(stderr, "/exec?") && kubectlgate.IsTransientDial(stderr) {
		return true
	}
	if strings.Contains(stderr, "error: unable to upgrade connection") && kubectlgate.IsTransientExecStart(stderr) {
		return true
	}
	line := stderr
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "Error from server") || strings.HasPrefix(line, "error: ") ||
		strings.HasPrefix(line, "Unable to connect to the server")
}
