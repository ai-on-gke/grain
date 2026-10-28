package orchestrator

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// A sandbox whose run is still live keeps authenticating to the git proxy
// long past its token's TTL, and stops once the run lets go of it.
func TestASandboxPastItsTokenTTLKeepsAuthenticatingWhileItsRunIsLive(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}

	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := gitproxy.NewSandboxTokenStore(path)
	store.TTL = time.Hour
	store.Now = clock
	token, err := store.EnsureToken("run-1")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := gitproxy.LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	proxy.Now = clock

	refreshed := make(chan struct{}, 1)
	refresh := func(sandbox string) (string, error) {
		tok, err := store.RefreshToken(sandbox)
		select {
		case refreshed <- struct{}{}:
		default:
		}
		return tok, err
	}
	// awaitRefresh waits for a refresh that began after it was called:
	// the first signal may belong to one already in flight.
	awaitRefresh := func() {
		t.Helper()
		for i := 0; i < 2; i++ {
			select {
			case <-refreshed:
			case <-time.After(10 * time.Second):
				t.Fatal("no refresh came")
			}
		}
	}

	stop := keepSandboxTokenFresh(context.Background(), refresh, "run-1", time.Millisecond)
	// Five 40-minute steps is over three hours: three TTLs, each of which
	// would have ended the token on its own.
	for step := 1; step <= 5; step++ {
		advance(40 * time.Minute)
		awaitRefresh()
		if name, ok := proxy.Authenticate(token); !ok || name != "run-1" {
			t.Fatalf("after %v the live run's token = %q, %v; want it still to authenticate",
				time.Duration(step)*40*time.Minute, name, ok)
		}
	}

	stop()
	advance(2 * time.Hour)
	if _, ok := proxy.Authenticate(token); ok {
		t.Fatal("a token kept on authenticating past its TTL once its run stopped refreshing it")
	}
}

// stop returns only once no refresh can follow it, so a Revoke right after
// it is the token's end rather than something a late refresh undoes.
func TestKeepSandboxTokenFreshStopsBeforeReturning(t *testing.T) {
	var mu sync.Mutex
	calls, afterStop := 0, 0
	stopped := false
	refresh := func(string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if stopped {
			afterStop++
		}
		return "", nil
	}
	stop := keepSandboxTokenFresh(context.Background(), refresh, "run-1", time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no refresh came")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	mu.Lock()
	stopped = true
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if afterStop != 0 {
		t.Fatalf("%d refreshes came after stop returned", afterStop)
	}
}

func TestKeepSandboxTokenFreshWithNoRefresherIsANoOp(t *testing.T) {
	keepSandboxTokenFresh(context.Background(), nil, "run-1", time.Millisecond)()
}
