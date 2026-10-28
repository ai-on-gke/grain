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

package cli

import (
	"flag"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestChangeTargetFlags(t *testing.T) {
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: "monorepo"}))

	if ct, err := changeTargetForKind("monorepo", false, "", 0, ""); ct != nil || err != nil {
		t.Errorf("no --monorepo: %+v, %v; want nothing", ct, err)
	}
	if _, err := changeTargetForKind("monorepo", false, "w", 0, ""); err == nil || !strings.Contains(err.Error(), "need --monorepo") {
		t.Errorf("a spec flag without --monorepo: error %v", err)
	}
	ct, err := changeTargetForKind("monorepo", true, "", 0, "")
	if err != nil || ct == nil || ct.Kind != "monorepo" || ct.SpecString() != "" {
		t.Errorf("--monorepo alone: %+v, %v", ct, err)
	}
	ct, err = changeTargetForKind("monorepo", true, " mine ", 42, "head")
	if err != nil || ct.SpecString() != `{"start_cl":42,"sync_to":"head","workspace":"mine"}` {
		t.Errorf("--monorepo with spec: %s, %v", ct.SpecString(), err)
	}

	fs := flag.NewFlagSet("grain create", flag.ContinueOnError)
	resolve := bindChangeTargetFlags(fs)
	if err := fs.Parse([]string{"-monorepo", "-monorepo-workspace", "mine", "-monorepo-start-cl", "42", "-monorepo-sync-to", "head"}); err != nil {
		t.Fatalf("parse registered flags: %v", err)
	}
	ct, err = resolve()
	if err != nil || ct == nil || ct.Kind != "monorepo" || ct.SpecString() != `{"start_cl":42,"sync_to":"head","workspace":"mine"}` {
		t.Errorf("resolved flags: %+v (%s), %v", ct, ct.SpecString(), err)
	}
}
