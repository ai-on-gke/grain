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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRefuseReviewOfGrants(t *testing.T) {
	ok := []string{
		"grain-ci",
		"https://caps.example/workspace#workspace",
		`https://caps.example/workspace#workspace={"readonly":true}`,
	}
	if err := model.RefuseReviewOfGrants(ok); err != nil {
		t.Fatalf("RefuseReviewOfGrants(%q) = %v, want nil", ok, err)
	}
	asked := append(ok, `https://caps.example/workspace#workspace={"review_of":"51","snapshot":3}`)
	if err := model.RefuseReviewOfGrants(asked); err == nil {
		t.Fatal("a grant naming review_of was not refused")
	}
}

func TestCLReviewTargetRoundTrips(t *testing.T) {
	key := model.CLReviewTarget("cl/612345678", 4, "security-review")
	if key != "cl/612345678@4:security-review" {
		t.Fatalf("CLReviewTarget = %q", key)
	}
	cl, snap, tmpl, ok := model.ParseCLReviewTarget(key)
	if !ok || cl != "cl/612345678" || snap != 4 || tmpl != "security-review" {
		t.Fatalf("ParseCLReviewTarget(%q) = %q %d %q %v", key, cl, snap, tmpl, ok)
	}
	for _, bad := range []string{"", "cl/1", "cl/1@x:t", "cl/1@2", "cl/1@2:"} {
		if _, _, _, ok := model.ParseCLReviewTarget(bad); ok {
			t.Errorf("ParseCLReviewTarget(%q) ok, want refused", bad)
		}
	}
}
