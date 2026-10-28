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

//go:build !windows

package gitproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// hangingHostTools puts a fake `gh` and `git` on PATH whose behaviour is
// script (a shell fragment run by both, after they record their start in
// state/started), and shortens ambientCommandTimeout and ambientWaitDelay
// so a test of the bound does not wait the real fifteen seconds. Every
// process the fakes leave behind writes its pid to state/pids, and is
// killed when the test ends.
func hangingHostTools(t *testing.T, script string) (state string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	bin, state := t.TempDir(), t.TempDir()
	body := "#!" + sh + "\n" +
		"SLEEP=" + sleep + "\n" +
		"STATE=" + state + "\n" +
		"echo \"$0 $*\" >> \"$STATE/started\"\n" +
		script + "\n"
	for _, name := range []string{"gh", "git"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	prevTimeout, prevDelay := ambientCommandTimeout, ambientWaitDelay
	ambientCommandTimeout, ambientWaitDelay = 1*time.Second, 500*time.Millisecond
	t.Cleanup(func() {
		ambientCommandTimeout, ambientWaitDelay = prevTimeout, prevDelay
		data, _ := os.ReadFile(filepath.Join(state, "pids"))
		for _, f := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return state
}

// within fails t unless fn returns inside limit, and returns how long it
// took.
func within(t *testing.T, limit time.Duration, what string, fn func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s", what, limit)
		return 0
	}
}

// A host tool that never answers, and has started a process of its own
// that holds its stdout -- the shape of a credential helper that spawns
// a cache daemon or an agent -- is killed with everything it started at
// the timeout, and the error names the command to run by hand. Without
// a process group and WaitDelay, Wait would sit on the inherited pipe
// for as long as the grandchild lived.
func TestAmbientTokenBoundsAHungToolAndItsChildren(t *testing.T) {
	for _, source := range []string{AmbientSourceGH, AmbientSourceGit} {
		t.Run(source, func(t *testing.T) {
			hangingHostTools(t, `$SLEEP 60 & echo $! >> "$STATE/pids"; $SLEEP 60`)
			var err error
			within(t, ambientCommandTimeout+ambientWaitDelay+3*time.Second, "ambientToken", func() {
				_, _, _, err = ambientToken("github.com", source)
			})
			if err == nil {
				t.Fatal("a hung host tool answered with a token")
			}
			want := "`gh auth token --hostname github.com` did not finish within"
			if source == AmbientSourceGit {
				want = "`git credential fill` did not finish within"
			}
			if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "by hand as") {
				t.Fatalf("error does not say which command hung and how to try it: %v", err)
			}
		})
	}
}

// A tool that answers and exits, but leaves behind a process that left
// its process group (setsid, as a daemonizing helper does) still holding
// stdout open, is answered as soon as WaitDelay has passed -- with the
// token it printed.
func TestAmbientTokenDoesNotWaitOnADaemonizedChild(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid not installed")
	}
	hangingHostTools(t, setsid+` $SLEEP 60 & echo $! >> "$STATE/pids"; echo gho_token_from_gh`)
	var token string
	within(t, ambientWaitDelay+3*time.Second, "ambientToken", func() {
		token, _, _, err = ambientToken("github.com", AmbientSourceGH)
	})
	if err != nil || token != "gho_token_from_gh" {
		t.Fatalf("ambientToken = %q, %v; want the token gh printed", token, err)
	}
}

// One ambient credential whose helper hangs holds up nothing but itself:
// a PAT credential's lookup in the same set answers at once, and every
// concurrent load of the hung one shares one run of the tool.
func TestAHungAmbientCredentialDoesNotBlockOthers(t *testing.T) {
	state := hangingHostTools(t, `$SLEEP 60 & echo $! >> "$STATE/pids"; $SLEEP 60`)
	c, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetAmbient("operator", "github.com", AmbientSourceGH); err != nil {
		t.Fatal(err)
	}
	if err := c.SetToken("bot", "ghp_static"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cred := c.load("operator", ""); cred.Unusable == "" {
				t.Errorf("a hung gh answered with a usable credential")
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, _ := os.ReadFile(filepath.Join(state, "started")); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake gh never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	within(t, 200*time.Millisecond, "a PAT credential's Get while an ambient one hangs", func() {
		cred, ok := c.Get("bot")
		if !ok || cred.Token == nil || *cred.Token != "ghp_static" {
			t.Errorf("Get(bot) = %+v, %v", cred, ok)
		}
	})
	within(t, 200*time.Millisecond, "forget while an ambient lookup hangs", func() {
		c.forget("bot")
	})
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(state, "started"))
	if n := strings.Count(string(data), "\n"); n != 1 {
		t.Fatalf("three concurrent loads ran gh %d times; want one shared run", n)
	}
}

// AmbientIdentityWithin answers pending, at once, while the host's tool
// is still running, and the lookup finishes in the background: asking
// again once it has done so gets its answer.
func TestAmbientIdentityWithinReportsPendingInsteadOfWaiting(t *testing.T) {
	hangingHostTools(t, `$SLEEP 60 & echo $! >> "$STATE/pids"; $SLEEP 60`)
	c, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetAmbient("operator", "github.com", AmbientSourceGH); err != nil {
		t.Fatal(err)
	}
	var pending bool
	within(t, 300*time.Millisecond, "AmbientIdentityWithin", func() {
		_, pending, err = c.AmbientIdentityWithin("operator", 50*time.Millisecond)
	})
	if !pending || err != nil {
		t.Fatalf("AmbientIdentityWithin while gh hangs = pending %v, %v; want pending", pending, err)
	}
	within(t, ambientCommandTimeout+ambientWaitDelay+3*time.Second, "the background identity lookup", func() {
		_, pending, err = c.AmbientIdentityWithin("operator", time.Minute)
	})
	if pending || err == nil || !strings.Contains(err.Error(), "did not finish within") {
		t.Fatalf("AmbientIdentityWithin after the timeout = pending %v, %v; want the timeout", pending, err)
	}
}
