package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// grain/task-322: a task that names no zone of its own -- one nobody
// filed from a browser -- is told the time on the deployment's clock
// before UTC, and one that does name a zone keeps it.
func TestATaskWithNoZoneIsToldTheDeploymentsClock(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	direct := model.Task{Interactive: true}

	if got := timezoneSectionAt(withDeploymentTimeZone(direct, "America/Los_Angeles"), now); !strings.Contains(got, "User timezone: America/Los_Angeles") {
		t.Fatalf("no task zone, deployment LA: %q", got)
	}
	own := direct
	own.TimeZone = "Europe/London"
	if got := timezoneSectionAt(withDeploymentTimeZone(own, "America/Los_Angeles"), now); !strings.Contains(got, "User timezone: Europe/London") {
		t.Fatalf("task zone London, deployment LA: %q", got)
	}
	if got := timezoneSectionAt(withDeploymentTimeZone(direct, ""), now); !strings.Contains(got, "User timezone: UTC") {
		t.Fatalf("neither set: %q", got)
	}
}
