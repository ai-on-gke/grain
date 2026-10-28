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

func TestPullRequestRefRoundTrips(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "bwsalmon", Name: "grain"}, Number: 42}
	s := ref.String()
	if s != "bwsalmon/grain#42" {
		t.Fatalf("String() = %q, want bwsalmon/grain#42", s)
	}
	got, err := model.ParsePullRequestRef(s)
	if err != nil {
		t.Fatalf("ParsePullRequestRef(%q): %v", s, err)
	}
	if got != ref {
		t.Fatalf("ParsePullRequestRef(%q) = %+v, want %+v", s, got, ref)
	}
}

func TestParsePullRequestRefRejectsMissingNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain"); err == nil {
		t.Fatal("expected an error for a ref with no #number")
	}
}

func TestParsePullRequestRefRejectsBadNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain#abc"); err == nil {
		t.Fatal("expected an error for a non-numeric #suffix")
	}
}
