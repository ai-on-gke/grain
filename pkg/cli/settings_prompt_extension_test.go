package cli

// grain/task-114's deployment-wide standing instructions, from the shell
// rather than the Settings pane: the text every run here is given on top
// of the prompt grain builds is exactly the kind of thing a provisioning
// script writes once and an operator reads back from the host while
// working out why every run is doing something unexpected. Same helpers,
// and the same real embedded store, settings_sandbox_shape_test.go
// already uses.

import (
	"context"
	"strings"
	"testing"
)

func TestCmdSettingsSetsAndClearsThePromptExtension(t *testing.T) {
	ctx := context.Background()
	srv := settingsTestServer(t)
	c := accessCodeClient(srv.URL)

	const text = "Run `make lint` before you push."
	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-prompt-extension", text}); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PromptExtension != text {
		t.Fatalf("PromptExtension = %q, want %q", settings.PromptExtension, text)
	}

	// An unrelated flag leaves it alone -- the same fs.Visit
	// nil-means-unchanged contract every other setting here gets, and the
	// one that matters most for a field a script rewrites on every run.
	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-max-workers", "3"}); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	settings, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PromptExtension != text {
		t.Fatalf("PromptExtension after an unrelated save = %q, want it untouched", settings.PromptExtension)
	}

	// And an empty one turns the feature back off, the way an empty
	// -environment-name unnames a deployment.
	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-prompt-extension", ""}); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	settings, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PromptExtension != "" {
		t.Fatalf("PromptExtension after clearing = %q, want empty", settings.PromptExtension)
	}
}

// Printed in full rather than summarised as set/none: "why is every agent
// doing X" is answered by reading the words. And a deployment that has
// written none says so, rather than printing a label with a blank after
// it that reads as a rendering bug.
func TestCmdSettingsPrintsThePromptExtension(t *testing.T) {
	ctx := context.Background()
	srv := settingsTestServer(t)
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	if !strings.Contains(out, "prompt extension: none") {
		t.Errorf("`grain settings` printed %q, which does not say the deployment has no prompt extension", out)
	}

	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{},
			[]string{"-prompt-extension", "Run `make lint` before you push.\nSay what you tried."}); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	// Both lines, each indented under a label on its own line: a
	// multi-line value written after "prompt extension: " would put its
	// first line in the column and the rest hard against the left margin.
	for _, want := range []string{"prompt extension:\n", "  Run `make lint` before you push.\n", "  Say what you tried.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("`grain settings` printed %q, which does not contain %q", out, want)
		}
	}
}

// promptExtensionBlock is what both `grain settings` and `grain repo
// prompt-extension` render every layer with, so its two shapes are worth
// pinning directly rather than only through one caller's output.
func TestPromptExtensionBlock(t *testing.T) {
	t.Parallel()
	if got := promptExtensionBlock("own", ""); got != "own: none\n" {
		t.Errorf("promptExtensionBlock with no text = %q, want %q", got, "own: none\n")
	}
	// Whitespace is not a layer anywhere else (model.PromptExtensionFor
	// trims every layer before reading it), so it must not print as one
	// here either -- a label followed by two indented blank lines would
	// say a deployment has instructions it does not have.
	if got := promptExtensionBlock("own", "  \n\t"); got != "own: none\n" {
		t.Errorf("promptExtensionBlock with only whitespace = %q, want %q", got, "own: none\n")
	}
	want := "own:\n  first line\n  second line\n"
	if got := promptExtensionBlock("own", "first line\nsecond line"); got != want {
		t.Errorf("promptExtensionBlock = %q, want %q", got, want)
	}
}
