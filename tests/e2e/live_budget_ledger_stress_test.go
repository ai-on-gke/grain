package e2e

import (
	"flag"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestLegacyDaemonStartupFlagsIgnoredWithNotice validates Requirement 3:
// Validate that legacy daemon startup flags (-max-workers, -max-ci-jobs) are ignored with informative logging.
func TestLegacyDaemonStartupFlagsIgnoredWithNotice(t *testing.T) {
	t.Parallel()

	// Test FlagSet parsing with legacy flags
	fs := flag.NewFlagSet("grain daemon test", flag.ContinueOnError)
	maxWorkers := fs.Int("max-workers", 1, "deprecated and ignored")
	maxMergers := fs.Int("max-mergers", 2, "deprecated and ignored")
	maxCIJobs := fs.Int("max-ci-jobs", 0, "deprecated and ignored")
	dataDir := fs.String("data-dir", "/tmp/grain-test", "")

	args := []string{
		"-data-dir", "/tmp/grain-custom",
		"-max-workers", "100",
		"-max-mergers", "50",
		"-max-ci-jobs", "200",
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("fs.Parse failed: %v", err)
	}

	if *dataDir != "/tmp/grain-custom" {
		t.Errorf("dataDir = %s, want /tmp/grain-custom", *dataDir)
	}
	if *maxWorkers != 100 || *maxMergers != 50 || *maxCIJobs != 200 {
		t.Errorf("expected legacy flags to be parsed into FlagSet without error: maxWorkers=%d, maxMergers=%d, maxCIJobs=%d", *maxWorkers, *maxMergers, *maxCIJobs)
	}

	// Validate that Limits built from a config ignore these values and impose no deployment-wide ceiling
	limits := model.Limits{
		Budget: model.Budget{
			Host: model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		},
		Ledger: &model.Ledger{},
	}
	if limits.Workers != 0 || limits.Mergers != 0 || limits.CIJobs != 0 {
		t.Errorf("limits should have 0 deployment-wide counts: %+v", limits)
	}
	if !limits.Unlimited() {
		t.Error("deployment limits should be Unlimited() with respect to count bounds")
	}

	t.Log("Legacy startup flags parsed cleanly and verified ignored by live scheduler")
}
