package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestResumeLaterTakesEachFormOfWhen(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	hour := now.Add(time.Hour)
	for name, tc := range map[string]struct {
		when map[string]any
		want model.ResumeOn
	}{
		"next cycle": {nil, model.ResumeOn{}},
		"ci":         {map[string]any{"ci": "my-branch"}, model.ResumeOn{CI: model.ResumeOnMyBranch}},
		"tasks merged": {map[string]any{"task": []any{"#12", "13", "12"}, "until": "merged"},
			model.ResumeOn{Tasks: []string{"12", "13"}, UntilMerged: true}},
		"at or message": {map[string]any{"at": "1h", "message": true},
			model.ResumeOn{At: &hour, Message: true}},
	} {
		t.Run(name, func(t *testing.T) {
			w := &fakeTaskWaiter{closeAfter: -1}
			args := map[string]any{"reason": "waiting"}
			if tc.when != nil {
				args["when"] = tc.when
			}
			res := resumeLater(context.Background(), w, nil, args, now)
			if res.IsError {
				t.Fatalf("refused: %s", res.Text)
			}
			if len(w.resumed) != 1 {
				t.Fatalf("ResumeLater called %d times, want once", len(w.resumed))
			}
			got := w.resumed[0]
			if got.JSON() != tc.want.JSON() {
				t.Fatalf("asked for %s, want %s", got.JSON(), tc.want.JSON())
			}
			taken, ok := ResumeLaterTaken(res.Text)
			if !ok || taken.JSON() != tc.want.JSON() {
				t.Fatalf("result %q reads back as (%s, %v), want %s", res.Text, taken.JSON(), ok, tc.want.JSON())
			}
			if !WaitForTaskParked(nil, res.Text) {
				t.Fatalf("result %q does not open with the park marker", res.Text)
			}
		})
	}
}

func TestResumeLaterRefuses(t *testing.T) {
	now := time.Now()
	for name, args := range map[string]map[string]any{
		"no reason":          {"when": map[string]any{"ci": "x"}},
		"task and more":      {"reason": "r", "when": map[string]any{"task": "12", "message": true}},
		"bad until":          {"reason": "r", "when": map[string]any{"task": "12", "until": "soon"}},
		"bad at":             {"reason": "r", "when": map[string]any{"at": "tomorrow"}},
		"daemon refuses":     {"reason": "r", "when": map[string]any{"message": true}},
		"unpushed, no force": {"reason": "r"},
	} {
		t.Run(name, func(t *testing.T) {
			w := &fakeTaskWaiter{closeAfter: -1}
			var unpushed UnpushedWorkReader
			switch name {
			case "daemon refuses":
				w.refuse = errors.New("no")
			case "unpushed, no force":
				unpushed = func(context.Context) (string, error) { return "unpushed commit: abc wip", nil }
			}
			res := resumeLater(context.Background(), w, unpushed, args, now)
			if !res.IsError {
				t.Fatalf("accepted: %s", res.Text)
			}
			if _, ok := ResumeLaterTaken(res.Text); ok {
				t.Fatalf("a refusal reads as a resume: %s", res.Text)
			}
			if name == "unpushed, no force" && !strings.HasPrefix(res.Text, "Not stopped") {
				t.Fatalf("refusal = %q", res.Text)
			}
		})
	}
	if res := resumeLater(context.Background(), nil, nil, map[string]any{"reason": "r"}, now); !res.IsError {
		t.Fatal("a server with no daemon took a resume_later")
	}
}

// wait_for_task's two parks are resume_later's task and at forms.
func TestWaitForTaskParksAreResumeLater(t *testing.T) {
	ctx := context.Background()
	w := &fakeTaskWaiter{closeAfter: -1}
	if res := callWaitForTask(t, ctx, w, nil, map[string]any{"task_id": "12", "park": true}); res.IsError {
		t.Fatal(res.Text)
	}
	if res := callWaitForTask(t, ctx, w, nil, map[string]any{"resume_at": "2h"}); res.IsError {
		t.Fatal(res.Text)
	}
	if len(w.resumed) != 2 || len(w.resumed[0].Tasks) != 1 || w.resumed[0].Tasks[0] != "12" || w.resumed[1].At == nil {
		t.Fatalf("ResumeLater calls = %+v, want one on task 12 then one at a time", w.resumed)
	}
}
