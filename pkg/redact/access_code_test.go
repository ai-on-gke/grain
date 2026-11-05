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

package redact_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/redact"
)

func TestTextRedactsAccessCodeHeader(t *testing.T) {
	text := "X-Grain-Access-Code: ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567\n" +
		"x-grain-access-code=secret-code-value\n"
	got, n := (*redact.Redactor)(nil).Text(text)
	for _, leaked := range []string{"ABCD-EFGH", "secret-code-value"} {
		if strings.Contains(got, leaked) {
			t.Errorf("Text left %q in:\n%s", leaked, got)
		}
	}
	if n != 2 {
		t.Errorf("Text counted %d redactions, want 2:\n%s", n, got)
	}
}

func TestTextRedactsBareExternalAgentToken(t *testing.T) {
	minted, _, prefix, err := model.GenerateExternalAgentToken()
	if err != nil {
		t.Fatalf("GenerateExternalAgentToken: %v", err)
	}
	longTok := "gma_" + strings.Repeat("a1b2c3d4", 8)
	line := "leaked " + minted + " and " + longTok + " (prefix " + prefix + ")\n"
	got, n := (*redact.Redactor)(nil).Text(line)
	for _, secret := range []string{minted, longTok} {
		if strings.Contains(got, secret) {
			t.Fatalf("Text left gma_ token %q in:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "prefix "+prefix) {
		t.Errorf("Text redacted non-secret display prefix %q in:\n%s", prefix, got)
	}
	if !strings.Contains(got, "[redacted: external agent token]") || n != 2 {
		t.Errorf("Text(%q) = %q, %d; want [redacted: external agent token] with n=2", line, got, n)
	}
}
