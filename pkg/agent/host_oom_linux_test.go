//go:build linux

package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/procgroup"
)

func TestLocalHostRunResetsChildOOMScoreAdjToPositive(t *testing.T) {
	h := LocalHost{Logf: func(string, ...any) {}}
	out, err := h.Run(context.Background(), Command{
		Path: "sh",
		Args: []string{"-c", "sleep 0.05 && cat /proc/self/oom_score_adj"},
	}, nil)
	if err != nil {
		t.Fatalf("LocalHost.Run: %v", err)
	}
	if got, want := strings.TrimSpace(out), strconv.Itoa(procgroup.ChildOOMScoreAdj); got != want {
		t.Errorf("child oom_score_adj = %q, want %q", got, want)
	}
}
