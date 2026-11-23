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

package ci

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/grit/actions"
)

// slimContainerCI is the job that failed bwsalmon/silo's gate: a slim
// Debian image fetching from deb.debian.org over HTTPS, which the
// guest's resolver sends to the package cache, and uploading what it
// built -- beside a job with no container, which runs on the guest.
const slimContainerCI = `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    container: debian:bookworm-slim
    steps:
      - run: python3 -c 'import urllib.request; urllib.request.urlopen("https://deb.debian.org/debian/dists/bookworm/Release")'
      - uses: actions/upload-artifact@v4
        with:
          name: out
          path: out/
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: make lint
`

// A container job is given the CA variables, and keeps them once the
// plan fills its event in; a job on the guest is not, and its script and
// fingerprint stay exactly what they were.
func TestAContainerJobIsGivenTheGuestsCA(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(slimContainerCI))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, []Workflow{w})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 2 {
		t.Fatalf("planned %d jobs, want 2", len(plan.Jobs))
	}
	for _, pj := range plan.Jobs {
		job := pj.Job
		script := job.Steps[0].Run
		switch job.ID {
		case "build":
			if !reflect.DeepEqual(job.Actions.ContainerCAEnv, actions.ContainerCAEnv) {
				t.Errorf("build ContainerCAEnv = %v, want %v", job.Actions.ContainerCAEnv, actions.ContainerCAEnv)
			}
			for _, want := range []string{"act 'push'", "--env 'SSL_CERT_FILE=" + actions.ContainerCADir + "/ca-certificates.crt'"} {
				if !strings.Contains(script, want) {
					t.Errorf("build script missing %q:\n%s", want, script)
				}
			}
		case "lint":
			if want := actions.ActScript(".github/workflows/ci.yml", "lint", "push"); script != want {
				t.Errorf("lint script = %q, want %q", script, want)
			}
			encoded, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "ContainerCAEnv") {
				t.Errorf("a job with no container fingerprints a key it does not use: %s", encoded)
			}
		}
	}
}

// A CA variable the workflow sets itself, at any level, is the author's
// and grain does not set it over theirs; a container written as null is
// no container at all.
func TestAContainerJobKeepsTheCAVariablesItSetsItself(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(`name: CI
on: [push]
env:
  SSL_CERT_FILE: /etc/mine.pem
jobs:
  build:
    runs-on: ubuntu-latest
    env:
      CURL_CA_BUNDLE: /etc/mine.pem
    container:
      image: python:3-slim
      env:
        REQUESTS_CA_BUNDLE: /etc/mine.pem
    steps:
      - run: pip install -r requirements.txt
  none:
    runs-on: ubuntu-latest
    container: null
    steps:
      - run: true
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range w.Jobs {
		switch job.ID {
		case "build":
			if want := []string{"NODE_EXTRA_CA_CERTS"}; !reflect.DeepEqual(job.Actions.ContainerCAEnv, want) {
				t.Errorf("build ContainerCAEnv = %v, want %v", job.Actions.ContainerCAEnv, want)
			}
		case "none":
			if job.Actions.ContainerCAEnv != nil {
				t.Errorf("a null container was given %v", job.Actions.ContainerCAEnv)
			}
		}
	}
}
