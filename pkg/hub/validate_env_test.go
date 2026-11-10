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

package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/hubenv"
)

// TestValidateHonoursGrainHubConfigEnv ensures `grain-hub validate` reads
// $GRAIN_HUB_CONFIG (and $GRAIN_REGISTRY_CONFIG with a one-time deprecation
// warning), matching `grain-hub serve`, while explicit -config and positional
// arguments still win.
func TestValidateHonoursGrainHubConfigEnv(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	goodFile := filepath.Join(tmp, "good.yaml")
	if err := os.WriteFile(goodFile, []byte(validTestYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	otherGoodFile := filepath.Join(tmp, "other-good.yaml")
	if err := os.WriteFile(otherGoodFile, []byte(validTestYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(tmp, "bad.yaml")
	if err := os.WriteFile(badFile, []byte("not_a_hub_field: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	prevLog := hubenv.Log
	t.Cleanup(func() {
		hubenv.Log = prevLog
		hubenv.ResetForTest()
	})

	runWithEnv := func(t *testing.T, hubCfg, regCfg string, args ...string) (stdout, warnings string, err error) {
		t.Helper()
		t.Setenv("GRAIN_HUB_CONFIG", hubCfg)
		t.Setenv("GRAIN_REGISTRY_CONFIG", regCfg)
		var outBuf, errBuf, warnBuf bytes.Buffer
		hubenv.ResetForTest()
		hubenv.Log = func() io.Writer { return &warnBuf }
		err = Run(ctx, append([]string{"validate"}, args...), &outBuf, &errBuf)
		return outBuf.String(), warnBuf.String(), err
	}

	t.Run("GRAIN_HUB_CONFIG validates without flags", func(t *testing.T) {
		out, warnings, err := runWithEnv(t, goodFile, "")
		if err != nil {
			t.Fatalf("validate with GRAIN_HUB_CONFIG: %v", err)
		}
		if !strings.Contains(out, "config "+goodFile+" is valid") {
			t.Errorf("stdout = %q, want config %s is valid", out, goodFile)
		}
		if warnings != "" {
			t.Errorf("unexpected deprecation warnings: %q", warnings)
		}
	})

	t.Run("GRAIN_REGISTRY_CONFIG falls back with deprecation warning", func(t *testing.T) {
		out, warnings, err := runWithEnv(t, "", goodFile)
		if err != nil {
			t.Fatalf("validate with GRAIN_REGISTRY_CONFIG: %v", err)
		}
		if !strings.Contains(out, "config "+goodFile+" is valid") {
			t.Errorf("stdout = %q, want config %s is valid", out, goodFile)
		}
		if n := strings.Count(warnings, "$GRAIN_REGISTRY_CONFIG is deprecated"); n != 1 {
			t.Errorf("got %d $GRAIN_REGISTRY_CONFIG warnings, want 1:\n%s", n, warnings)
		}
	})

	t.Run("GRAIN_HUB_CONFIG wins over GRAIN_REGISTRY_CONFIG without warning", func(t *testing.T) {
		out, warnings, err := runWithEnv(t, goodFile, badFile)
		if err != nil {
			t.Fatalf("validate with both env vars set: %v", err)
		}
		if !strings.Contains(out, "config "+goodFile+" is valid") {
			t.Errorf("stdout = %q, want config %s is valid", out, goodFile)
		}
		if warnings != "" {
			t.Errorf("unexpected warning when GRAIN_HUB_CONFIG is set: %q", warnings)
		}
	})

	t.Run("-config and positional path override env vars without warning", func(t *testing.T) {
		for _, args := range [][]string{
			{"-config", otherGoodFile},
			{otherGoodFile},
		} {
			out, warnings, err := runWithEnv(t, badFile, badFile, args...)
			if err != nil {
				t.Fatalf("validate %v: %v", args, err)
			}
			if !strings.Contains(out, "config "+otherGoodFile+" is valid") {
				t.Errorf("validate %v stdout = %q, want %s", args, out, otherGoodFile)
			}
			if warnings != "" {
				t.Errorf("validate %v emitted unexpected warning: %q", args, warnings)
			}
		}
	})

	t.Run("missing config names -config and GRAIN_HUB_CONFIG", func(t *testing.T) {
		_, _, err := runWithEnv(t, "", "")
		if err == nil {
			t.Fatal("validate succeeded with no config")
		}
		for _, want := range []string{"-config", "GRAIN_HUB_CONFIG"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		}
	})
}

func TestHubAPIAndStoreErrorsSayHubNotRegistry(t *testing.T) {
	for _, ae := range []*api.APIError{
		{StatusCode: 502, Message: "bad gateway"},
		{StatusCode: 403, Code: "quota_exceeded", Message: "over quota"},
		{StatusCode: 500, Code: "internal", Ref: "req-9", Message: "boom"},
	} {
		msg := ae.Error()
		if !strings.HasPrefix(msg, "hub API ") || strings.Contains(strings.ToLower(msg), "registry") {
			t.Errorf("APIError.Error() = %q, want \"hub API ...\" without \"registry\"", msg)
		}
	}

	for _, rawURL := range []string{
		"http://example.com",
		"ftp://example.com",
		"https://user:pass@example.com",
		"https://example.com?q=1",
		"https://",
		"https://metadata.google.internal",
		"https://" + strings.Repeat("a", 2100) + ".example.com",
	} {
		_, err := api.ValidateHubURL(rawURL, false)
		if err == nil {
			t.Fatalf("ValidateHubURL(%q) succeeded, want error", rawURL)
		}
		if !strings.Contains(err.Error(), "hub URL") || strings.Contains(strings.ToLower(err.Error()), "registry") {
			t.Errorf("ValidateHubURL(%q) = %q, want \"hub URL\" without \"registry\"", rawURL, err.Error())
		}
	}

	for _, sentinel := range []error{
		store.ErrNotFound,
		store.ErrConflict,
		store.ErrNameReserved,
		store.ErrPrimaryScopeConflict,
		store.ErrQuotaExceeded,
		store.ErrInvalidCredential,
		store.ErrSQLPoolExhausted,
	} {
		if !strings.HasPrefix(sentinel.Error(), "hub store: ") || strings.Contains(strings.ToLower(sentinel.Error()), "registry") {
			t.Errorf("store sentinel = %q, want \"hub store: ...\" without \"registry\"", sentinel.Error())
		}
	}

	psc := &store.PrimaryScopeConflictError{
		Conflict: api.ScopeConflict{
			Repo:              "acme/widgets",
			PathPrefix:        "pkg/foo",
			ConflictingPrefix: "pkg",
			ConflictingEntry:  "alpha",
		},
	}
	if !errors.Is(psc, store.ErrPrimaryScopeConflict) || !strings.HasPrefix(psc.Error(), "hub store: ") {
		t.Errorf("PrimaryScopeConflictError = %q, want hub store prefix", psc.Error())
	}
}
