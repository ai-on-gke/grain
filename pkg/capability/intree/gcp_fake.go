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

package intree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
)

// gcpFakeFixture is the file -fake-cloud names: the in-memory GCP
// (gcpfake) a test-only gcp server connects to instead of Google, so a
// walkthrough can run a real daemon's gcp server with no GCP at all.
type gcpFakeFixture struct {
	// DefaultIdentity is who every admin is (gcpfake.Cloud.DefaultIdentity).
	DefaultIdentity string `json:"default_identity"`
	// Identities is who each named admin is, overriding DefaultIdentity.
	Identities map[string]string `json:"identities"`
	// Accounts are service accounts that exist before the server starts,
	// as an existing-account set's would.
	Accounts []struct {
		Project     string `json:"project"`
		Email       string `json:"email"`
		Description string `json:"description"`
	} `json:"accounts"`
	// Bindings are IAM bindings that exist before the server starts.
	Bindings []struct {
		Resource string `json:"resource"`
		Role     string `json:"role"`
		Member   string `json:"member"`
	} `json:"bindings"`
	// ClockFile, when set, names a file holding a Go duration (or
	// nothing) that the server's clock is offset by, read on every
	// reading of the clock -- so a walkthrough can move the server past a
	// grant's deadline without waiting hours for it.
	ClockFile string `json:"clock_file"`
}

// loadGCPFake builds the fake cloud and clock path names.
func loadGCPFake(path string) (*gcpfake.Cloud, func() time.Time, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("gcp: -fake-cloud: %v", err)
	}
	var fx gcpFakeFixture
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		return nil, nil, fmt.Errorf("gcp: -fake-cloud %s: %v", path, err)
	}
	cloud := gcpfake.New()
	cloud.DefaultIdentity = fx.DefaultIdentity
	for name, id := range fx.Identities {
		cloud.Identities[name] = id
	}
	for _, a := range fx.Accounts {
		if a.Project == "" || a.Email == "" {
			return nil, nil, fmt.Errorf("gcp: -fake-cloud %s: an account needs a project and an email", path)
		}
		cloud.AddAccount(a.Project, a.Email, a.Description)
	}
	for _, bd := range fx.Bindings {
		cloud.Bind(bd.Resource, bd.Role, bd.Member)
	}
	now := time.Now
	if fx.ClockFile != "" {
		file := fx.ClockFile
		now = func() time.Time { return time.Now().Add(gcpFakeClockOffset(file)) }
	}
	cloud.Now = now
	return cloud, now, nil
}

// gcpFakeClockOffset is the duration in file, zero when it is missing,
// empty or not a duration.
func gcpFakeClockOffset(file string) time.Duration {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return d
}
