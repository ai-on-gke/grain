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

func TestNormalizeIdentity(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"dharb@google.com":                             "dharb@google.com",
		" accounts.google.com:dharb@google.com ":       "dharb@google.com",
		"https://accounts.google.com:dharb@google.com": "dharb@google.com",
		"user:dharb@google.com":                        "dharb@google.com",
		"User:Accounts.Google.Com:Dharb@Google.com":    "Dharb@Google.com",
		"":                     "",
		"group:eng@google.com": "group:eng@google.com",
	} {
		if got := model.NormalizeIdentity(in); got != want {
			t.Errorf("NormalizeIdentity(%q) = %q, want %q", in, got, want)
		}
	}
	if model.SameIdentity("", "") {
		t.Error("SameIdentity of two empty identities is true; nobody is nobody's")
	}
	if !model.SameIdentity("accounts.google.com:Dharb@google.com", "user:dharb@GOOGLE.com") {
		t.Error("IAP's and IAM's spellings of one address are not the same identity")
	}
}
