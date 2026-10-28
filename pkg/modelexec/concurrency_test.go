// Package modelexec tests concurrent agent execution and folder isolation.
package modelexec

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestConcurrentAgentExecutionAndFolderIsolation(t *testing.T) {
	token := "secret-concurrency-token"
	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy": "/bin/sh",
	}

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "concurrent-worker",

		Binaries:    binaries,
		Concurrency: 4,
	}

	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}

	if host.Concurrency() != 4 {
		t.Fatalf("host.Concurrency() = %d, want 4", host.Concurrency())
	}

	const numAgents = 4
	var wg sync.WaitGroup
	dirs := make([]string, numAgents)
	errs := make([]error, numAgents)

	startBarrier := make(chan struct{})

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			// Execute a command that prints its working directory and sleeps briefly
			out, err := host.Run(ctx, agent.Command{
				Path: "/bin/sh",
				Args: []string{"-c", "pwd && sleep 0.1"},
			}, nil)
			if err != nil {
				errs[idx] = err
				return
			}
			dirs[idx] = strings.TrimSpace(out)
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	seenDirs := make(map[string]bool)
	for i := 0; i < numAgents; i++ {
		if errs[i] != nil {
			t.Fatalf("agent %d failed: %v", i, errs[i])
		}
		dir := dirs[i]
		if dir == "" {
			t.Fatalf("agent %d had empty working dir", i)
		}
		if seenDirs[dir] {
			t.Fatalf("duplicate working dir %q used across agents", dir)
		}
		seenDirs[dir] = true
	}

	// Give time for deferred cleanup and verify agent folders were cleaned up
	time.Sleep(100 * time.Millisecond)
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("agent folder %s was not cleaned up after execution", dir)
		}
	}
}

func TestConcurrencyLimitQueueingAndDynamicLimits(t *testing.T) {
	token := "secret-queueing-token"
	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy": "/bin/sh",
	}

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "limited-worker",

		Binaries:    binaries,
		Concurrency: 1,
	}

	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}
	if host.Concurrency() != 1 {
		t.Fatalf("host.Concurrency() = %d, want 1", host.Concurrency())
	}

	// Check agent backends reporting Concurrency = 1
	backends := srv.AgentBackends()
	if len(backends) == 0 || backends[0].Concurrency != 1 {
		t.Fatalf("AgentBackends() concurrency = %+v, want 1", backends)
	}

	// Update limits via UI/Config
	srv.SetLimits([]model.AgentBackend{
		{
			ID:          "executor-limited-worker-antigravity",
			Framework:   "antigravity",
			Concurrency: 3,
		},
	})

	if host.Concurrency() != 3 {
		t.Fatalf("after SetLimits, host.Concurrency() = %d, want 3", host.Concurrency())
	}

	backends = srv.AgentBackends()
	if len(backends) == 0 || backends[0].Concurrency != 3 {
		t.Fatalf("after SetLimits, AgentBackends() concurrency = %+v, want 3", backends)
	}

	// Run 3 concurrent agents
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := host.Run(ctx, agent.Command{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello && sleep 0.05"},
			}, nil)
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("agent %d failed: %v", i, err)
		}
	}
}

func TestMultiExecutorPoolLeastLoadedScheduling(t *testing.T) {
	token := "secret-least-loaded-token"
	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy": "/bin/sh",
	}

	d1 := &Daemon{
		Addr:       ln.Addr().String(),
		Token:      token,
		Name:       "worker-pool",
		InstanceID: "inst-alpha",

		Binaries:    binaries,
		Concurrency: 2,
	}
	d2 := &Daemon{
		Addr:       ln.Addr().String(),
		Token:      token,
		Name:       "worker-pool",
		InstanceID: "inst-beta",

		Binaries:    binaries,
		Concurrency: 2,
	}

	go func() { _ = d1.Run(ctx) }()
	go func() { _ = d2.Run(ctx) }()

	// Wait for both daemons to connect
	var connected []Status
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connected = srv.Connected()
		if len(connected) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(connected) != 2 {
		t.Fatalf("expected 2 connected hosts, got %d: %+v", len(connected), connected)
	}

	// Verify unique instance IDs and backend IDs
	backends := srv.AgentBackends()
	if len(backends) != 2 {
		t.Fatalf("expected 2 agent backends, got %d: %+v", len(backends), backends)
	}
	if backends[0].ID == backends[1].ID {
		t.Fatalf("expected distinct backend IDs for duplicate hostnames, got %q and %q", backends[0].ID, backends[1].ID)
	}

	// First host selection
	h1 := srv.ConnectedHost("agy")
	if h1 == nil {
		t.Fatalf("expected connected host, got nil")
	}
	firstID := h1.ID()

	// Start a running job on h1
	jobStarted := make(chan struct{})
	jobDone := make(chan struct{})
	go func() {
		close(jobStarted)
		_, _ = h1.Run(ctx, agent.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 0.3"},
		}, nil)
		close(jobDone)
	}()

	<-jobStarted
	// Give time for running counter to increment
	time.Sleep(50 * time.Millisecond)

	// Second host selection should select the least-loaded host (the other instance)
	h2 := srv.ConnectedHost("agy")
	if h2 == nil {
		t.Fatalf("expected second connected host, got nil")
	}
	if h2.ID() == firstID {
		t.Fatalf("expected least-loaded host selection to pick different instance than %q, got %q", firstID, h2.ID())
	}

	<-jobDone
}

func TestDuplicateHostnameInstanceDeduplicationAndReconnect(t *testing.T) {
	token := "secret-dedup-token"
	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy": "/bin/sh",
	}

	// 1. Connect two daemons with same hostname "runner" but different instance IDs
	ctx1, cancel1 := context.WithCancel(ctx)
	d1 := &Daemon{
		Addr:       ln.Addr().String(),
		Token:      token,
		Name:       "runner",
		InstanceID: "runner-node-1",

		Binaries:    binaries,
		Concurrency: 1,
	}
	go func() { _ = d1.Run(ctx1) }()

	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	d2 := &Daemon{
		Addr:       ln.Addr().String(),
		Token:      token,
		Name:       "runner",
		InstanceID: "runner-node-2",

		Binaries:    binaries,
		Concurrency: 1,
	}
	go func() { _ = d2.Run(ctx2) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Connected()) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(srv.Connected()) != 2 {
		t.Fatalf("expected 2 connected hosts, got %d", len(srv.Connected()))
	}

	// 2. Reconnecting with SAME instance ID replaces the old connection cleanly
	cancel1()

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Connected()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx1Recon, cancel1Recon := context.WithCancel(ctx)
	defer cancel1Recon()
	d1Recon := &Daemon{
		Addr:       ln.Addr().String(),
		Token:      token,
		Name:       "runner",
		InstanceID: "runner-node-1",

		Binaries:    binaries,
		Concurrency: 1,
	}
	go func() { _ = d1Recon.Run(ctx1Recon) }()

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Connected()) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	connected := srv.Connected()
	if len(connected) != 2 {
		t.Fatalf("expected 2 connected hosts after reconnect, got %d", len(connected))
	}

	// 3. Cancelling d2 should drop host count to 1
	cancel2()

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.Connected()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	connected = srv.Connected()
	if len(connected) != 1 {
		t.Fatalf("expected 1 connected host after d2 cancellation, got %d", len(connected))
	}
	if connected[0].ID != "runner-node-1" {
		t.Fatalf("expected remaining host to have ID 'runner-node-1', got %q", connected[0].ID)
	}
}
