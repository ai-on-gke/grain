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

package runhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// TestDockerRunHostSurvivesADaemonRestart is the acceptance test's run-host
// half against real docker: a run starts in a sibling container, the
// daemon driving it stops and detaches, the container keeps running and
// writing its spool with nobody reading it, and a second Host -- the next
// daemon -- lists it, decides to adopt it, and tails it to the end from
// the persisted cursor. The transcript the two tee'd is the whole stdout,
// with no hole and no duplicate, and the run host is gone afterwards.
//
// It needs a docker daemon and builds grain, so it runs only with
// GRAIN_RUNHOST_DOCKER=1 (the "run host" job in .grain/workflows/tests.yml).
// GRAIN_RUNHOST_IMAGE names the image, debian:bookworm-slim by default:
// the test mounts its own static grain into it, so any image with a
// /bin/sh will do.
func TestDockerRunHostSurvivesADaemonRestart(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 to run against a real docker daemon")
	}
	image := os.Getenv("GRAIN_RUNHOST_IMAGE")
	if image == "" {
		image = "debian:bookworm-slim"
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "grain"), "github.com/ai-on-gke/grain/cmd/grain")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building grain: %v\n%s", err, out)
	}
	root := t.TempDir()
	deployment := fmt.Sprintf("runhost-test-%d", time.Now().UnixNano())
	driver := Docker{}
	t.Cleanup(func() {
		infos, _ := driver.List(context.Background(), deployment)
		for _, i := range infos {
			_ = driver.Destroy(context.Background(), i.Name)
		}
	})
	newHost := func() *Host {
		return &Host{
			LocalHost:   agent.LocalHost{Logf: t.Logf},
			Driver:      driver,
			Deployment:  deployment,
			Generation:  "test",
			Image:       image,
			Grain:       filepath.Join(bin, "grain"),
			User:        fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			SpoolRoot:   filepath.Join(root, "spool"),
			StagingRoot: filepath.Join(root, "staging"),
			Mounts:      []Mount{{Path: bin, ReadOnly: true}},
			Poll:        50 * time.Millisecond,
			CursorEvery: time.Nanosecond,
		}
	}

	// The first daemon: starts the run, then is stopped mid-run.
	first := newHost()
	var mu sync.Mutex
	var persisted int64
	var persistedAt time.Time
	first.OnCursor = func(_ context.Context, _ string, c int64) {
		mu.Lock()
		persisted, persistedAt = c, time.Now()
		mu.Unlock()
	}
	first.Detach = func(context.Context) bool { return true }
	ctx, stop := context.WithCancel(WithRun(context.Background(),
		Run{Task: "7", Run: "7-1", Deadline: time.Now().Add(10 * time.Minute)}))
	defer stop()
	const lines = 40
	script := fmt.Sprintf(`i=1; while [ $i -le %d ]; do echo "line $i"; i=$((i+1)); sleep 0.1; done`, lines)
	var firstTee syncBuffer
	errc := make(chan error, 1)
	go func() {
		_, err := first.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", script}}, &firstTee)
		errc <- err
	}()
	deadline := time.Now().Add(2 * time.Minute)
	for !strings.Contains(firstTee.String(), "line 3\n") {
		if time.Now().After(deadline) {
			t.Fatalf("the run host never produced output; tee so far %q", firstTee.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if err := <-errc; err != ErrDetached {
		t.Fatalf("the stopped daemon's Run = %v, want ErrDetached", err)
	}

	// Nobody is reading: the run host carries on regardless.
	time.Sleep(time.Second)
	infos, err := driver.List(context.Background(), deployment)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("listing after the daemon stopped = %+v, want the one run host", infos)
	}
	if infos[0].Labels.Run != "7-1" || infos[0].Labels.Task != "7" || infos[0].Labels.Protocol != Protocol ||
		infos[0].Labels.Deadline.IsZero() {
		t.Fatalf("labels = %+v", infos[0].Labels)
	}

	// The next daemon: list, decide, adopt.
	mu.Lock()
	cursor, lastSeen := persisted, persistedAt
	mu.Unlock()
	plan := Decide([]Claim{{Run: "7-1", Task: "7", LastSeen: lastSeen}}, infos,
		func(i Info) SpoolState { e, _ := ReadExit(i.Labels.Spool); return SpoolState{Exited: e != nil} }, time.Now())
	if len(plan.Adopt) != 1 || plan.Adopt[0].Gap < time.Second {
		t.Fatalf("plan = %+v, want one adoption crediting the gap", plan)
	}
	second := newHost()
	var secondTee bytes.Buffer
	out, err := second.Attach(context.Background(), plan.Adopt[0].Host, cursor, &secondTee)
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTee.String() + secondTee.String(); got != out {
		t.Fatalf("transcript has a hole or a duplicate:\nfirst  %q\nsecond %q\nwhole  %q", firstTee.String(), secondTee.String(), out)
	}
	if strings.Count(out, "\n") != lines || !strings.HasSuffix(out, fmt.Sprintf("line %d\n", lines)) {
		t.Fatalf("the run did not finish across the restart: %q", out)
	}
	if infos, _ := driver.List(context.Background(), deployment); len(infos) != 0 {
		t.Fatalf("the finished run host was left behind: %+v", infos)
	}
}

// syncBuffer is a bytes.Buffer the test may read while Attach writes it.
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
