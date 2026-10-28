// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/procgroup"
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
