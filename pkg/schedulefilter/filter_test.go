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

package schedulefilter

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func ptr(t time.Time) *time.Time { return &t }

var facts = []Facts{
	{ID: "sched-a", Kind: KindSchedule, Name: "Nightly deps", Source: "bwsalmon/grain", DeclaredPath: ".grain/schedules/nightly.yml",
		Type: TypeTemplate, Repo: "bwsalmon/grain", Template: "deps", Enabled: true, Cadence: "daily",
		Next: now.Add(2 * time.Hour), Last: ptr(now.Add(-22 * time.Hour)), LastOutcome: OutcomeFired},
	{ID: "sched-b", Kind: KindSchedule, Name: "Candidate cut", Source: SourceDeployment, Type: TypeTask, Repo: "bwsalmon/grain",
		Base: "candidate", Enabled: false, Cadence: "weekly", Next: now.Add(72 * time.Hour)},
	{ID: "sched-c", Kind: KindSchedule, Name: "Upgrade", Source: SourceSystem, Type: TypeSystem, Enabled: true,
		Cadence: "everyNHours", Next: now.Add(-time.Minute)},
	{ID: "sched-d", Kind: KindWorkflow, Name: "Workflow nightly", Source: "acme/widgets", Type: TypeWorkflow, Repo: "acme/widgets",
		Enabled: true, Cadence: "daily", Next: now.Add(30 * time.Hour), LastOutcome: "failure"},
}

func answer(t *testing.T, query string) []string {
	t.Helper()
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", query, err)
	}
	var out []string
	for _, fa := range Apply(f, facts, func(f Facts) Facts { return f }, now, time.UTC) {
		out = append(out, fa.ID)
	}
	return out
}

func TestFilterReadsTheProcessesPagesConditions(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"sched-a", "sched-b", "sched-c", "sched-d"}},
		{"f=source:any:bwsalmon/grain", []string{"sched-a"}},
		{"f=source:any:bwsalmon/grain|acme/widgets", []string{"sched-a", "sched-d"}},
		{"f=source:none:system|deployment", []string{"sched-a", "sched-d"}},
		{"f=type:any:template", []string{"sched-a"}},
		{"f=type:none:template|task", []string{"sched-c", "sched-d"}},
		{"f=name:has:candidate", []string{"sched-b"}},
		{"f=name:hasnot:nightly", []string{"sched-b", "sched-c"}},
		{"f=enabled:any:no", []string{"sched-b"}},
		{"f=base:any:__none__", []string{"sched-a", "sched-c", "sched-d"}},
		{"f=base:filled:", []string{"sched-b"}},
		{"f=template:any:deps", []string{"sched-a"}},
		{"f=lastOutcome:any:failure", []string{"sched-d"}},
		{"f=last:empty:", []string{"sched-b", "sched-c", "sched-d"}},
		// What fires in the next six hours: the overdue one included.
		{"f=next:coming:6", []string{"sched-a", "sched-c"}},
		{"f=last:within:2", []string{"sched-a"}},
		{"f=next:after:2026-10-25", []string{"sched-b", "sched-d"}},
		{"f=next:before:2026-10-25", []string{"sched-a", "sched-c"}},
		{"q=widgets", []string{"sched-d"}},
		{"q=sched-b", []string{"sched-b"}},
		{"q=nightly.yml", []string{"sched-a"}},
		{"f=enabled:any:", []string{"sched-a", "sched-b", "sched-c", "sched-d"}},
		{"f=requireApproval:any:yes", nil},
		{"f=requireApproval:empty:", []string{"sched-a", "sched-b", "sched-c", "sched-d"}},
		{"sort=next:asc", []string{"sched-c", "sched-a", "sched-d", "sched-b"}},
		{"sort=enabled:asc,name:desc", []string{"sched-d", "sched-c", "sched-a", "sched-b"}},
		{"sort=last", []string{"sched-a", "sched-b", "sched-c", "sched-d"}},
		{"sort=type:asc", []string{"sched-a", "sched-b", "sched-d", "sched-c"}},
		{"sort=cadence:asc,name:asc", []string{"sched-c", "sched-a", "sched-d", "sched-b"}},
		{"sort=requireApproval:asc,name:asc", []string{"sched-b", "sched-a", "sched-c", "sched-d"}},
	} {
		got := answer(t, tc.query)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// The page writes its kind chips as `?kind=` rather than an `f`
// (processFilters.js's processNarrowingQuery), so a narrowed page's
// address pasted onto the API has to read them -- found in the
// 2026-10-25 Processes walkthrough, where `?kind=workflow` answered
// every schedule and `sort=kind:asc` a 400.
func TestFilterReadsTheKindChips(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"kind=schedule", []string{"sched-a", "sched-b", "sched-c"}},
		{"kind=workflow", []string{"sched-d"}},
		{"kind=schedule,workflow", []string{"sched-a", "sched-b", "sched-c", "sched-d"}},
		{"kind=template", nil},
		{"kind=suite,trigger", nil},
		{"kind=workflow&q=nightly", []string{"sched-d"}},
		{"kind=schedule&f=enabled:any:yes", []string{"sched-a", "sched-c"}},
		{"f=kind:any:workflow", []string{"sched-d"}},
		{"f=kind:none:schedule", []string{"sched-d"}},
		{"sort=kind:desc", []string{"sched-d", "sched-a", "sched-b", "sched-c"}},
	} {
		if got := answer(t, tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterRefusesWhatItCannotRead(t *testing.T) {
	for _, query := range []string{
		"f=nosuch:any:x",
		"f=enabled:has:x",
		"f=name:any:x",
		"f=enabled",
		"sort=nosuch",
		"sort=name:sideways",
		"kind=schedules",
		"kind=schedule,nosuch",
	} {
		q, _ := url.ParseQuery(query)
		if _, err := ParseQuery(q); err == nil {
			t.Errorf("ParseQuery(%q) = nil error, want one", query)
		}
	}
}

func TestEscapedValuesReadBack(t *testing.T) {
	cond, err := ParseCondition("name:has:a%3Ab%7Cc")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a:b|c"}; !reflect.DeepEqual(cond.Values, want) {
		t.Errorf("values = %v, want %v", cond.Values, want)
	}
}
