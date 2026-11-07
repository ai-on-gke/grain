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

package githubtoken_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

func TestValidNameMatchesCredentialLadderAndPattern(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(githubtoken.NamePattern)
	valid := []string{"release-bot", "docs_bot", "Bot123", "a"}
	for _, name := range valid {
		if !githubtoken.ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
		if err := gitproxy.ValidCredentialName(name); err != nil {
			t.Errorf("gitproxy.ValidCredentialName(%q) = %v, want nil", name, err)
		}
		if !re.MatchString(name) {
			t.Errorf("NamePattern %q did not match %q", githubtoken.NamePattern, name)
		}
	}
	invalid := []string{"", "anonymous", "../escape", "a/b", "a.b", "has space", "colon:name"}
	for _, name := range invalid {
		if githubtoken.ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
		if gitproxy.ValidCredentialName(name) == nil {
			t.Errorf("gitproxy.ValidCredentialName(%q) = nil, want error", name)
		}
		if name != "anonymous" && re.MatchString(name) {
			t.Errorf("NamePattern %q matched invalid %q", githubtoken.NamePattern, name)
		}
	}
}

func TestPromptSectionAndCredentialFile(t *testing.T) {
	t.Parallel()
	prompt := githubtoken.PromptSection("release-bot")
	if !strings.Contains(prompt, `"release-bot"`) || !strings.Contains(prompt, "never hold the token") {
		t.Errorf("PromptSection = %q, missing expected guidance", prompt)
	}
	if got := githubtoken.CredentialFile("release-bot", false); got != "secrets/github/release-bot.token" {
		t.Errorf("CredentialFile(token) = %q", got)
	}
	if got := githubtoken.CredentialFile("release-bot", true); got != "secrets/github/release-bot.app.json" {
		t.Errorf("CredentialFile(app) = %q", got)
	}
}
