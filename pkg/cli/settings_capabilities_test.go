package cli

// `grain settings` is the only place a shell on a deployment's own host
// can ask why a capability a task was granted never did anything. These
// cover the line it prints per capability -- in particular that the two
// kinds of gap stay distinguishable, since "this deployment is missing a
// secret" and "grain's own code offers no way to grant this" are fixed
// in different places and reading one as the other is what makes a
// configured-but-ungrantable gcp-key look like a configuration problem.

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestCapabilityStatusLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		status  ui.CapabilityStatus
		want    []string
		wantNot []string
	}{
		{
			name:    "ready and grantable says only that",
			status:  ui.CapabilityStatus{ID: "gemini-key", Ready: true, Grantable: true},
			want:    []string{"gemini-key", "ready"},
			wantNot: []string{"not ready", "NOT GRANTABLE", "needs:", "missing secrets:"},
		},
		{
			name: "an unconfigured capability names what it needs",
			status: ui.CapabilityStatus{
				ID: "gcp-key", Grantable: true,
				MissingConfig:  []string{"GCP project", "GCP service account email"},
				MissingSecrets: []string{"gcp-key-minter"},
			},
			want:    []string{"gcp-key", "not ready", "needs: GCP project, GCP service account email", "missing secrets: gcp-key-minter"},
			wantNot: []string{"NOT GRANTABLE"},
		},
		{
			// A setting saved that this daemon is not running with:
			// there is nothing to type in, so "needs:" would be the
			// wrong instruction and the restart is the right one.
			name: "a capability whose stored setting is unapplied says so, not \"needs\"",
			status: ui.CapabilityStatus{
				ID: "gemini-key", Grantable: true, PendingConfig: []string{"GCP project"},
			},
			want: []string{
				"gemini-key", "not ready",
				"saved but not yet in effect: GCP project", "restart the daemon",
			},
			wantNot: []string{"needs:"},
		},
		{
			// The case this exists for: nothing is missing, so every
			// other signal available reads "working".
			name:    "a ready capability nothing can grant still says so",
			status:  ui.CapabilityStatus{ID: "gcp-key", Ready: true, Grantable: false},
			want:    []string{"gcp-key", "ready", "NOT GRANTABLE"},
			wantNot: []string{"needs:", "missing secrets:"},
		},
		{
			// A capability every new task is filed holding
			// (model.Config.DefaultCapabilities) says so, and says it
			// before whatever is missing: an unready default is a
			// deployment-wide problem rather than a per-task one.
			name:   "a defaulted capability that is not ready says both",
			status: ui.CapabilityStatus{ID: "gcp-key", Grantable: true, Default: true, MissingConfig: []string{"GCP project"}},
			want:   []string{"gcp-key", "not ready", "default -- every new task is filed with this", "needs: GCP project"},
		},
		{
			name:    "a capability that is not defaulted says nothing about defaults",
			status:  ui.CapabilityStatus{ID: "gemini-key", Ready: true, Grantable: true},
			wantNot: []string{"default"},
		},
		{
			// grain/task-24: the per-repo layer names the repos rather
			// than repeating a bare "default", so a line never reads as
			// a deployment-wide default that only some tasks get.
			name: "a capability defaulted only on some repos names them",
			status: ui.CapabilityStatus{
				ID: "gcp-key", Ready: true, Grantable: true,
				DefaultRepos: []string{"acme/gadgets", "acme/widgets"},
			},
			want:    []string{"gcp-key", "default in: acme/gadgets, acme/widgets"},
			wantNot: []string{"default -- every new task is filed with this"},
		},
		{
			// Both layers, on one capability: a repo can restate one the
			// deployment already gives, and dropping the deployment-wide
			// entry would leave that repo's own standing.
			name: "a capability defaulted deployment-wide and on a repo says both",
			status: ui.CapabilityStatus{
				ID: "gcp-key", Ready: true, Grantable: true,
				Default: true, DefaultRepos: []string{"acme/widgets"},
			},
			want: []string{"default -- every new task is filed with this", "default in: acme/widgets"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityStatusLine(tc.status)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("line %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(got, unwanted) {
					t.Errorf("line %q unexpectedly contains %q", got, unwanted)
				}
			}
		})
	}
}
