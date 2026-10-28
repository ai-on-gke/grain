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

package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

func instant(t time.Time) *time.Time { return &t }

// printed runs fn and returns what it wrote to out.
func printed(out *bytes.Buffer, fn func()) string {
	out.Reset()
	fn()
	return out.String()
}

// liveRun is one run still going, with a step still printing: what a
// follower is fed, poll after poll, with a longer `output` each time.
func liveRun(output string, done bool) ci.Run {
	started := time.Now().Add(-time.Minute)
	step := ci.StepRun{Name: "go test ./...", StartedAt: &started, ExitCode: -1, Output: output}
	run := ci.Run{
		ID: "9f2c1a", Repo: "acme/widgets", Ref: "main", StartedAt: &started,
		Jobs: []ci.JobRun{{Workflow: "tests", WorkflowPath: "tests.yml", JobID: "test", Name: "go test",
			StartedAt: &started, Steps: []ci.StepRun{
				{Name: "build", Conclusion: ci.ConclusionSuccess, Output: "compiled it"},
				step,
			}}},
	}
	if done {
		run.Conclusion, run.FinishedAt = ci.ConclusionSuccess, instant(time.Now())
		run.Jobs[0].Conclusion, run.Jobs[0].FinishedAt = ci.ConclusionSuccess, instant(time.Now())
		run.Jobs[0].Steps[1].Conclusion, run.Jobs[0].Steps[1].ExitCode = ci.ConclusionSuccess, 0
	}
	return run
}

// The whole of what -follow is: the second poll prints what the first did
// not, and not the line it already printed. A follower that reprinted its
// source every poll would be `grain ci show` in a loop, which is the
// thing a reader was already doing by hand.
func TestFollowerPrintsOnlyWhatIsNew(t *testing.T) {
	var out bytes.Buffer
	f := NewFollower(&out, OutputFailed)

	first := printed(&out, func() { f.Follow(liveRun("ok pkg/a 0.2s\n", false)) })
	if !strings.Contains(first, "ok pkg/a 0.2s") {
		t.Errorf("the first poll should print what the step has printed so far:\n%s", first)
	}
	if !strings.Contains(first, "=== job go test, step go test ./... ===") {
		t.Errorf("output should say which step it came from:\n%s", first)
	}
	if strings.Contains(first, "compiled it") {
		t.Errorf("a finished green step's output is not what -follow is for:\n%s", first)
	}

	second := printed(&out, func() { f.Follow(liveRun("ok pkg/a 0.2s\nok pkg/b 1.1s\n", false)) })
	if strings.Contains(second, "pkg/a") {
		t.Errorf("a line already printed should not be printed twice:\n%s", second)
	}
	if !strings.Contains(second, "ok pkg/b 1.1s") {
		t.Errorf("the newest line should be printed:\n%s", second)
	}
	// Same step still writing: a header per poll would break a test
	// suite's own output into five-second paragraphs.
	if strings.Contains(second, "===") {
		t.Errorf("a header belongs to a change of source, not to a poll:\n%s", second)
	}

	// A poll with nothing new in it prints nothing at all, which is what
	// makes a quiet job quiet rather than a stream of blank headers.
	same := printed(&out, func() { f.Follow(liveRun("ok pkg/a 0.2s\nok pkg/b 1.1s\n", false)) })
	if strings.TrimSpace(same) != "" {
		t.Errorf("a poll that carried nothing new should print nothing:\n%q", same)
	}

	// The step goes green, which under -output failed takes it out of
	// what a snapshot would show -- but it is the step being followed,
	// and stopping in the middle of its output would leave the reader
	// with no word on how the thing they were watching ended.
	last := printed(&out, func() { f.Follow(liveRun("ok pkg/a 0.2s\nok pkg/b 1.1s\nPASS\n", true)) })
	if !strings.Contains(last, "PASS") {
		t.Errorf("a step being followed should be followed to the end:\n%s", last)
	}
}

// A step that keeps printing past ci.MaxStepOutput has its output
// rewritten rather than appended -- head kept, middle dropped, newest
// lines kept. A follower resuming at "the byte I stopped at" would print
// the middle of a line and then nothing ever again, so the rewrite is
// detected and said out loud.
func TestFollowerSaysWhenAStepsOutputWasRewritten(t *testing.T) {
	var buf bytes.Buffer
	f := NewFollower(&buf, OutputAll)
	head := strings.Repeat("the head of the log\n", 20)
	middle := strings.Repeat("a line from the middle of a long test log\n", 40)
	printed(&buf, func() { f.Follow(liveRun(head+middle, false)) })

	// What ci.liveOutput hands out once the buffer is full: the head, a
	// note, and the newest lines -- of which the tail overlaps what has
	// already been printed.
	elided := head + "\n\n[grain ci] 65536 bytes elided\n\n" + middle + "the newest line\n"
	out := printed(&buf, func() { f.Follow(liveRun(elided, false)) })
	if !strings.Contains(out, "the newest line") {
		t.Errorf("following should carry on past a rewrite, not go quiet:\n%s", out)
	}
	if !strings.Contains(out, "middle has been dropped") {
		t.Errorf("a rewritten buffer should be reported rather than papered over:\n%s", out)
	}
	if strings.Contains(out, "the head of the log") {
		t.Errorf("the resume point should be where the reader was, not the top:\n%s", out)
	}
}

func TestNewOutput(t *testing.T) {
	long := strings.Repeat("x. a line that is long enough to anchor on\n", 30)
	for _, tc := range []struct {
		name          string
		prev, cur     string
		added         string
		wantRewritten bool
	}{
		{name: "first poll", cur: "hello\n", added: "hello\n"},
		{name: "grown", prev: "a\n", cur: "a\nb\n", added: "b\n"},
		{name: "unchanged", prev: "a\n", cur: "a\n"},
		{name: "half a line", prev: "a\nb", cur: "a\nb c\n", added: " c\n"},
		{name: "rewritten with an overlap", prev: "head\n" + long, cur: "elided\n" + long + "new\n",
			added: "\nnew\n", wantRewritten: true},
		{name: "rewritten past recognition", prev: "head\n" + long, cur: "nothing of it left\n",
			added: "nothing of it left\n", wantRewritten: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			added, rewritten := NewOutput(tc.prev, tc.cur)
			if added != tc.added || rewritten != tc.wantRewritten {
				t.Fatalf("NewOutput = %q, %v; want %q, %v", added, rewritten, tc.added, tc.wantRewritten)
			}
		})
	}
}

// `grain ci watch <id>` is `grain ci show -follow <id>`: it streams the
// output, then prints the run and exits with its verdict the way `grain
