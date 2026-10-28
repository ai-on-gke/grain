package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// read_grain_metrics' checks section names the red-CI procedure's two
// shares, so a deployment can watch them move after the prompt changed.
func TestPrintChecksReportsTheRedCIProcedure(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printChecks(&out, ui.MetricsChecks{RedCI: ui.MetricsRedCI{
		FixingRuns: 4, Narrowed: 1, NarrowedShare: 0.25, Selective: 3, SelectiveShare: 0.75,
	}})
	text := out.String()
	for _, want := range []string{
		"red-CI procedure (4 CI-fixing run(s)",
		"1 (25%) ran a narrowed local test before the fixing push",
		"3 (75%) used run_ci_job, retrigger_ci or wait_for_checks with jobs",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("checks section is missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	printChecks(&out, ui.MetricsChecks{})
	if out.Len() != 0 {
		t.Errorf("a window with nothing to report printed:\n%s", out.String())
	}
}
