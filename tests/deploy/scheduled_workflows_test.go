package deploy

import (
	"os"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/packagepublish"
	"github.com/bwsalmon/grain/pkg/ci"
)

// Grain reads a cron under .grain/workflows as a task schedule of its own
// (pkg/repodecl's workflows.go), and .grain/workflows/nightly.yml is what
// fires the long suite on this deployment.
func TestTheNightlysVerdictIsBackedByAGrainWorkflowThatIsActuallyScheduled(t *testing.T) {
	workflows, err := ci.LoadNativeDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path != ".grain/workflows/nightly.yml" {
			continue
		}
		if len(w.On.Schedule) == 0 {
			t.Fatalf("%s declares no `on: schedule`, so nothing fires the long suite on this deployment", w.Path)
		}
		return
	}
	t.Fatal("no .grain/workflows/nightly.yml: nothing runs the long suite on this deployment")
}

// The publishing credential is refused to a scheduled run, because
// `schedule` is accepted by ci.Request.Validate from any caller.
func TestThePublishingCredentialStillRefusesAScheduledRun(t *testing.T) {
	p := &packagepublish.Provider{}
	if p.SupportsCIEvent(string(ci.EventSchedule)) {
		t.Errorf("package-publish now accepts %s runs, so a scheduled grain workflow could hold the credential "+
			"that deletes package versions: revisit whether `schedule` is a trusted event at all "+
			"(ci.Request.Validate accepts it from any caller)",
			ci.EventSchedule)
	}
	for _, event := range []ci.Event{ci.EventRelease, ci.EventManual} {
		if !p.SupportsCIEvent(string(event)) {
			t.Errorf("package-publish no longer accepts %s runs, which is where the release publishes from", event)
		}
	}
}
