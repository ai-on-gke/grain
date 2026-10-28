//go:build linux

package procgroup

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestSacrificeOnOOMWritesPositiveScore(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if err := SacrificeOnOOM(cmd.Process.Pid); err != nil {
		t.Fatalf("SacrificeOnOOM(%d): %v", cmd.Process.Pid, err)
	}

	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid))
	if err != nil {
		t.Fatalf("reading oom_score_adj: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), strconv.Itoa(ChildOOMScoreAdj); got != want {
		t.Errorf("oom_score_adj = %q, want %q", got, want)
	}
}
