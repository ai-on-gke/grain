package cli

// `grain comment <id> -attach path body...` -- the order a flag
// naturally gets typed in, and the one Go's flag package cannot parse,
// since it stops looking for flags at the first positional argument.
// Before this check the command filed a comment whose body was the
// literal words "-attach path body...", with the file silently not
// attached (found by hand against a real deployment, task 244).

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestCommentRefusesAFlagWrittenAfterTheTaskID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := settingsTestServer(t)
	c := accessCodeClient(srv.URL)

	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "a task", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	err = cmdComment(ctx, c, &printer{}, []string{created.ID, "-attach", "/tmp/shot.png", "look at this"})
	if err == nil {
		t.Fatal("cmdComment filed the flag as prose instead of saying where it goes")
	}
	if !strings.Contains(err.Error(), "-attach") || !strings.Contains(err.Error(), "before the task id") {
		t.Errorf("error = %q, want it to name the flag and where it belongs", err)
	}

	// And nothing was filed: the point is that the mistake is refused,
	// not that it is refused after a comment nobody wanted.
	task, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.Comments) != 0 {
		t.Errorf("comments = %d, want none filed", len(task.Comments))
	}

	// Prose still gets through: a quoted body is one argument, so it
	// never looks like a flag however it starts.
	if err := cmdComment(ctx, c, &printer{}, []string{created.ID, "-attach is the flag for that"}); err != nil {
		t.Fatalf("cmdComment on a body that merely mentions a flag: %v", err)
	}
}

func TestMisplacedFlagOnlyMatchesThisCommandsOwnFlags(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("attach", "", "")

	for _, tc := range []struct {
		args []string
		want string
		ok   bool
	}{
		{args: []string{"a body"}, ok: false},
		{args: []string{"-attach", "f"}, want: "attach", ok: true},
		{args: []string{"--attach=f"}, want: "attach", ok: true},
		{args: []string{"body", "-attach", "f"}, want: "attach", ok: true},
		{args: []string{"-unknown", "f"}, ok: false},
		{args: []string{"--"}, ok: false},
		{args: []string{"-"}, ok: false},
	} {
		got, ok := misplacedFlag(fs, tc.args)
		if ok != tc.ok || got != tc.want {
			t.Errorf("misplacedFlag(%q) = %q, %v; want %q, %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}
