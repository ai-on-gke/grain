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

// Package githubtoken provides credential-naming, prompt, and probing helpers
// for named GitHub credentials stored under secrets/github.
//
// Per-task git credential overrides are served on the in-process repo
// capability server as https://grain.dev/capabilities/repo#credential
// {"credential": "<name>"} (pkg/capability/grainserver).
package githubtoken

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultHost is the default GitHub host ("github.com").
const DefaultHost = "github.com"

// NamePattern is the anchored regular expression a named credential stem under
// secrets/github must satisfy (excluding "anonymous", enforced by ValidName and
// the repo#credential JSON schema).
const NamePattern = model.GitCredentialNamePattern

// credentialDir is where these credentials live, relative to a deployment's
// data directory.
const credentialDir = "secrets/github"

// ValidName reports whether name is a valid named credential stem under
// secrets/github: non-empty [A-Za-z0-9_-]+ and not "anonymous".
func ValidName(name string) bool {
	return model.ValidGitCredentialName(name)
}

// CredentialFile is the file backing the credential named name, in either form
// the ladder keeps one in.
func CredentialFile(name string, app bool) string {
	if app {
		return credentialDir + "/" + name + ".app.json"
	}
	return credentialDir + "/" + name + ".token"
}

// Credential is one named credential's current material, as the ladder
// resolves it right now.
type Credential struct {
	Token     string
	App       bool
	MintError string
}

// CredentialSource resolves a named GitHub credential to the material it
// currently authenticates with.
type CredentialSource interface {
	GitHubCredential(name string) (Credential, bool)
}

func realTransport(host string, insecureHTTP bool) github.Transport {
	rt := github.NewRealTransport(host)
	rt.UseTLS = !insecureHTTP
	return rt
}

// PromptSection tells the agent which named GitHub credential its pushes carry.
func PromptSection(name string) string {
	return fmt.Sprintf(
		"Git operations in this sandbox are authenticated with the %q GitHub token, which this "+
			"task was granted in place of the default one. You never hold the token itself: git "+
			"traffic goes through grain's proxy, which attaches it. If a push or fetch is refused, "+
			"it is that token's access that is missing, and saying so is more useful than retrying.",
		name)
}
