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

package githubsandbox

import "github.com/ai-on-gke/grain/pkg/model"

// FakeApp is a GitHub App installation on one account, for this
// package's external tests (package githubsandbox_test), which can
// import grainserver -- and so exercise github#sandbox-repo through the
// in-process server and protocol.MintPass, as the orchestrator and CI
// engine do -- where this package's own tests cannot.
type FakeApp struct{ c *fakeAppClient }

// NewFakeApp is an App installed on account, with no repositories.
func NewFakeApp(account string) FakeApp { return FakeApp{newFakeAppClient(account)} }

// Provider is a sandbox-repo provider minting through the fake App.
func (a FakeApp) Provider() *Provider { return testProvider(a.c) }

// Repos is the fake App's repositories, sorted.
func (a FakeApp) Repos() []string { return sortedRepoNames(a.c) }

// FakeCredentials resolves the App's ID and key, recording the secret
// store audit's run of every resolve.
type FakeCredentials struct{ c *fakeCredentials }

// NewFakeCredentials holds the default App ID and key credentials.
func NewFakeCredentials() FakeCredentials { return FakeCredentials{testCredentials()} }

// Resolver is the credentials as a model.CredentialResolver.
func (f FakeCredentials) Resolver() model.CredentialResolver { return f.c }

// AuditedRuns is secrets.RunFromContext on every resolve, in order.
func (f FakeCredentials) AuditedRuns() []string { return append([]string(nil), f.c.runs...) }
