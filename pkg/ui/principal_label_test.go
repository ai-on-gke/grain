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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestPrincipalLabel is operator-attribution spec §7.4: one row for
// every row of §1.2.2's table, legacy rows included.
func TestPrincipalLabel(t *testing.T) {
	human := func(id string, src model.AuthSource) model.Principal {
		return model.Principal{Kind: model.PrincipalHuman, ID: id, Source: src}
	}
	alice := human("alice@example.com", model.SourceIAP)
	for _, tc := range []struct {
		name   string
		p      model.Principal
		behalf *model.Principal
		want   string
	}{
		{"verified by IAP", alice, nil, "alice@example.com"},
		{"verified by a registered verifier", human("bob@example.com", "example-proxy"), nil, "bob@example.com"},
		{"a service account behind IAP",
			model.Principal{Kind: model.PrincipalService, ID: "sa@p.iam.gserviceaccount.com", Source: model.SourceIAP}, nil,
			"sa@p.iam.gserviceaccount.com"},
		{"operator by the access code", human("operator", model.SourceAccessCode), nil, "operator (access code)"},
		{"operator over loopback", human("operator", model.SourceLoopback), nil, "operator (local)"},
		{"operator in-process", human("operator", model.SourceInProcess), nil, "operator (local)"},
		{"a renamed operator by the access code", human("ops", model.SourceAccessCode), nil, "operator (access code)"},
		{"legacy operator row", human("operator", ""), nil, "operator (access code)"},
		{"legacy row naming somebody else", human("bwsalmon", ""), nil, "bwsalmon (unverified)"},
		{"a console session", model.Principal{Kind: model.PrincipalAgent, ID: "42", Source: model.SourceSession}, nil, "session 42"},
		{"a dispatched run", model.Principal{Kind: model.PrincipalAgent, ID: "42", Source: model.SourceSandbox}, nil, "task 42"},
		{"a legacy agent row", model.Principal{Kind: model.PrincipalAgent, ID: "42"}, nil, "task 42"},
		{"an external agent", model.Principal{Kind: model.PrincipalAgent, ID: "external:ci-bot", Source: model.SourceExternalToken},
			nil, "external agent ci-bot"},
		{"a legacy external agent row", model.Principal{Kind: model.PrincipalAgent, ID: "external:ci-bot"}, nil, "external agent ci-bot"},
		{"automation", model.GrainPrincipal, nil, "grain"},
		{"automation, sourced", model.Principal{Kind: model.PrincipalAutomation, ID: "auto-submit", Source: model.SourceInternal}, nil, "grain"},
		{"a session for a verified person", model.Principal{Kind: model.PrincipalAgent, ID: "42", Source: model.SourceSession},
			&alice, "session 42 for alice@example.com"},
		{"an unverified on-behalf-of is not named", model.Principal{Kind: model.PrincipalAgent, ID: "42", Source: model.SourceSandbox},
			&model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}, "task 42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ui.PrincipalLabel(tc.p, tc.behalf); got != tc.want {
				t.Errorf("PrincipalLabel(%+v, %+v) = %q, want %q", tc.p, tc.behalf, got, tc.want)
			}
		})
	}
}

// TestPrincipalVerified pins model.Principal.Verified (spec §1.1.4): a
// person or service a verifier vouched for, and nothing else.
func TestPrincipalVerified(t *testing.T) {
	for _, tc := range []struct {
		p    model.Principal
		want bool
	}{
		{model.Principal{Kind: model.PrincipalHuman, ID: "a@x", Source: model.SourceIAP}, true},
		{model.Principal{Kind: model.PrincipalHuman, ID: "a@x", Source: model.SourceToken}, true},
		{model.Principal{Kind: model.PrincipalService, ID: "sa@x", Source: "example-proxy"}, true},
		{model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}, false},
		{model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceLoopback}, false},
		{model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceInProcess}, false},
		{model.Principal{Kind: model.PrincipalHuman, ID: "operator"}, false},
		{model.Principal{Kind: model.PrincipalAgent, ID: "1", Source: model.SourceIAP}, false},
		{model.Principal{Kind: model.PrincipalAutomation, ID: "grain", Source: model.SourceInternal}, false},
	} {
		if got := tc.p.Verified(); got != tc.want {
			t.Errorf("%+v.Verified() = %v, want %v", tc.p, got, tc.want)
		}
	}
}
