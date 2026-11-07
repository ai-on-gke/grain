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

package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPLCStudioRunAndHealthz(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"-listen", "127.0.0.1:0", "-default-user", "dev@example.com"}, &stdout, &stderr)
	}()

	var baseURL string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out := stdout.String()
		if idx := strings.Index(out, "http://"); idx >= 0 {
			line := strings.TrimSpace(out[idx:])
			baseURL = strings.Fields(line)[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if baseURL == "" {
		cancel()
		t.Fatalf("server did not print listening URL; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		cancel()
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET /healthz status = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run exited with error: %v", err)
	}
}
