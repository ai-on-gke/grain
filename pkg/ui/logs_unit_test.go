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

package ui_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeUnitSource is a ui.UnitLogSource over fake journals: one per unit
// name, the running one first.
type fakeUnitSource struct {
	units    []systemlog.Unit
	journals map[string]*fakeLogSource
}

func (f *fakeUnitSource) Units() []systemlog.Unit { return f.units }

func (f *fakeUnitSource) Tail(ctx context.Context, n int) ([]string, error) {
	return f.journals[f.units[0].Name].Tail(ctx, n)
}

func (f *fakeUnitSource) ForUnit(unit string) (ui.LogSource, error) {
	for _, u := range f.units {
		if u.Name == unit {
			return f.journals[unit], nil
		}
	}
	return nil, fmt.Errorf("no grain unit %q on this host", unit)
}

type logUnitResponse struct {
	Lines []string     `json:"lines"`
	Unit  *ui.LogUnit  `json:"unit"`
	Units []ui.LogUnit `json:"units"`
}

// On a host carrying both grain's system unit and its user unit, the
// daemon source reads the running one and says so, lists both, and
// reads the other on ?unit=; a unit outside that list is refused.
func TestGetLogLinesNamesTheUnitReadAndTakesUnitToPickTheOther(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{
		"daemon": &fakeUnitSource{
			units: []systemlog.Unit{systemlog.WorkstationUnit, systemlog.DefaultDaemonUnit},
			journals: map[string]*fakeLogSource{
				"grain.service":        {lines: []string{"user: serving"}},
				"grain-daemon.service": {lines: []string{"system: stale"}},
			},
		},
		"dmesg": &fakeLogSource{lines: []string{"kernel"}},
	}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[logUnitResponse](t, rec)
	if len(got.Lines) != 1 || got.Lines[0] != "user: serving" {
		t.Errorf("lines = %v, want the running (user) unit's", got.Lines)
	}
	if got.Unit == nil || got.Unit.Unit != "grain.service" || got.Unit.Scope != "user" || !got.Unit.Running {
		t.Errorf("unit = %+v, want grain.service, user, running", got.Unit)
	}
	if len(got.Units) != 2 || got.Units[0].Unit != "grain.service" || !got.Units[0].Running ||
		got.Units[1].Unit != "grain-daemon.service" || got.Units[1].Scope != "system" || got.Units[1].Running {
		t.Errorf("units = %+v, want the running user unit first, then the system unit", got.Units)
	}

	rec = do(t, srv, http.MethodGet, "/api/logs/daemon?unit=grain-daemon.service", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got = decode[logUnitResponse](t, rec)
	if len(got.Lines) != 1 || got.Lines[0] != "system: stale" {
		t.Errorf("lines with unit=grain-daemon.service = %v, want the system unit's", got.Lines)
	}
	if got.Unit == nil || got.Unit.Unit != "grain-daemon.service" || got.Unit.Scope != "system" || got.Unit.Running {
		t.Errorf("unit with unit=grain-daemon.service = %+v, want grain-daemon.service, system, not running", got.Unit)
	}

	rec = do(t, srv, http.MethodGet, "/api/logs/daemon?unit=sshd.service", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sshd.service") {
		t.Errorf("unit=sshd.service: status = %d, body %s; want 400 naming the unit", rec.Code, rec.Body)
	}

	// A source that is not a journal takes no unit, and says nothing
	// about one.
	rec = do(t, srv, http.MethodGet, "/api/logs/dmesg", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dmesg: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[logUnitResponse](t, rec); got.Unit != nil || got.Units != nil {
		t.Errorf("dmesg named a unit: %+v %+v", got.Unit, got.Units)
	}
	rec = do(t, srv, http.MethodGet, "/api/logs/dmesg?unit=grain.service", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("dmesg?unit=: status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// With one grain unit on the host there is nothing to choose between:
// the response names the unit it read and offers no list.
func TestGetLogLinesWithOneUnitNamesItAndOffersNoChoice(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{
		"daemon": &fakeUnitSource{
			units:    []systemlog.Unit{systemlog.DefaultDaemonUnit},
			journals: map[string]*fakeLogSource{"grain-daemon.service": {lines: []string{"serving"}}},
		},
	}
	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[logUnitResponse](t, rec)
	if got.Unit == nil || got.Unit.Unit != "grain-daemon.service" || !got.Unit.Running {
		t.Errorf("unit = %+v, want grain-daemon.service, running", got.Unit)
	}
	if got.Units != nil {
		t.Errorf("units = %+v, want none listed with a single unit", got.Units)
	}
}
