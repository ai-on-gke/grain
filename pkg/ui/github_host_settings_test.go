package ui_test

// githubHost is a bare host with an optional :port: the git proxy forwards
// to it as it stands and github.APIHost maps exactly "github.com" to the
// API host. The 2026-10-05 deployment-operations walkthrough saved
// "https://github.example.com/" and "bad host" from Admin → Integrations
// & Auth; both were stored and reported as waiting for a restart, after
// which every GitHub call the daemon made would have gone to a host that
// does not exist.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestUpdateSettingsRefusesAGitHubHostThatIsNotAHost(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"https://github.example.com/",
		"http://127.0.0.1:8080",
		"github.example.com/api/v3",
		"bad host",
		"ghe.example.com:",
		"ghe.example.com:https",
		"ghe.example.com:70000",
		"user@github.com",
		"",
		"   ",
	} {
		t.Run(bad, func(t *testing.T) {
			host := bad
			_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GitHubHost: &host})
			if err == nil {
				t.Fatalf("UpdateSettings stored githubHost %q, want a validation error", bad)
			}
			if !strings.Contains(err.Error(), "githubHost") {
				t.Errorf("error %v does not name githubHost, which is the field a form has to mark", err)
			}
		})
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.GitHubHost != "github.com" {
		t.Fatalf("githubHost = %q after the refusals, want the github.com stored before them", read.GitHubHost)
	}

	// The URL refusal names the host the operator meant.
	url := "https://ghe.example.com/"
	_, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GitHubHost: &url})
	if err == nil || !strings.Contains(err.Error(), `"ghe.example.com"`) {
		t.Errorf("error for %q = %v, want it to suggest \"ghe.example.com\"", url, err)
	}
}

func TestUpdateSettingsStoresAGitHubHostTrimmed(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{
		"ghe.example.com":        "ghe.example.com",
		" ghe.example.com:8443 ": "ghe.example.com:8443",
		"127.0.0.1:9":            "127.0.0.1:9",
		"[::1]:8443":             "[::1]:8443",
		"github.com":             "github.com",
	} {
		host := raw
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{GitHubHost: &host}); err != nil {
			t.Fatalf("UpdateSettings(githubHost %q): %v", raw, err)
		}
		read, err := c.GetSettings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if read.GitHubHost != want {
			t.Errorf("githubHost %q stored as %q, want %q", raw, read.GitHubHost, want)
		}
	}
}
