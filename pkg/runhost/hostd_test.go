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

package runhost

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func TestDecideHostd(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	local := hostd.ThisHello("build-a")
	proc := func(id, run string, exited bool, deadline time.Time) hostd.Process {
		p := hostd.Process{ID: id, Run: run, Task: "t" + run, Kind: hostd.KindCLI, Name: "agy", PID: 100,
			Detached: true, Deadline: deadline, Spool: "/spool/" + id, Exited: exited}
		if exited {
			p.Exit = &hostd.Exit{Code: 0}
		}
		return p
	}
	later := now.Add(time.Hour)
	type want struct {
		adopt   []string // run ids
		release []string // hostd ids
		finish  []string // "run: reason fragment"
	}
	cases := []struct {
		name  string
		rows  []Claim
		procs []hostd.Process
		peer  hostd.Hello
		want  want
	}{
		{"matched: adopted, with the gap credited",
			[]Claim{{Run: "1", LastSeen: now.Add(-90 * time.Second)}}, []hostd.Process{proc("p1", "1", false, later)}, local,
			want{adopt: []string{"1"}}},
		{"matched, ended while no daemon was attached: adopted, its spool is final",
			[]Claim{{Run: "1"}}, []hostd.Process{proc("p1", "1", true, later)}, local,
			want{adopt: []string{"1"}}},
		{"unmatched: released",
			nil, []hostd.Process{proc("p9", "9", false, later)}, local,
			want{release: []string{"p9"}}},
		{"matched, still running past its deadline: finished and released",
			[]Claim{{Run: "1"}}, []hostd.Process{proc("p1", "1", false, now.Add(-time.Minute))}, local,
			want{release: []string{"p1"}, finish: []string{"1: outlived its own deadline"}}},
		{"protocol skew of one: adopted",
			[]Claim{{Run: "1"}}, []hostd.Process{proc("p1", "1", false, later)},
			hostd.Hello{Protocol: hostd.ProtocolVersion + 1, MinCompatible: hostd.ProtocolVersion, Build: "build-b"},
			want{adopt: []string{"1"}}},
		{"protocol skew past what either side serves: finished with the mismatch, released",
			[]Claim{{Run: "1"}, {Run: "2"}}, []hostd.Process{proc("p1", "1", false, later), proc("p9", "9", false, later)},
			hostd.Hello{Protocol: hostd.ProtocolVersion + 2, MinCompatible: hostd.ProtocolVersion + 1, Build: "build-c"},
			want{release: []string{"p1", "p9"}, finish: []string{"1: hostd is updating or mismatched"}}},
		{"a protocol-1 start is not adoptable and not listed",
			nil, []hostd.Process{{ID: "p5", Run: "5", PID: 5}}, local,
			want{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := DecideHostd(c.rows, c.procs, local, c.peer, now)
			var adopt, release, finish []string
			for _, a := range plan.Adopt {
				adopt = append(adopt, a.Claim.Run)
				if id, ok := HostdID(a.Host); !ok || a.Host.Labels.Spool != "/spool/"+id {
					t.Errorf("adopted %+v does not name its hostd process and spool", a.Host)
				}
				if !a.Claim.LastSeen.IsZero() && a.Gap != now.Sub(a.Claim.LastSeen) {
					t.Errorf("gap = %s", a.Gap)
				}
			}
			for _, r := range plan.Release {
				id, ok := HostdID(r.Host)
				if !ok {
					t.Errorf("release %q is not a hostd process", r.Host.Name)
				}
				release = append(release, id)
			}
			for _, f := range plan.Finish {
				finish = append(finish, f.Claim.Run+": "+f.Reason)
			}
			slices.Sort(release)
			if !slices.Equal(adopt, c.want.adopt) || !slices.Equal(release, c.want.release) || len(finish) != len(c.want.finish) {
				t.Fatalf("plan: adopt %v release %v finish %v; want %+v", adopt, release, finish, c.want)
			}
			for i, w := range c.want.finish {
				run, frag, _ := strings.Cut(w, ": ")
				if !strings.HasPrefix(finish[i], run+": ") || !strings.Contains(finish[i], frag) {
					t.Errorf("finish[%d] = %q, want run %s and %q", i, finish[i], run, frag)
				}
			}
		})
	}
}
