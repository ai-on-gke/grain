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

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestHeadTailBufferKeepsEverythingThatFits(t *testing.T) {
	b := &headTailBuffer{limit: 100}
	var want strings.Builder
	for i := 0; want.Len() < 100; i++ {
		s := fmt.Sprintf("%d,", i)
		if want.Len()+len(s) > 100 {
			break
		}
		want.WriteString(s)
		b.Write([]byte(s))
	}
	if got := b.String(); got != want.String() {
		t.Fatalf("String() = %q, want %q", got, want.String())
	}
}

func TestHeadTailBufferKeepsHeadAndTailOfWhatDoesNot(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, limit := range []int{1, 2, 7, 64, 1000} {
		for trial := 0; trial < 50; trial++ {
			b := &headTailBuffer{limit: limit}
			var all []byte
			for len(all) < 5*limit+rng.Intn(3*limit+1) {
				p := make([]byte, rng.Intn(2*limit+2))
				for i := range p {
					p[i] = byte('a' + rng.Intn(26))
				}
				all = append(all, p...)
				if n, err := b.Write(p); n != len(p) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			head, tail := all[:limit/2], all[len(all)-(limit-limit/2):]
			got := b.String()
			if !strings.HasPrefix(got, string(head)) || !strings.HasSuffix(got, string(tail)) {
				t.Fatalf("limit %d: String() = %q, want head %q and tail %q", limit, got, head, tail)
			}
			marker := fmt.Sprintf("[%d bytes of output dropped", len(all)-limit)
			if !strings.Contains(got, marker) {
				t.Fatalf("limit %d: String() = %q, want it to say %q", limit, got, marker)
			}
		}
	}
}

// allocatedDuring reports how many bytes of heap f allocated, whether or
// not they were freed again: a peak can only be as high as this.
func allocatedDuring(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// writeLocalDockerShim puts a `docker` on PATH that runs `docker exec
// <container> argv...` as argv on this machine, so ContainerExecRunner
// drives a real process without a docker daemon.
func writeLocalDockerShim(t *testing.T) {
	t.Helper()
	writeFakeDockerExec(t, `shift 2; exec "$@"`)
}

// The audit's reproduction (task 2801): read_file of /dev/zero through a
// container runner grew this process's heap to 4 GB in three seconds.
func TestSSHReadFileOfDevZeroIsRefusedWithoutReadingIt(t *testing.T) {
	writeLocalDockerShim(t)
	tool := sshReadFileTool(&ContainerExecRunner{Container: "c"}, "")
	var res Result
	allocated := allocatedDuring(func() {
		res = tool.Handler(context.Background(), map[string]any{"file_path": "/dev/zero"})
	})
	if !res.IsError || !strings.Contains(res.Text, "not a regular file") {
		t.Fatalf("read_file /dev/zero = %+v, want a not-a-regular-file error", res)
	}
	if allocated > 64<<20 {
		t.Fatalf("read_file /dev/zero allocated %d bytes", allocated)
	}
}

func TestSSHReadFileRefusesAFileOverTheCapRatherThanTruncatingIt(t *testing.T) {
	writeLocalDockerShim(t)
	old := maxReadFileBytes
	maxReadFileBytes = 1 << 20
	t.Cleanup(func() { maxReadFileBytes = old })

	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, 512<<20); err != nil {
		t.Fatal(err)
	}
	runner := &ContainerExecRunner{Container: "c"}
	var res Result
	allocated := allocatedDuring(func() {
		res = sshEditFileTool(runner, "").Handler(context.Background(), map[string]any{
			"file_path": big, "old_string": "x", "new_string": "y",
		})
	})
	if !res.IsError || !strings.Contains(res.Text, "file is larger than") {
		t.Fatalf("edit_file of a 512 MiB file = %+v, want a too-large error", res)
	}
	if allocated > 64<<20 {
		t.Fatalf("edit_file of a 512 MiB file allocated %d bytes", allocated)
	}
	if info, err := os.Stat(big); err != nil || info.Size() != 512<<20 {
		t.Fatalf("edit_file changed the file it refused: %v, %v", info, err)
	}
}

func TestContainerExecRunnerBoundsWhatItCollects(t *testing.T) {
	writeLocalDockerShim(t)
	old := execOutputLimit
	execOutputLimit = 1 << 20
	t.Cleanup(func() { execOutputLimit = old })

	var stdout string
	var code int
	allocated := allocatedDuring(func() {
		stdout, _, code = (&ContainerExecRunner{Container: "c"}).Run(context.Background(),
			[]string{"head", "-c", fmt.Sprint(256 << 20), "/dev/zero"}, "")
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(stdout) > execOutputLimit+256 || !strings.Contains(stdout, "bytes of output dropped") {
		t.Fatalf("stdout is %d bytes, want at most the %d-byte limit with a marker", len(stdout), execOutputLimit)
	}
	if allocated > 64<<20 {
		t.Fatalf("collecting 256 MiB of output allocated %d bytes", allocated)
	}
}

func TestLocalRunCommandBoundsWhatItCollects(t *testing.T) {
	old := execOutputLimit
	execOutputLimit = 1 << 20
	t.Cleanup(func() { execOutputLimit = old })

	var res Result
	allocated := allocatedDuring(func() {
		res = runCommandTool(t.TempDir(), nil, nil).Handler(context.Background(), map[string]any{
			"command": "head -c 268435456 /dev/zero",
		})
	})
	if res.IsError {
		t.Fatalf("run_command = %+v", res)
	}
	if allocated > 64<<20 {
		t.Fatalf("collecting 256 MiB of output allocated %d bytes", allocated)
	}
}

func TestLocalReadFileRefusesWhatIsNotARegularFile(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Blocks forever in os.ReadFile, which is the bug; the test's own
	// timeout is the failure.
	res := readFileTool(root).Handler(context.Background(), map[string]any{"file_path": "fifo"})
	if !res.IsError || !strings.Contains(res.Text, "not a regular file") {
		t.Fatalf("read_file of a FIFO = %+v, want a not-a-regular-file error", res)
	}
}

func TestLocalReadFileRefusesAFileOverTheCap(t *testing.T) {
	old := maxReadFileBytes
	maxReadFileBytes = 1 << 10
	t.Cleanup(func() { maxReadFileBytes = old })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big"), bytes.Repeat([]byte("a\n"), 1<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	res := readFileTool(root).Handler(context.Background(), map[string]any{"file_path": "big"})
	if !res.IsError || !strings.Contains(res.Text, "file is larger than") {
		t.Fatalf("read_file over the cap = %+v, want a too-large error", res)
	}
}

func TestServeAnswersAnOverlongLineAndCarriesOn(t *testing.T) {
	in := strings.Repeat("x", 1<<20) + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	_ = serve(context.Background(), NewRegistry(), bufio.NewReaderSize(strings.NewReader(in), 64), w, 1<<10)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2: %q", len(lines), out.String())
	}
	var first, second rpcResponse
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Error == nil || first.Error.Code != -32600 {
		t.Fatalf("first response = %s, want a -32600 error", lines[0])
	}
	if !strings.Contains(lines[0], `"id":null`) {
		t.Errorf("first response = %s, want id null: JSON-RPC 2.0 requires it when the request's id is unknown", lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil || second.Error != nil || string(second.ID) != "1" {
		t.Fatalf("second response = %s, want the tools/list answer", lines[1])
	}
}

func TestHandleAnswersAnUnparsableLineWithANullID(t *testing.T) {
	got := string(NewRegistry().Handle(context.Background(), []byte("{not json")))
	if !strings.Contains(got, `"code":-32700`) || !strings.Contains(got, `"id":null`) {
		t.Fatalf("answer to an unparsable line = %s, want a -32700 error with id null", got)
	}
}

// A command whose output the runner already cut is reported at its real
// size: the runner's marker is in the middle the answer elides, so the
// answer's own notice has to carry what it said.
func TestRunCommandElisionCountsWhatTheRunnerDropped(t *testing.T) {
	old := execOutputLimit
	execOutputLimit = 1 << 20
	t.Cleanup(func() { execOutputLimit = old })

	res := runCommandTool(t.TempDir(), nil, nil).Handler(context.Background(), map[string]any{
		"command": "head -c 10485760 /dev/zero | tr '\\0' a; echo; echo end",
	})
	if res.IsError {
		t.Fatalf("run_command = %+v", res)
	}
	if strings.Contains(res.Text, "bytes of output dropped") {
		t.Fatalf("the runner's own marker survived the answer's elision, so this test proves nothing:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "of 10.0 MB "+elisionMarker) {
		t.Errorf("the elision notice does not count the 10 MB the command printed:\n%s", res.Text[strings.Index(res.Text, "[grain]"):])
	}
	if !strings.HasSuffix(strings.TrimSpace(strings.SplitN(res.Text, "stderr:", 2)[0]), "end") {
		t.Errorf("the tail of the output is missing:\n%s", res.Text)
	}
}

func TestRunnerDroppedReadsTheMarkerBack(t *testing.T) {
	old := execOutputLimit
	execOutputLimit = 1 << 10
	t.Cleanup(func() { execOutputLimit = old })

	b := newHeadTailBuffer()
	_, _ = b.Write(bytes.Repeat([]byte("x"), 5000))
	out := b.String()
	dropped, markerLen := runnerDropped(out)
	if dropped != 5000-1<<10 || len(out) != 1<<10+markerLen {
		t.Errorf("runnerDropped = %d, %d for %d bytes of output; want %d dropped and the rest the marker", dropped, markerLen, len(out), 5000-1<<10)
	}
	if d, _ := runnerDropped(strings.Repeat("x", 4000)); d != 0 {
		t.Errorf("runnerDropped of output nothing cut = %d, want 0", d)
	}
}
