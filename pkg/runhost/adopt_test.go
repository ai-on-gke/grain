package runhost

import (
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	host := func(name, run string, protocol int, running bool) Info {
		return Info{Name: name, Running: running, Labels: Labels{Deployment: "d", Run: run, Protocol: protocol,
			Spool: "/spool/" + name, Deadline: now.Add(time.Hour)}}
	}
	exited := map[string]bool{}
	spool := func(i Info) SpoolState { return SpoolState{Exited: exited[i.Name]} }
	exited["done"] = true

	type want struct {
		adopt   []string // run ids
		release []string // host names
		finish  []string // "run: reason fragment"
	}
	cases := []struct {
		name  string
		rows  []Claim
		hosts []Info
		want  want
	}{
		{"live row, running host: adopted",
			[]Claim{{Run: "1"}}, []Info{host("h1", "1", Protocol, true)},
			want{adopt: []string{"1"}}},
		{"live row, host whose CLI finished while no daemon ran: adopted, its spool is final",
			[]Claim{{Run: "1"}}, []Info{host("done", "1", Protocol, false)},
			want{adopt: []string{"1"}}},
		{"live row, host stopped with no exit record: finished and freed",
			[]Claim{{Run: "1"}}, []Info{host("h1", "1", Protocol, false)},
			want{release: []string{"h1"}, finish: []string{"1: without recording"}}},
		{"live row, host of the next protocol: refused by name",
			[]Claim{{Run: "1"}}, []Info{host("h1", "1", Protocol+1, true)},
			want{release: []string{"h1"}, finish: []string{"1: protocol"}}},
		{"live row, no host: finished with the reason",
			[]Claim{{Run: "1"}}, nil,
			want{finish: []string{"1: no run host"}}},
		{"host no row claims: freed",
			nil, []Info{host("h9", "9", Protocol, true)},
			want{release: []string{"h9"}}},
		{"live row, host past its deadline label whose CLI finished in time: adopted, its spool is final",
			[]Claim{{Run: "1", LastSeen: now.Add(-time.Minute)}},
			[]Info{{Name: "done", Running: false, Labels: Labels{Deployment: "d", Run: "1", Protocol: Protocol,
				Spool: "/spool/done", Deadline: now.Add(-time.Second)}}},
			want{adopt: []string{"1"}}},
		{"two hosts for one row: the running one adopted, the other freed",
			[]Claim{{Run: "1"}}, []Info{host("old", "1", Protocol, false), host("new", "1", Protocol, true)},
			want{adopt: []string{"1"}, release: []string{"old"}}},
	}
	if Protocol > 1 {
		cases = append(cases, struct {
			name  string
			rows  []Claim
			hosts []Info
			want  want
		}{"live row, host of the previous protocol: adopted",
			[]Claim{{Run: "1"}}, []Info{host("h1", "1", Protocol-1, true)},
			want{adopt: []string{"1"}}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Decide(c.rows, c.hosts, spool, now)
			var adopt, release, finish []string
			for _, a := range p.Adopt {
				adopt = append(adopt, a.Claim.Run)
			}
			for _, r := range p.Release {
				release = append(release, r.Host.Name)
			}
			for _, f := range p.Finish {
				finish = append(finish, f.Claim.Run+": "+f.Reason)
			}
			if strings.Join(adopt, ",") != strings.Join(c.want.adopt, ",") {
				t.Errorf("adopt = %v, want %v", adopt, c.want.adopt)
			}
			if strings.Join(release, ",") != strings.Join(c.want.release, ",") {
				t.Errorf("release = %v, want %v", release, c.want.release)
			}
			if len(finish) != len(c.want.finish) {
				t.Fatalf("finish = %v, want %v", finish, c.want.finish)
			}
			for i, f := range c.want.finish {
				run, frag, _ := strings.Cut(f, ": ")
				if !strings.HasPrefix(finish[i], run+": ") || !strings.Contains(finish[i], frag) {
					t.Errorf("finish[%d] = %q, want run %s with %q", i, finish[i], run, frag)
				}
			}
		})
	}
}

func TestDecideCreditsTheGapAndKeepsTheDeadline(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	h := Info{Name: "h", Running: true, Labels: Labels{Run: "1", Protocol: Protocol, Deadline: now.Add(time.Minute)}}
	p := Decide([]Claim{{Run: "1", LastSeen: now.Add(-90 * time.Second)}}, []Info{h},
		func(Info) SpoolState { return SpoolState{} }, now)
	if len(p.Adopt) != 1 || p.Adopt[0].Gap != 90*time.Second || !p.Adopt[0].Host.Labels.Deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("plan = %+v", p)
	}
	late := Decide([]Claim{{Run: "1"}}, []Info{h}, func(Info) SpoolState { return SpoolState{} }, now.Add(2*time.Minute))
	if len(late.Adopt) != 0 || len(late.Finish) != 1 || !strings.Contains(late.Finish[0].Reason, "deadline") {
		t.Fatalf("a run host past its deadline label must be finished, plan = %+v", late)
	}
}
