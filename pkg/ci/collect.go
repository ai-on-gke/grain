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
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Collecting a job's artifacts: the script the sandbox is asked to run,
// and how its answer is read back.
//
// # Why a shell script and base64 rather than a transport of its own
//
// Because ci.Sandbox has one way in -- Exec -- and adding a second would
// mean a new method on the interface, on both of the adapters behind it
// (cmd/grain's shell and its run_command fallback), and on every Sandbox
// a test has written. What this needs is a few megabytes of bytes out of
// a machine that already hands back the whole of a step's output as a
// string, so it asks that machine to produce the bytes in an alphabet
// that survives a string, and reads them out of the answer. A backend
// that grows a real file transfer later can be reached through an
// optional interface exactly as StreamingSandbox is, and nothing above
// this function changes.
//
// The cost of that choice is bounded on purpose and bounded *in the
// sandbox*: the archive is built there, its size is checked there
// against the store's own MaxArtifactBytes, and an artifact over that
// bound is refused before a single byte of it is encoded. So the worst
// case this can cost the daemon is one artifact's bound in base64 -- a
// third again as much as the file -- held for as long as it takes to
// write it down, rather than whatever a workflow file felt like
// producing.

// artifactCollectionTimeout bounds the whole collection of one job's
// artifacts. Long enough for a tar of a few hundred megabytes of small
// files on a busy host, short enough that a backend which has stopped
// answering does not hold the sandbox open for the run's sake.
const artifactCollectionTimeout = 5 * time.Minute

// The markers the collection script frames its answer with. A status
// line first, then -- when there is anything to send -- the base64
// between a pair of markers. base64's alphabet cannot produce any of
// these, so nothing in the payload can be mistaken for the framing.
const (
	artifactStatusMarker = "grain-ci-artifact:"
	artifactBeginMarker  = "grain-ci-artifact-begin"
	artifactEndMarker    = "grain-ci-artifact-end"
)

// artifactScript is what one artifact's collection looks like inside the
// job's sandbox: tar the declared paths that exist, refuse the result if
// it is over what the store would keep, and print it as base64.
//
// It runs with `set +e` of its own. Both of grain's sandbox transports
// run a script the way a workflow step wants to be run -- stopping at
// the first command that fails -- and this one is the opposite kind of
// script: every failure in it is an outcome to report on the status
// line, not a reason to stop. A job's artifacts going uncollected must
// never be able to turn into a job that failed for a second reason.
//
// Nothing here is interpolated that a workflow file could have made
// dangerous: the paths are checked at parse (validArtifactPath refuses
// quotes, globs, `$`, `..` and absolute paths), and they reach tar
// through a file of names rather than through the command line.
func artifactScript(spec ArtifactSpec, maxBytes int64) string {
	var b strings.Builder
	b.WriteString("set +e\n")
	b.WriteString("grain_ci_tmp=$(mktemp -d 2>/dev/null)\n")
	b.WriteString("if [ -z \"$grain_ci_tmp\" ]; then grain_ci_tmp=/tmp/grain-ci-artifact.$$; mkdir -p \"$grain_ci_tmp\"; fi\n")
	b.WriteString("grain_ci_out=$grain_ci_tmp/artifact" + ArtifactExt + "\n")
	b.WriteString("grain_ci_list=$grain_ci_tmp/paths\n")
	b.WriteString(": > \"$grain_ci_list\"\n")
	for _, path := range spec.Paths {
		b.WriteString("if [ -e '" + path + "' ]; then printf '%s\\n' '" + path + "' >> \"$grain_ci_list\"; fi\n")
	}
	b.WriteString("if [ ! -s \"$grain_ci_list\" ]; then\n")
	b.WriteString("  echo '" + artifactStatusMarker + "none'\n")
	b.WriteString("  rm -rf \"$grain_ci_tmp\"\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n")
	b.WriteString("tar -czf \"$grain_ci_out\" --ignore-failed-read -T \"$grain_ci_list\" 2>\"$grain_ci_tmp/err\"\n")
	b.WriteString("grain_ci_status=$?\n")
	b.WriteString("if [ ! -s \"$grain_ci_out\" ]; then\n")
	b.WriteString("  echo \"" + artifactStatusMarker + "fail tar exited $grain_ci_status: $(tail -c 400 \"$grain_ci_tmp/err\" 2>/dev/null | tr '\\n\\r' '  ')\"\n")
	b.WriteString("  rm -rf \"$grain_ci_tmp\"\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n")
	b.WriteString("grain_ci_size=$(wc -c < \"$grain_ci_out\" | tr -d ' ')\n")
	b.WriteString("if [ \"$grain_ci_size\" -gt " + strconv.FormatInt(maxBytes, 10) + " ]; then\n")
	b.WriteString("  echo \"" + artifactStatusMarker + "big $grain_ci_size\"\n")
	b.WriteString("  rm -rf \"$grain_ci_tmp\"\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n")
	b.WriteString("echo \"" + artifactStatusMarker + "ok $grain_ci_size\"\n")
	// Everything from here to the end marker has to be the payload and
	// nothing else: a warning printed on stderr in the middle of the
	// stream would land inside the base64, since both transports hand
	// back one interleaved string.
	b.WriteString("exec 2>/dev/null\n")
	b.WriteString("echo '" + artifactBeginMarker + "'\n")
	b.WriteString("base64 < \"$grain_ci_out\"\n")
	b.WriteString("echo '" + artifactEndMarker + "'\n")
	b.WriteString("rm -rf \"$grain_ci_tmp\"\n")
	b.WriteString("exit 0\n")
	return b.String()
}

// collectArtifacts copies what a job declared out of its sandbox, before
// runJob's deferred Release destroys the machine.
//
// Only for a job that did not succeed, which is the whole of this
// feature's policy (see artifacts.go). A job with nothing declared, or a
// deployment with nowhere to put files, runs exactly one command fewer
// than it did before this existed: none.
//
// It is given a context of its own rather than the job's. The job whose
// trace somebody wants most is very often the one that ran past its
// timeout, and a collection under an expired context would fail for
// every one of them.
func (e *Engine) collectArtifacts(ctx context.Context, runID string, pj PlannedJob, sandbox Sandbox, result *JobRun) {
	if e.Artifacts == nil || len(pj.Job.Artifacts) == 0 || result.Conclusion == ConclusionSuccess {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactCollectionTimeout)
	defer cancel()
	for _, spec := range pj.Job.Artifacts {
		record := e.collectArtifact(ctx, runID, pj, spec, sandbox)
		result.Artifacts = append(result.Artifacts, record)
		switch {
		case record.Stored:
			e.log("ci: run %s: job %s: kept %s (%s) from %s",
				runID, pj.ID, spec.Name, humanBytes(record.Bytes), strings.Join(spec.Paths, ", "))
		default:
			e.log("ci: run %s: job %s: %s was not kept: %s", runID, pj.ID, spec.Name, record.Note)
		}
	}
}

// collectArtifact is one declaration: run the script, read the answer,
// write it down. It always returns a record -- an artifact that could
// not be collected is an artifact with a note saying why, which is what
// a reader who goes looking for it has to be told.
func (e *Engine) collectArtifact(ctx context.Context, runID string, pj PlannedJob, spec ArtifactSpec, sandbox Sandbox) Artifact {
	ref := ArtifactRef{Run: runID, Job: pj.Job.ID, Name: spec.Name}
	record := Artifact{Run: ref.Run, Job: ref.Job, Name: ref.Name}

	out, err := sandbox.Exec(ctx, Command{
		Script:  artifactScript(spec, e.Artifacts.MaxArtifactBytes()),
		Timeout: artifactCollectionTimeout,
	})
	if err != nil {
		// The transport, not the script: the machine is gone, which is
		// commonly *why* the job failed. Nothing to do but say so where
		// the reader looking for the file will see it.
		record.Note = ArtifactNote("collecting it from the sandbox failed: %v", err)
		return record
	}

	body, note := decodeArtifact(out.Output, spec, e.Artifacts.MaxArtifactBytes())
	if note != "" {
		record.Note = note
		return record
	}
	saved, err := e.Artifacts.Save(ctx, ref, body)
	if err != nil {
		record.Note = ArtifactNote("keeping it failed: %v", err)
		return record
	}
	return saved
}

// decodeArtifact reads the collection script's answer: either the bytes,
// or the note explaining why there are none.
//
// Everything it cannot make sense of is a note rather than an error. The
// output of a command in a sandbox is not a format anybody controls
// end to end -- a transport may frame it, a shell may prepend to it --
// so the rule here is to find the framing and trust nothing outside it.
func decodeArtifact(output string, spec ArtifactSpec, maxBytes int64) ([]byte, string) {
	status, ok := artifactStatus(output)
	if !ok {
		return nil, ArtifactNote("the sandbox did not answer the collection script (its output was %d bytes)", len(output))
	}
	verb, rest, _ := strings.Cut(status, " ")
	switch verb {
	case "none":
		return nil, ArtifactNote("nothing matched %s, so there was nothing to keep", strings.Join(spec.Paths, ", "))
	case "big":
		return nil, ArtifactNote("%s of %s is over the %s a single artifact may be, so it was not copied out of the sandbox",
			humanBytes(parseArtifactSize(rest)), strings.Join(spec.Paths, ", "), humanBytes(maxBytes))
	case "fail":
		return nil, ArtifactNote("archiving %s in the sandbox failed: %s", strings.Join(spec.Paths, ", "), rest)
	case "ok":
	default:
		return nil, ArtifactNote("the collection script answered %q, which this build does not understand", status)
	}

	_, after, found := strings.Cut(output, artifactBeginMarker+"\n")
	if !found {
		return nil, ArtifactNote("the sandbox reported an archive of %s but sent none", rest)
	}
	encoded, _, found := strings.Cut(after, artifactEndMarker)
	if !found {
		return nil, ArtifactNote("the sandbox stopped sending the archive part way through (%d bytes of it arrived)", len(after))
	}
	body, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return nil, ArtifactNote("what the sandbox sent is not the archive it said it was: %v", err)
	}
	if want := parseArtifactSize(rest); want > 0 && int64(len(body)) != want {
		return nil, ArtifactNote("the archive arrived as %d bytes where the sandbox made %d of it", len(body), want)
	}
	return body, ""
}

// artifactStatus picks the status line out of whatever else the
// transport put around it, taking the last one: a script that somehow
// ran twice, or a shell that echoed the command before running it,
// should be read as the answer rather than as the echo.
func artifactStatus(output string) (string, bool) {
	status, found := "", false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, artifactStatusMarker); ok {
			status, found = after, true
		}
	}
	return status, found
}

func parseArtifactSize(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

const (
	goTestLogStatusMarker      = "grain-ci-gotest:"
	goTestLogBeginMarker       = "grain-ci-gotest-begin"
	goTestLogEndMarker         = "grain-ci-gotest-end"
	maxCollectedGoTestLogBytes = 4 << 20 // 4 MiB
)

func jobUsesGoTestLog(job Job, steps []StepRun) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "run-go-tests.sh") ||
			strings.Contains(step.Run, "gotest.log") ||
			strings.Contains(step.Run, "GRAIN_GOTEST_LOG") {
			return true
		}
	}
	for _, step := range steps {
		if strings.Contains(step.Output, "=== per-package results ===") ||
			strings.Contains(step.Output, "=== failing tests ===") {
			return true
		}
	}
	return false
}

func goTestLogScript(maxBytes int64) string {
	var b strings.Builder
	b.WriteString("set +e\n")
	b.WriteString("grain_gotest_file=\"${GRAIN_GOTEST_LOG:-/tmp/gotest.log}\"\n")
	b.WriteString("if [ ! -s \"$grain_gotest_file\" ] && [ -s \"gotest.log\" ]; then\n")
	b.WriteString("  grain_gotest_file=\"gotest.log\"\n")
	b.WriteString("fi\n")
	b.WriteString("if [ ! -s \"$grain_gotest_file\" ]; then\n")
	b.WriteString("  echo '" + goTestLogStatusMarker + "none'\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n")
	b.WriteString("grain_gotest_size=$(wc -c < \"$grain_gotest_file\" 2>/dev/null | tr -d ' \\t\\n\\r')\n")
	b.WriteString("echo \"" + goTestLogStatusMarker + "ok $grain_gotest_size\"\n")
	b.WriteString("exec 2>/dev/null\n")
	b.WriteString("echo '" + goTestLogBeginMarker + "'\n")
	b.WriteString("tail -c " + strconv.FormatInt(maxBytes, 10) + " \"$grain_gotest_file\" | base64\n")
	b.WriteString("echo '" + goTestLogEndMarker + "'\n")
	b.WriteString("exit 0\n")
	return b.String()
}

func (e *Engine) collectGoTestLog(ctx context.Context, runID string, pj PlannedJob, sandbox Sandbox, result *JobRun) {
	if sandbox == nil || result.GoTestLog != "" || !jobUsesGoTestLog(pj.Job, result.Steps) {
		return
	}
	collectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactCollectionTimeout)
	defer cancel()
	out, err := sandbox.Exec(collectCtx, Command{
		Script:  goTestLogScript(maxCollectedGoTestLogBytes),
		Timeout: artifactCollectionTimeout,
	})
	if err != nil {
		e.log("ci: run %s: job %s: collecting /tmp/gotest.log failed: %v", runID, pj.ID, err)
		return
	}
	if logText := decodeGoTestLog(out.Output); logText != "" {
		result.GoTestLog = logText
	}
}

func decodeGoTestLog(output string) string {
	status, found := "", false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, goTestLogStatusMarker); ok {
			status, found = after, true
		}
	}
	if !found || !strings.HasPrefix(status, "ok") {
		return ""
	}
	_, after, ok := strings.Cut(output, goTestLogBeginMarker+"\n")
	if !ok {
		return ""
	}
	encoded, _, ok := strings.Cut(after, goTestLogEndMarker)
	if !ok {
		return ""
	}
	body, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return ""
	}
	return string(body)
}
