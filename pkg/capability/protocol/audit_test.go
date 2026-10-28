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

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A release reply's audit (§4.4.4): what Grain accepts, and what it
// refuses while still counting the release itself as done.
func TestParseReleaseResponseAudit(t *testing.T) {
	const id = "e1-7-1-0"
	r, err := ParseReleaseResponse([]byte(`{"status":"ok"}`), id)
	if err != nil || len(r.Audit) != 0 || r.AuditRefused != "" {
		t.Fatalf("a bare ok: %+v, %v", r, err)
	}
	r, err = ParseReleaseResponse([]byte(`{"status":"ok","audit_omitted":2,"audit":[`+
		`{"time":"2026-10-23T18:00:00Z","attempt_id":"e1-7-1-0","script":"deploy","argv":["a"]},`+
		`{"time":"2026-10-23T18:00:01.5+02:00","nested":{"x":[1,2]}}]}`), id)
	if err != nil || len(r.Audit) != 2 || r.AuditOmitted != 2 || r.AuditRefused != "" {
		t.Fatalf("a valid audit: %+v, %v", r, err)
	}
	if _, err := ParseReleaseResponse([]byte(`{"status":"refused","audit":[]}`), id); err == nil {
		t.Error("a release that is not ok was accepted")
	}

	big := `{"time":"2026-10-23T18:00:00Z","x":"` + strings.Repeat("x", MaxAuditRecordBytes) + `"}`
	many := strings.TrimSuffix(strings.Repeat(`{"time":"2026-10-23T18:00:00Z"},`, MaxAuditRecords+1), ",")
	var total strings.Builder
	for i := 0; i*(MaxAuditRecordBytes-100) <= MaxAuditBytes; i++ {
		if i > 0 {
			total.WriteString(",")
		}
		total.WriteString(`{"time":"2026-10-23T18:00:00Z","x":"` + strings.Repeat("x", MaxAuditRecordBytes-100) + `"}`)
	}
	for name, audit := range map[string]string{
		"not an object":      `["x"]`,
		"null record":        `[null]`,
		"no time":            `[{"script":"a"}]`,
		"time not RFC 3339":  `[{"time":"yesterday"}]`,
		"another attempt_id": `[{"time":"2026-10-23T18:00:00Z","attempt_id":"e1-8-1-0"}]`,
		"record too big":     `[` + big + `]`,
		"too many records":   `[` + many + `]`,
		"too many bytes":     `[` + total.String() + `]`,
	} {
		r, err := ParseReleaseResponse([]byte(`{"status":"ok","audit_omitted":1,"audit":`+audit+`}`), id)
		if err != nil {
			t.Errorf("%s: the release itself failed: %v", name, err)
			continue
		}
		if r.AuditRefused == "" || len(r.Audit) != 0 || r.AuditOmitted != 0 {
			t.Errorf("%s: audit kept: %d records, omitted %d, refused %q", name, len(r.Audit), r.AuditOmitted, r.AuditRefused)
		}
	}
	if r, _ := ParseReleaseResponse([]byte(`{"status":"ok","audit_omitted":-1}`), id); r == nil || r.AuditRefused == "" {
		t.Errorf("a negative audit_omitted was accepted: %+v", r)
	}
}

// A served method with an Audit hook answers release_ with it, inside
// the limits, and answers a repeated release -- Grain retrying one it
// timed out on -- with the same records rather than none.
func TestMethodReleaseCarriesItsAudit(t *testing.T) {
	now := time.Date(2026, 10, 23, 18, 0, 0, 0, time.UTC)
	audited := 0
	m, err := NewMethod(MethodSpec{
		Name: "audited",
		Now:  func() time.Time { return now },
		Mint: func(context.Context, string, time.Time, map[string]any) (*MintResponse, error) {
			return &MintResponse{Status: StatusOK}, nil
		},
		Release: func(context.Context, string) error { return nil },
		Audit: func(id string) ([]any, int) {
			audited++
			return []any{
				map[string]any{"time": now.Format(time.RFC3339), "attempt_id": id, "n": 1},
				map[string]any{"time": now.Format(time.RFC3339), "x": strings.Repeat("x", MaxAuditRecordBytes)},
				map[string]any{"time": now.Format(time.RFC3339), "attempt_id": id, "n": 2},
			}, 4
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const id = "e1-7-1-0"
	if _, err := m.handleMint(ctx, map[string]any{"attempt_id": id, "deadline": now.Add(time.Hour).Format(time.RFC3339), "args": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	raw, err := m.handleRelease(ctx, map[string]any{"attempt_id": id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseReleaseResponse(raw, id)
	if err != nil || r.AuditRefused != "" {
		t.Fatalf("release reply %s: %+v, %v", raw, r, err)
	}
	if len(r.Audit) != 2 || r.AuditOmitted != 5 {
		t.Fatalf("audit = %d records, omitted %d; want 2, and the oversized one counted with the 4", len(r.Audit), r.AuditOmitted)
	}
	var second struct{ N int }
	if err := json.Unmarshal(r.Audit[1], &second); err != nil || second.N != 2 {
		t.Errorf("records out of order: %s", r.Audit[1])
	}
	again, err := m.handleRelease(ctx, map[string]any{"attempt_id": id})
	if err != nil || string(again) != string(raw) || audited != 1 {
		t.Errorf("repeated release = %s, %v (audited %d times); want the first reply again", again, err, audited)
	}
	// Past the grace period the kept reply goes, and an unknown
	// attempt_id releases as ok with nothing (§4.4.2).
	now = now.Add(DefaultGrace + time.Minute)
	m.Expire(ctx)
	if late, _ := m.handleRelease(ctx, map[string]any{"attempt_id": id}); string(late) != string(releasedOK) {
		t.Errorf("release after the grace period = %s, want a bare ok", late)
	}
}

// Pass.Release returns each releasable grant's audit, by grant, and
// nothing for a grant whose server sent none.
func TestPassReleaseReturnsAudits(t *testing.T) {
	f := &auditMinter{fakeMinter: fakeMinter{answers: map[string]string{"a": `{"status":"ok"}`, "b": `{"status":"ok"}`}}}
	p, err := MintPass(context.Background(), grantsOf("a", "b"), func(string) (Minter, error) { return f, nil }, PassOptions{Deployment: "d", Task: "7", Attempt: 1, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	audits, err := p.Release(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].AttemptID != "d-7-1-0" || !strings.HasSuffix(audits[0].Grant, "#a") || len(audits[0].Records) != 1 || audits[0].Omitted != 3 {
		t.Errorf("audits = %+v", audits)
	}
}

type auditMinter struct{ fakeMinter }

func (f *auditMinter) Release(ctx context.Context, method, attemptID string) (*ReleaseResponse, error) {
	f.fakeMinter.Release(ctx, method, attemptID)
	if method != "a" {
		return &ReleaseResponse{Status: StatusOK}, nil
	}
	return ParseReleaseResponse([]byte(fmt.Sprintf(`{"status":"ok","audit_omitted":3,"audit":[{"time":"2026-10-23T18:00:00Z","attempt_id":%q}]}`, attemptID)), attemptID)
}
