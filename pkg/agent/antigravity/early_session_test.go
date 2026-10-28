package antigravity

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The conversation agy names on its first event is reported as it
// arrives (agent.RunConfig.OnResumableSession), under the same rule as
// the one on the Result: only from a HOME that outlives the run.
func TestRunReportsItsConversationAsItIsNamedOnlyFromAKeptHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		taskID string
		want   string
	}{
		{"the task's own home", "42", "conv-1"},
		{"a temp home", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: stream(conversationInit("conv-1"), successResult("done"))},
			}}
			f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))
			var reported []string
			if _, err := f.Run(context.Background(), agent.RunConfig{
				Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: tc.taskID,
				OnResumableSession: func(s string) { reported = append(reported, s) },
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			var got string
			if len(reported) == 1 {
				got = reported[0]
			} else if len(reported) > 1 {
				t.Fatalf("reported %q, want at most one conversation", reported)
			}
			if got != tc.want {
				t.Errorf("reported %q, want %q", got, tc.want)
			}
		})
	}
}
