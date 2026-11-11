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

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitForFileExists(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readLinesTrimmed(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestCancelImageEachPhaseBeforePointOfNoReturn(t *testing.T) {
	prevDigest := "sha256:" + strings.Repeat("1", 64)
	newDigest := "sha256:" + strings.Repeat("2", 64)
	sandboxDigest := "sha256:" + strings.Repeat("3", 64)
	prevRef := "docker.io/grainai/grain:v3.1.0@" + prevDigest

	phases := []struct {
		name           string
		grace          time.Duration
		blockPull      bool
		blockVerify    bool
		blockHealth    bool
		blockSandbox   bool
		blockSchema    bool
		blockFlush     bool
		wantStep       string
		wantRemovedNew bool
		wantRemovedSb  bool
		wantDiscarded  bool
		wantResumed    bool
	}{
		{
			name:     "grace_window",
			grace:    30 * time.Second,
			wantStep: stepGrace,
		},
		{
			name:           "pull",
			blockPull:      true,
			wantStep:       stepPull,
			wantRemovedNew: true,
		},
		{
			name:           "verify",
			blockVerify:    true,
			wantStep:       stepVerify,
			wantRemovedNew: true,
		},
		{
			name:           "health_check",
			blockHealth:    true,
			wantStep:       stepHealthCheck,
			wantRemovedNew: true,
		},
		{
			name:           "sandbox_image",
			blockSandbox:   true,
			wantStep:       stepSandbox,
			wantRemovedNew: true,
			wantRemovedSb:  true,
		},
		{
			name:           "schema",
			blockSchema:    true,
			wantStep:       stepSchema,
			wantRemovedNew: true,
			wantRemovedSb:  true,
			wantDiscarded:  true,
		},
		{
			name:           "flush",
			blockFlush:     true,
			wantStep:       "",
			wantRemovedNew: true,
			wantRemovedSb:  true,
			wantDiscarded:  true,
			wantResumed:    true,
		},
	}

	for _, tc := range phases {
		t.Run(tc.name, func(t *testing.T) {
			shortRestartGrace(t)
			dir := t.TempDir()
			refFile := filepath.Join(dir, "grain-image.env")
			if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+prevRef+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Simulate a leftover temp file from staging.
			if err := os.WriteFile(refFile+".tmp", []byte("partial"), 0o644); err != nil {
				t.Fatal(err)
			}

			entered := filepath.Join(dir, "entered")
			removedFile := filepath.Join(dir, "removed")
			pullCalls := filepath.Join(dir, "pulled")

			pullScript := `printf '%s\n' "$1" >> ` + pullCalls + `; exit 0`
			if tc.blockPull {
				pullScript = `printf '%s\n' "$1" >> ` + pullCalls + `; touch ` + entered + `; sleep 30`
			}

			verifyScript := `printf '[%s]\n' '` + cosignPayload("docker.io/grainai/grain", newDigest) + `'`
			if tc.blockVerify {
				verifyScript = `touch ` + entered + `; sleep 30`
			} else {
				verifyScript = `case "$1" in *guest*) printf '[%s]\n' '` + cosignPayload("docker.io/grainai/guest", sandboxDigest) + `';; *) printf '[%s]\n' '` + cosignPayload("docker.io/grainai/grain", newDigest) + `';; esac`
			}

			runScript := `
while [ "${1#--}" != "$1" ]; do
  shift
  shift
done
ref="$1"
shift
cmd="$1"
case "$cmd" in
  sandbox-image)
    case "$ref" in
      *v3.1.0*) echo "docker.io/grainai/guest:v3.1.0" ;;
      *) echo "docker.io/grainai/guest:v3.2.0@` + sandboxDigest + `" ;;
    esac
    ;;
  schema-version)
    if [ "` + boolStr(tc.blockHealth) + `" = "true" ] && [ ! -f ` + entered + ` ]; then
      touch ` + entered + `
      sleep 30
    fi
    echo "102"
    ;;
  state)
    if [ "` + boolStr(tc.blockSchema) + `" = "true" ]; then
      touch ` + entered + `
      sleep 30
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac`

			mirrorPushScript := `exit 0`
			if tc.blockSandbox {
				mirrorPushScript = `touch ` + entered + `; sleep 30`
			}

			var discarded atomic.Int32
			var resumed atomic.Int32
			g := &gate{}

			u := newUpgrader(t, Config{
				GraceWindow: tc.grace,
				Image: &ImageConfig{
					Repository:         "docker.io/grainai/grain",
					RefFile:            refFile,
					PullCmd:            []string{"sh", "-c", pullScript, "sh"},
					DigestCmd:          []string{"sh", "-c", `case "$1" in *mirror*) echo "mirror.example/grain/guest@` + sandboxDigest + `";; *guest*) echo "docker.io/grainai/guest@` + sandboxDigest + `";; *) echo "docker.io/grainai/grain@` + newDigest + `";; esac`, "sh"},
					VerifyCmd:          []string{"sh", "-c", verifyScript, "sh"},
					RunCmd:             []string{"sh", "-c", runScript, "sh"},
					SandboxImageArgs:   []string{"sandbox-image"},
					SandboxImageMirror: "mirror.example/grain/guest",
					TagCmd:             []string{"true"},
					PushCmd:            []string{"sh", "-c", mirrorPushScript, "sh"},
					RemoveCmd:          []string{"sh", "-c", `printf '%s\n' "$1" >> ` + removedFile + `; exit 0`, "sh"},
				},
				HealthCheckArgs: []string{"schema-version"},
				Schema: &SchemaConfig{
					Version:     101,
					VersionArgs: []string{"schema-version"},
					CheckArgs:   []string{"state", "check", "-migrate"},
					Prepare: func(context.Context, string, int, int) (string, error) {
						return dir, nil
					},
					Discard: func() { discarded.Add(1) },
				},
				Hold: g.hold,
				Flush: func(ctx context.Context) (func(), error) {
					if tc.blockFlush {
						_ = os.WriteFile(entered, []byte("1"), 0o644)
						<-ctx.Done()
						return func() { resumed.Add(1) }, ctx.Err()
					}
					return func() { resumed.Add(1) }, nil
				},
				RestartCmd: []string{"sh", "-c", "exit 1"},
				StatusFile: filepath.Join(dir, "upgrade-status.json"),
			})

			if err := u.Start("v3.2.0"); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if tc.grace > 0 {
				deadline := time.Now().Add(5 * time.Second)
				for {
					st, _ := u.Status()
					if st.GraceUntil != nil && len(st.Steps) > 0 && st.Steps[0].State == StepRunning {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for grace window step: %+v", st)
					}
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				waitForFileExists(t, entered)
			}

			stBefore, err := u.Status()
			if err != nil {
				t.Fatalf("Status before cancel: %v", err)
			}
			cancellable, blockedWhy := u.CancelState(stBefore)
			if !cancellable || blockedWhy != "" {
				t.Fatalf("CancelState = (%v, %q), want (true, \"\")", cancellable, blockedWhy)
			}

			cancelled, err := u.Cancel("alice@example.com", "wrong edition")
			if err != nil || !cancelled {
				t.Fatalf("Cancel = (%v, %v), want (true, nil)", cancelled, err)
			}

			st, err := u.Status()
			if err != nil {
				t.Fatalf("Status after cancel: %v", err)
			}
			if st.Phase != PhaseCancelled {
				t.Fatalf("Phase = %q (detail %q), want %q", st.Phase, st.Detail, PhaseCancelled)
			}
			if st.CancelledBy != "alice@example.com" || st.CancelReason != "wrong edition" {
				t.Errorf("CancelledBy=%q CancelReason=%q", st.CancelledBy, st.CancelReason)
			}
			if !strings.Contains(st.Detail, "alice@example.com") || !strings.Contains(st.Detail, "wrong edition") {
				t.Errorf("Detail = %q, want operator and reason", st.Detail)
			}
			if st.GraceUntil != nil {
				t.Errorf("GraceUntil = %v after cancel, want nil", st.GraceUntil)
			}
			if tc.wantStep != "" {
				found := false
				for _, s := range st.Steps {
					if s.Name == tc.wantStep {
						found = true
						if s.State != StepCancelled {
							t.Errorf("step %q state = %q, want %q", s.Name, s.State, StepCancelled)
						}
					}
				}
				if !found {
					t.Errorf("step %q missing from %+v", tc.wantStep, st.Steps)
				}
			}

			// Queue hold must always be released.
			if held, released := g.counts(); held != 1 || released != 1 {
				t.Errorf("queue hold: held=%d released=%d, want 1/1", held, released)
			}

			// RefFile must still name prevRef, and .tmp cleaned up unless grace stopped before stageImage.
			gotRef, err := os.ReadFile(refFile)
			if err != nil || strings.TrimSpace(string(gotRef)) != "GRAIN_IMAGE="+prevRef {
				t.Errorf("RefFile = %q (%v), want GRAIN_IMAGE=%s", string(gotRef), err, prevRef)
			}
			if tc.grace == 0 {
				if _, err := os.Stat(refFile + ".tmp"); !os.IsNotExist(err) {
					t.Errorf("RefFile.tmp still exists after cancel cleanup: %v", err)
				}
			} else if len(readLinesTrimmed(pullCalls)) > 0 {
				t.Errorf("grace window cancel still pulled: %v", readLinesTrimmed(pullCalls))
			}

			removed := strings.Join(readLinesTrimmed(removedFile), "\n")
			if tc.wantRemovedNew && !strings.Contains(removed, "docker.io/grainai/grain:v3.2.0") {
				t.Errorf("removed = %q, want newly pulled v3.2.0 tag cleaned up", removed)
			}
			if strings.Contains(removed, "v3.1.0") {
				t.Errorf("removed = %q, must not remove running previous image", removed)
			}
			if tc.wantRemovedSb {
				if !strings.Contains(removed, "docker.io/grainai/guest:v3.2.0") {
					t.Errorf("removed = %q, want sandbox image cleaned up", removed)
				}
				if !strings.Contains(removed, "mirror.example/grain/guest:v3.2.0") {
					t.Errorf("removed = %q, want mirrored sandbox tag cleaned up", removed)
				}
			}
			if tc.wantDiscarded && discarded.Load() != 1 {
				t.Errorf("Schema.Discard called %d times, want 1", discarded.Load())
			}
			if tc.wantResumed && resumed.Load() != 1 {
				t.Errorf("Flush resume called %d times, want 1", resumed.Load())
			}
		})
	}
}

func TestCancelAfterPointOfNoReturnReturnsTooLate(t *testing.T) {
	shortRestartGrace(t)
	dir := t.TempDir()
	prevDigest := "sha256:" + strings.Repeat("a", 64)
	newDigest := "sha256:" + strings.Repeat("b", 64)
	prevRef := "docker.io/grainai/grain:v3.1.0@" + prevDigest
	refFile := filepath.Join(dir, "grain-image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+prevRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inRestart := filepath.Join(dir, "in-restart")
	releaseRestart := filepath.Join(dir, "release-restart")
	stopping := make(chan struct{})

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd:  []string{"sh", "-c", `echo "docker.io/grainai/grain@` + newDigest + `"`, "sh"},
			RunCmd:     []string{"true"},
			ListCmd:    []string{"true"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", `touch ` + inRestart + `; while [ ! -f ` + releaseRestart + ` ]; do sleep 0.02; done`},
		Stopping:        stopping,
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3.2.0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFileExists(t, inRestart)

	st, err := u.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantMsg := "restarting onto v3.2.0 — too late to cancel; upgrade back to docker.io/grainai/grain:v3.1.0 afterwards"
	cancellable, blocked := u.CancelState(st)
	if cancellable || blocked != wantMsg {
		t.Fatalf("CancelState during restart = (%v, %q), want (false, %q)", cancellable, blocked, wantMsg)
	}
	cancelled, err := u.Cancel("alice@example.com", "too slow")
	if cancelled {
		t.Fatal("Cancel succeeded after point of no return")
	}
	tooLate, ok := IsTooLate(err)
	if !ok || tooLate.Error() != wantMsg {
		t.Fatalf("Cancel err = %v, want TooLateError %q", err, wantMsg)
	}

	// Let RestartCmd finish and mark daemon stopping so run() exits while the
	// outgoing process is still serving requests during shutdown drain.
	close(stopping)
	_ = os.WriteFile(releaseRestart, []byte("1"), 0o644)
	waitForIdle(t, u)

	stAfter, err := u.Status()
	if err != nil {
		t.Fatalf("Status after run exit: %v", err)
	}
	if stAfter.PreviousImage != prevRef {
		t.Errorf("PreviousImage = %q, want %q", stAfter.PreviousImage, prevRef)
	}
	if _, err := u.Cancel("alice@example.com", "still draining"); !func() bool { _, ok := IsTooLate(err); return ok }() {
		t.Errorf("Cancel while RestartPending = %v, want TooLateError", err)
	}
}

func TestCancelSourceAndKubePipelinesAndRollbackToPreviousImage(t *testing.T) {
	t.Run("source_flush_cancel_restores_binary_and_readopts", func(t *testing.T) {
		shortRestartGrace(t)
		_, checkout := newFixtureRepo(t, "feature")
		dir := t.TempDir()
		installPath := filepath.Join(dir, "grain")
		if err := os.WriteFile(installPath, []byte("#!/bin/sh\necho old-binary\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		inFlush := filepath.Join(dir, "in-flush")
		var resumed atomic.Int32
		var discarded atomic.Int32
		g := &gate{}

		u := newUpgrader(t, Config{
			SrcDir:          checkout,
			BuildCmd:        []string{"sh", "-c", "mkdir -p bin && printf '#!/bin/sh\necho 2\n' > bin/grain && chmod +x bin/grain"},
			BuiltBinary:     filepath.Join(checkout, "bin", "grain"),
			InstallPath:     installPath,
			HealthCheckArgs: []string{"schema-version"},
			Schema: &SchemaConfig{
				Version:     1,
				VersionArgs: []string{"schema-version"},
				Prepare: func(context.Context, string, int, int) (string, error) {
					return dir, nil
				},
				Discard: func() { discarded.Add(1) },
			},
			Hold: g.hold,
			Flush: func(ctx context.Context) (func(), error) {
				_ = os.WriteFile(inFlush, []byte("1"), 0o644)
				<-ctx.Done()
				return func() { resumed.Add(1) }, ctx.Err()
			},
			RestartCmd: []string{"true"},
			StatusFile: filepath.Join(dir, "upgrade-status.json"),
		})

		if err := u.Start("feature"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitForFileExists(t, inFlush)

		cancelled, err := u.Cancel("bob@example.com", "stop source upgrade")
		if err != nil || !cancelled {
			t.Fatalf("Cancel = (%v, %v), want (true, nil)", cancelled, err)
		}
		gotBin, err := os.ReadFile(installPath)
		if err != nil || !strings.Contains(string(gotBin), "old-binary") {
			t.Errorf("installed binary = %q (%v), want previous binary restored", string(gotBin), err)
		}
		if _, err := os.Stat(installPath + ".prev"); !os.IsNotExist(err) {
			t.Errorf(".prev backup left behind: %v", err)
		}
		if resumed.Load() != 1 || discarded.Load() != 1 {
			t.Errorf("resumed=%d discarded=%d, want 1/1", resumed.Load(), discarded.Load())
		}
		if held, released := g.counts(); held != 1 || released != 1 {
			t.Errorf("hold counts = %d/%d, want 1/1", held, released)
		}
	})

	t.Run("kube_cancel_before_rollout_readopts_and_leaves_deployment", func(t *testing.T) {
		fake, calls, _ := fakeKubectl(t, kubeSucceeded, "")
		kubectl := kubectlAround(t, fake, "example/grain:v3.1.0@sha256:"+strings.Repeat("9", 64), 0)
		dir := t.TempDir()
		inFlush := filepath.Join(dir, "in-kube-flush")
		var resumed atomic.Int32
		g := &gate{}

		u := kubeUpgrader(t, kubectl, []string{"schema-version"})
		u.cfg.Hold = g.hold
		u.cfg.Flush = func(ctx context.Context) (func(), error) {
			_ = os.WriteFile(inFlush, []byte("1"), 0o644)
			<-ctx.Done()
			return func() { resumed.Add(1) }, ctx.Err()
		}

		if err := u.Start("main"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitForFileExists(t, inFlush)

		cancelled, err := u.Cancel("ops@example.com", "wrong cluster")
		if err != nil || !cancelled {
			t.Fatalf("Cancel = (%v, %v), want (true, nil)", cancelled, err)
		}
		st, _ := u.Status()
		if st.Phase != PhaseCancelled {
			t.Fatalf("Phase = %q, want cancelled", st.Phase)
		}
		if strings.Contains(calls(), "set image") {
			t.Errorf("kubectl calls = %q, must not repoint deployment on cancel", calls())
		}
		if resumed.Load() != 1 {
			t.Errorf("resumed = %d, want 1", resumed.Load())
		}
		if held, released := g.counts(); held != 1 || released != 1 {
			t.Errorf("hold counts = %d/%d, want 1/1", held, released)
		}
	})

	t.Run("rollback_to_persisted_previous_sha_image", func(t *testing.T) {
		shortRestartGrace(t)
		dir := t.TempDir()
		prevDigest := "sha256:" + strings.Repeat("c", 64)
		newDigest := "sha256:" + strings.Repeat("d", 64)
		prevRef := "docker.io/grainai/grain:sha-1a2b3c4@" + prevDigest
		refFile := filepath.Join(dir, "grain-image.env")
		if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+prevRef+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		u := newUpgrader(t, Config{
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				RefFile:    refFile,
				PullCmd:    []string{"true"},
				DigestCmd:  []string{"sh", "-c", `case "$1" in *sha-1a2b3c4*) echo "docker.io/grainai/grain@` + prevDigest + `";; *) echo "docker.io/grainai/grain@` + newDigest + `";; esac`, "sh"},
				RunCmd:     []string{"true"},
				ListCmd:    []string{"true"},
			},
			HealthCheckArgs: []string{"schema-version"},
			StatusFile:      filepath.Join(dir, "upgrade-status.json"),
		})

		if err := u.Start("main"); err != nil {
			t.Fatalf("Start(main): %v", err)
		}
		st := waitForPhase(t, u, PhaseOK)
		waitForIdle(t, u)
		if st.PreviousImage != prevRef {
			t.Fatalf("PreviousImage = %q, want %q", st.PreviousImage, prevRef)
		}

		// Roll back one-click to PreviousImage even though sha-1a2b3c4 is not
		// in DefaultAllowedBranches.
		if err := u.StartImage("", st.PreviousImage); err != nil {
			t.Fatalf("StartImage(PreviousImage): %v", err)
		}
		waitForPhase(t, u, PhaseOK)
		waitForIdle(t, u)
		running, err := u.Running()
		if err != nil || running.Ref != prevRef {
			t.Fatalf("Running after rollback = (%+v, %v), want %q", running, err, prevRef)
		}
	})
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// A Kubernetes deployment takes an explicit image only as the exact
// image its last upgrade replaced: the rollback the pane and
// `grain upgrade rollback` offer. Any other image is refused before
// anything touches the cluster, as every explicit image on this path was
// before rollback existed -- the Kubernetes path has no edition guard of
// its own (deploymentEdition reads only ImageConfig), so letting an
// arbitrary image through would let a caller cross editions unasked.
func TestKubeTakesAnExplicitImageOnlyAsTheRecordedRollback(t *testing.T) {
	u := kubeUpgrader(t, "/nonexistent", []string{"schema-version"})
	prev := "example/grain:v3.1.0@sha256:" + strings.Repeat("a", 64)
	if err := writeStatus(u.cfg.StatusFile, Status{Branch: "main", Phase: PhaseOK, PreviousImage: prev}); err != nil {
		t.Fatal(err)
	}

	for _, image := range []string{
		"example/grain:main",
		"example/grain@sha256:" + strings.Repeat("b", 64),
		"example/grain",
	} {
		if err := u.StartImage("main", image); err == nil || !strings.Contains(err.Error(), "roll back") {
			t.Errorf("StartImage(main, %q) = %v, want a refusal naming rollback", image, err)
		}
	}
	select {
	case <-u.Done():
	default:
		t.Fatal("a refused explicit image started an upgrade")
	}

	branch, ref, err := u.ValidateImage("", prev)
	if err != nil {
		t.Fatalf("ValidateImage(recorded previous image) = %v, want it accepted", err)
	}
	if ref != prev || branch != "v3.1.0" {
		t.Errorf("ValidateImage(recorded previous image) = (%q, %q), want (v3.1.0, %q)", branch, ref, prev)
	}
}
