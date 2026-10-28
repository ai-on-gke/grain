package kubectlgate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateAdmitsNoMoreThanItsLimitAtOnce(t *testing.T) {
	g := New(3, time.Hour)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			running.Add(-1)
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 3 {
		t.Errorf("peak concurrent holders = %d, want at most 3", got)
	}
}

func TestGateHandsASlotOnAfterTheGraceEvenWhileItsHolderRuns(t *testing.T) {
	// A long `kubectl exec` must not keep the next sandbox from
	// starting: its slot was for the connection set-up, long done.
	g := New(1, 20*time.Millisecond)
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	second, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire never admitted past a holder that outlived the grace: %v", err)
	}
	second()
	// Releasing the first holder after its grace already freed the slot
	// must not free a second one.
	release()
	third, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer third()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel2()
	if _, err := g.Acquire(ctx2); err == nil {
		t.Error("gate of one admitted two holders inside the grace")
	}
}

func TestGateAcquireGivesUpWithItsContext(t *testing.T) {
	g := New(1, time.Hour)
	release, _ := g.Acquire(context.Background())
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Error("Acquire on a full gate with a cancelled context succeeded")
	}
}

func TestIsTransientDialMatchesOnlyFailuresToReachTheServer(t *testing.T) {
	for stderr, want := range map[string]bool{
		`Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out`:                                                                                                         true,
		`error: error validating "STDIN": error validating data: failed to download openapi: Get "https://136.70.201.174/openapi/v2?timeout=32s": dial tcp 136.70.201.174:443: connect: connection timed out`: true,
		`Unable to connect to the server: dial tcp 10.0.0.1:443: i/o timeout`:                                                                                                                                 true,
		`Unable to connect to the server: net/http: TLS handshake timeout`:                                                                                                                                    true,
		`error: unable to upgrade connection: error dialing backend: dial timeout, backstop`:                                                                                                                  true,
		`error: unable to upgrade connection: error dialing backend: dial tcp 10.128.0.5:10250: connect: connection refused`:                                                                                  true,
		`error: unable to upgrade connection: error dialing backend: dial tcp 10.128.0.5:10250: i/o timeout`:                                                                                                  true,
		`error: unable to upgrade connection: container not found ("sandbox")`:                                                                                                                                false,
		`error: unable to upgrade connection: pod does not exist`:                                                                                                                                             false,
		`Error from server (NotFound): pods "p" not found`:                                                                                                                                                    false,
		`Error from server (Forbidden): pods "p" is forbidden`:                                                                                                                                                false,
		`chmod: cannot access '.gitconfig': No such file or directory`:                                                                                                                                        false,
		``: false,
	} {
		if got := IsTransientDial(stderr); got != want {
			t.Errorf("IsTransientDial(%q) = %v, want %v", stderr, got, want)
		}
	}
}

func TestBackoffDoublesWithJitter(t *testing.T) {
	for attempt, base := range map[int]time.Duration{1: retryBase, 2: 2 * retryBase, 3: 4 * retryBase} {
		for range 50 {
			if d := Backoff(attempt); d < base || d > 2*base {
				t.Fatalf("Backoff(%d) = %v, want within [%v, %v]", attempt, d, base, 2*base)
			}
		}
	}
}

func TestBackoffForWaitsLongerForAKubeletDialFailure(t *testing.T) {
	const upgrade = "error: unable to upgrade connection: error dialing backend: dial timeout, backstop"
	for attempt, base := range map[int]time.Duration{1: backendRetryBase, 2: 2 * backendRetryBase} {
		for range 50 {
			if d := BackoffFor(upgrade, attempt); d < base || d > 2*base {
				t.Fatalf("BackoffFor(upgrade, %d) = %v, want within [%v, %v]", attempt, d, base, 2*base)
			}
		}
	}
	if backendRetryBase <= retryBase {
		t.Errorf("backendRetryBase %v is no longer than retryBase %v", backendRetryBase, retryBase)
	}
	// Any other failure keeps Backoff's schedule.
	if d := BackoffFor("Unable to connect to the server: net/http: TLS handshake timeout", 1); d < retryBase || d > 2*retryBase {
		t.Errorf("BackoffFor(TLS timeout, 1) = %v, want within [%v, %v]", d, retryBase, 2*retryBase)
	}
	restore := SetBackoffForTest(time.Millisecond)
	if d := BackoffFor(upgrade, 1); d > 2*time.Millisecond {
		t.Errorf("BackoffFor under SetBackoffForTest = %v, want at most 2ms", d)
	}
	restore()
	if backendRetryBase != 2*time.Second {
		t.Errorf("backendRetryBase after restore = %v, want 2s", backendRetryBase)
	}
}

func TestIsRetryableIdempotentExecMatchesTransportFailuresOnly(t *testing.T) {
	for _, c := range []struct {
		exitCode int
		stderr   string
		want     bool
	}{
		{-1, "[grain] the exec session ended before the command reported an exit status; the command may have been cut off", true},
		{-1, "", true},
		{1, "error: Timeout occurred", true},
		{1, "error: unable to upgrade connection: error dialing backend: dial timeout, backstop", true},
		{1, "Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out", true},
		{1, "Unable to connect to the server: net/http: TLS handshake timeout", true},
		{1, "error: unable to upgrade connection: error dialing backend: dial timeout, backstop", true},
		{0, "error: Timeout occurred", false},
		{1, "chmod: cannot access '.gitconfig': No such file or directory", false},
		{1, `Error from server (Forbidden): pods "p" is forbidden`, false},
		{126, "sudo: a password is required", false},
	} {
		if got := IsRetryableIdempotentExec(c.exitCode, c.stderr); got != c.want {
			t.Errorf("IsRetryableIdempotentExec(%d, %q) = %v, want %v", c.exitCode, c.stderr, got, c.want)
		}
	}
}

func TestRetryIdempotentRetriesTransportFailuresUntilOneSucceeds(t *testing.T) {
	defer SetBackoffForTest(time.Millisecond)()
	calls := 0
	stdout, _, code := RetryIdempotent(context.Background(), func() (string, string, int) {
		calls++
		if calls < MaxAttempts {
			return "", "error: Timeout occurred", 1
		}
		return "done", "", 0
	})
	if code != 0 || stdout != "done" || calls != MaxAttempts {
		t.Errorf("RetryIdempotent = (%q, %d) after %d calls, want (done, 0) after %d", stdout, code, calls, MaxAttempts)
	}
}

func TestRetryIdempotentStopsAtMaxAttemptsAndOnTheCommandsOwnFailure(t *testing.T) {
	defer SetBackoffForTest(time.Millisecond)()
	calls := 0
	_, stderr, code := RetryIdempotent(context.Background(), func() (string, string, int) {
		calls++
		return "", "cut off", -1
	})
	if code != -1 || stderr != "cut off" || calls != MaxAttempts {
		t.Errorf("transport failure every time: (%q, %d) after %d calls, want the last failure after %d", stderr, code, calls, MaxAttempts)
	}
	calls = 0
	_, _, code = RetryIdempotent(context.Background(), func() (string, string, int) {
		calls++
		return "", "permission denied", 1
	})
	if code != 1 || calls != 1 {
		t.Errorf("the command's own failure: exit %d after %d calls, want exit 1 after 1", code, calls)
	}
}

func TestStartupGraceOutlastsKubectlsDialTimeout(t *testing.T) {
	// kubectl's TCP dial gives up after 30s; a slot handed on before then
	// admits another wave of handshakes on top of the one still dialing.
	if StartupGrace <= 30*time.Second {
		t.Errorf("StartupGrace = %v, want longer than kubectl's 30s dial timeout", StartupGrace)
	}
}
