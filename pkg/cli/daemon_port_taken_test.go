// The sandbox port block (sandboxnet.go) promises that a taken port fails
// only the service reserved it. The 2026-10-01 walkthrough
// (docs/test-runs/2026-10-01-sandbox-isolation-port-block.md, finding 3)
// found the one port that broke that promise: with the git proxy's port
// occupied, runDaemon returned before it ever started the model proxies,
// so a single collision took four listeners down instead of one. The git
// proxy still halts dispatch on its own -- every dispatch clones through
// it -- but the model proxies now start in run(), beside the two caches,
// and this test drives run() itself to keep them there.
package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

func TestAGitProxyPortTakenFailsOnlyTheGitProxy(t *testing.T) {
	// reconcilerDown is process-global (daemon.go), and so is
	// modelProxies; neither may leak a failed run into another test.
	t.Cleanup(func() { daemon.ReconcilerDown.Store(false) })

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	base := freeAlignedBlock(t)
	// Exactly the walkthrough's repro: something else already holds the
	// git proxy's reserved port. Direct binds loopback when nothing has
	// to reach in from outside (sandboxAdvertiseHost), so that is the
	// address to take.
	taken, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+daemon.OffsetGitProxy)))
	if err != nil {
		t.Fatalf("occupying the git proxy's port: %v", err)
	}
	defer taken.Close()

	uiAddr := freeTCPAddr(t)
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
		GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
		UiAddr: uiAddr, Actor: "tester",
		SandboxPortBase: base,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- daemon.Run(ctx, cfg) }()

	// The git proxy's failure is still fatal to the reconcile loop, and
	// reconcilerDown is how that shows: wait for it, so the assertions
	// below are about the daemon after runDaemon has given up rather
	// than a race with its startup.
	deadline := time.Now().Add(daemonWaitLimit)
	for !daemon.ReconcilerDown.Load() {
		if time.Now().After(deadline) {
			cancel()
			<-runErr
			t.Fatal("runDaemon never reported failing with the git proxy's port taken")
		}
		select {
		case err := <-runErr:
			t.Fatalf("run() returned (%v) before ctx was cancelled", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Every model proxy is still published, on its own reserved port, and
	// answering there.
	for i, vendor := range modelproxy.Vendors() {
		want := net.JoinHostPort("127.0.0.1", strconv.Itoa(base+daemon.OffsetModelProxyBase+i))
		url := daemon.ModelProxies[vendor.Name].URL()
		if url != "http://"+want {
			t.Errorf("%s proxy URL = %q after the git proxy failed, want http://%s", vendor.Name, url, want)
			continue
		}
		resp, err := http.Get(url + "/")
		if err != nil {
			t.Errorf("%s proxy at %s is not answering after the git proxy failed: %v", vendor.Name, url, err)
			continue
		}
		resp.Body.Close()
	}

	// And so are the two caches, which never depended on the git proxy:
	// their ports are bound, so nothing else can take them.
	for name, offset := range map[string]int{"package cache": daemon.OffsetPackageCache, "Actions cache": daemon.OffsetActionsCache} {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(base+offset))
		if ln, err := net.Listen("tcp", addr); err == nil {
			ln.Close()
			if name == "Actions cache" {
				// A config{} that asks for no Actions cache gets none
				// (startActionsCache), so a free port there is not a failure.
				continue
			}
			t.Errorf("%s port %s is free after the git proxy failed, want it still bound", name, addr)
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run() = %v, want nil once ctx is cancelled", err)
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("run() never returned after ctx was cancelled")
	}
	for _, vendor := range modelproxy.Vendors() {
		if url := daemon.ModelProxies[vendor.Name].URL(); url != "" {
			t.Errorf("%s proxy URL = %q after run() returned, want it withdrawn", vendor.Name, url)
		}
	}
}
