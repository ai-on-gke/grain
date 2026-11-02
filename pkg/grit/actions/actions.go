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

// Package actions runs one GitHub Actions job with act, in a disposable
// Kontur guest: the `grit act` subcommand and the script pkg/ci hands an
// Actions job's sandbox. It moved here from pkg/grit, which is now the
// CI service (design/design/grit-split.design.md, "pkg/grit and cmd/grit").
package actions

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// DefaultRunner is the runs-on label a job is assumed to want when none
// is named, and the one act is told to run on the guest itself.
const DefaultRunner = "ubuntu-latest"

// RunnerImages maps a GitHub runs-on label to the trusted Kontur OCI image.
type RunnerImages map[string]string

// Resolve returns the image mapped for label, or an error naming the
// label when there is none.
func (m RunnerImages) Resolve(label string) (string, error) {
	image := strings.TrimSpace(m[label])
	if image == "" {
		return "", fmt.Errorf("grit: no Kontur image is mapped for runner %q", label)
	}
	return image, nil
}

// CacheURLFile is where the sandbox is told this deployment's Actions
// cache lives: one line holding the ACTIONS_CACHE_URL for the job, put
// there when the sandbox was built (cmd/grain's ciSandboxes) and worth
// nothing once that sandbox is released (pkg/actionscache's Scopes).
//
// It is a file rather than an environment variable because the
// environment grain hands a step is part of that job's cache key
// (pkg/ci's runEnv and jobFingerprint.Env): a per-job token in there
// would change the key of every Actions job on every run, and a job
// whose key never repeats is a job whose verdict is never reused.
// Reading a path that is identical for everybody keeps the key still.
const CacheURLFile = "/etc/grain/actions-cache-url"

// ActScript runs one job inside an already prepared Kontur guest.
//
// act runs an Actions cache server of its own and hands the job an
// ACTIONS_CACHE_URL pointing at it -- which is a cache inside a machine
// grain destroys when the job ends, so nothing ever reads back what a
// build exported into it. Where this deployment keeps a cache of its
// own (CacheURLFile above), act is told to run no server and the job is
// pointed at that one instead, and a workflow that already says
// `cache-from: type=gha` starts finding last run's layers with no edit
// to the workflow at all.
//
// Where there is no such file -- a deployment running with the cache
// off, or `grit` on somebody's laptop -- act keeps its own server and
// the job behaves exactly as it did before this existed.
func ActScript(workflow, job, event string) string {
	if event == "" {
		event = `"$GRAIN_CI_EVENT"`
	} else {
		event = shellWord(event)
	}
	return "set -euo pipefail\n" +
		"set -- act " + event +
		" --workflows " + shellWord(workflow) +
		" --job " + shellWord(job) +
		" --platform " + DefaultRunner + "=-self-hosted" +
		" --pull=false --artifact-server-path /tmp/grit-actions-artifacts\n" +
		"grain_actions_cache=\n" +
		"if [ -r " + shellWord(CacheURLFile) + " ]; then grain_actions_cache=$(cat " + shellWord(CacheURLFile) + "); fi\n" +
		"if [ -n \"$grain_actions_cache\" ]; then\n" +
		"  set -- \"$@\" --no-cache-server --env \"ACTIONS_CACHE_URL=$grain_actions_cache\"\n" +
		"fi\n" +
		"exec \"$@\"\n"
}

// WorkflowDir is where ActScriptForText puts the workflow it was given.
// Under /tmp rather than in the checkout because the checkout is what the
// job is about: a workflow file that appeared in it would show up in
// `git status`, in a `git diff` a step takes, and in anything the job
// builds out of its own tree.
const WorkflowDir = "/tmp/grain-ci-workflow"

// ActScriptForText is ActScript given the workflow file's text rather
// than only its path: the text is written into the guest and act is run
// against that, while workflow keeps naming the file in the repository so
// the job's log and its error messages say what the author wrote.
//
// It exists for one caller -- pkg/ci handing act a copy of a workflow
// with one job's `needs:` taken out, so that act runs the single job
// grain asked for rather than that job's whole dependency chain a second
// time. See pkg/ci/actions_needs.go.
//
// base64 rather than the text itself, so that no line of somebody's
// workflow can end the heredoc or be read as shell.
func ActScriptForText(workflow, job, event, text string) string {
	dst := path.Join(WorkflowDir, path.Base(workflow))
	return "set -euo pipefail\n" +
		"mkdir -p " + shellWord(WorkflowDir) + "\n" +
		"base64 -d > " + shellWord(dst) + " <<'GRAIN_CI_WORKFLOW'\n" +
		base64Lines(text) +
		"GRAIN_CI_WORKFLOW\n" +
		ActScript(dst, job, event)
}

// base64Lines is the encoding wrapped at a width a log and an editor can
// both hold, one trailing newline and no blank line.
func base64Lines(text string) string {
	const width = 76
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	var b strings.Builder
	for len(encoded) > width {
		b.WriteString(encoded[:width])
		b.WriteByte('\n')
		encoded = encoded[width:]
	}
	b.WriteString(encoded)
	b.WriteByte('\n')
	return b.String()
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
