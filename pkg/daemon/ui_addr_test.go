// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestDaemonRunEphemeralUIAddrAndOccupiedPortRefusal(t *testing.T) {
	// Not parallel: Run touches package-level globals.
	t.Cleanup(func() {
		model.SetDefaultGitHost("")
		ReconcilerDown.Store(false)
		LivePullRequests.Store(nil)
		liveGitHubCalls.Store(nil)
		liveRepoNames.Store(nil)
	})

	t.Run("ephemeral_port_zero_reports_bound_address", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		uiAddrCh := make(chan string, 1)
		runErr := make(chan error, 1)
		go func() {
			runErr <- Run(ctx, Config{
				DataDir:                t.TempDir(),
				SandboxDir:             t.TempDir(),
				MaxWorkers:             1,
				PollInterval:           time.Hour,
				UiAddr:                 "127.0.0.1:0",
				UIAddrReady:            func(addr string) { uiAddrCh <- addr },
				SandboxPortBase:        0,
				DisableNativeSandboxes: true,
				Actor:                  "tester",
			})
		}()
		defer func() {
			cancel()
			if err := <-runErr; err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("daemon Run returned unexpected error: %v", err)
			}
		}()

		var boundAddr string
		select {
		case boundAddr = <-uiAddrCh:
		case err := <-runErr:
			runErr <- err
			t.Fatalf("daemon exited before binding UI listener: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatal("daemon did not bind UI listener within 30s")
		}
		if strings.HasSuffix(boundAddr, ":0") {
			t.Fatalf("UIAddrReady reported unbound port %q", boundAddr)
		}

		deadline := time.Now().Add(30 * time.Second)
		for {
			resp, err := http.Get("http://" + boundAddr + "/api/config")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("daemon UI at %s did not answer within 30s", boundAddr)
			}
			time.Sleep(30 * time.Millisecond)
		}
	})

	t.Run("occupied_port_fails_cleanly", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()

		err = Run(context.Background(), Config{
			DataDir:                t.TempDir(),
			SandboxDir:             t.TempDir(),
			MaxWorkers:             1,
			PollInterval:           time.Hour,
			UiAddr:                 occupied.Addr().String(),
			SandboxPortBase:        0,
			DisableNativeSandboxes: true,
			Actor:                  "tester",
		})
		if err == nil || !strings.Contains(err.Error(), "starting the UI/API server: listening on ") {
			t.Fatalf("Run error on occupied UiAddr = %v, want 'starting the UI/API server: listening on ...'", err)
		}
	})
}
