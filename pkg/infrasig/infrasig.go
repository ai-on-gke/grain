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

// Package infrasig is the one list of what a CI step prints when it
// failed on the machine it ran on rather than on the tree it was given:
// a quota, a registry rate limit, a runner that went away, a forge that
// answered a clone with a 5xx, a runner whose DNS stopped answering.
//
// It is a leaf package of its own because two halves of grain ask the
// same question and must not answer it from two lists: pkg/orchestrator
// reads a finished trunk run's failing steps (infraVerdict), and
// pkg/github reads the failing jobs of a pull request's Actions runs
// (github.OnlyInfraFailures) so the merge queue does not spend an
// automatic repair on an outage no commit can clear. The 2026-11-11
// hosted-runner DNS outage was missed by both because the first list had
// no entry for name resolution and the second did not exist.
//
// Every entry is lower case and matched as a substring of the lower-cased
// tail of a failing step's output. A test's output is the tree's, so an
// entry belongs here only if no assertion in a tree plausibly prints it
// by accident; each one says why.
package infrasig

import "strings"

// StepOutput are external rate-limit and registry signatures read out of
// a failing step's own output: GitHub/GCP/registry rate limiting and
// quota, and the docker client's own wording for a registry 5xx.
var StepOutput = []string{
	"429 too many requests", "toomanyrequests", "resource_exhausted", "rate limit exceeded",
	"quota exceeded", "received unexpected http status: 5",
}

// RunnerLost are what GitHub Actions writes when the runner a job was on
// went away mid-step -- the hosted VM lost, or shut down under it. The
// runner's own sentences, which no test prints, so they are read out of
// any failed step.
var RunnerLost = []string{
	"lost communication with the server",
	"the runner has received a shutdown signal",
}

// RunnerNetwork are the runner's resolver failing under the step: the
// shape of the 2026-11-11 outage, where every hosted runner's
// `apt-get install` died on "Temporary failure resolving
// 'security.ubuntu.com'" for three hours and no commit could have
// cleared it.
//
// Deliberately not here on their own: apt's "Failed to fetch" and
// "Unable to fetch some archives". apt prints both for a package that
// was really removed from the mirror (a 404), which a workflow file in
// the tree can fix; they only mean the runner when a resolver failure
// below sits beside them, and then that line is the signature.
var RunnerNetwork = []string{
	// glibc's text for EAI_AGAIN as apt and wget print it: the resolver
	// itself did not answer. A transient property of the machine's
	// network, never a deterministic one of the tree it is building.
	"temporary failure resolving",
	// curl's and git's wording for a hostname the runner could not look
	// up (curl exit 6). The tree's Go tests cannot print it -- Go's own
	// resolver says "no such host" -- and the tree's tests do not shell
	// out to curl against made-up hosts.
	"could not resolve host",
	// glibc's text for EAI_NONAME as apt, wget and python print it when
	// the runner's resolver has lost a name it answered for a minute ago.
	// Go's resolver words the same failure differently, so a Go test in
	// the tree cannot produce this string.
	"name or service not known",
}

// CheckoutOutput are git's own words for a forge that answered a clone
// or fetch with a 5xx (the release runs of 2026-11-01/02 died on them). A
// test could print "502" for any reason, so these are git's whole
// phrases, and they are read only out of a step that checks the tree out
// -- never out of a step that tests it.
var CheckoutOutput = []string{
	"the requested url returned error: 502",
	"the requested url returned error: 503",
	"the requested url returned error: 504",
	"rpc failed; http 502", "rpc failed; http 503", "rpc failed; http 504",
}

// StepTail is how much of a failing step's output is searched: the end,
// which is where a tool that gave up says why.
const StepTail = 4096

// Output is the infrastructure signature in the tail of what one failed
// step printed, or "": the rate-limit, registry, runner-lost and
// runner-network ones from any step, git's 5xx ones only when checkout
// says the step is the one that checks the tree out.
func Output(checkout bool, out string) string {
	if len(out) > StepTail {
		out = out[len(out)-StepTail:]
	}
	for _, list := range [][]string{StepOutput, RunnerLost, RunnerNetwork} {
		if sig := FirstContained(out, list); sig != "" {
			return sig
		}
	}
	if checkout {
		return FirstContained(out, CheckoutOutput)
	}
	return ""
}

// actionsErrorMarker is how a GitHub Actions job log marks the line on
// which a step failed ("##[error]Process completed with exit code 100."),
// and actionsStepMarker how it opens each `run:` step ("##[group]Run
// sudo apt-get ...").
const (
	actionsErrorMarker = "##[error]"
	actionsStepMarker  = "##[group]run "
)

// JobLog is the infrastructure signature in a whole GitHub Actions job
// log's tail, or "". A job log is every step of the job, and the steps
// that passed are not evidence: an apt warning about a mirror that
// answered on the retry, one step before a genuine test failure, must
// not make that failure the runner's. So the output searched is the
// failing step's own -- from the last `##[group]Run ` that opens a step
// before the last line Actions marked `##[error]`, to that line -- and
// only a log without those markers is read by its own end. Read as a
// non-checkout step: a job log is no evidence of which step was the
// checkout.
func JobLog(log string) string {
	lower := strings.ToLower(log)
	if i := strings.LastIndex(lower, actionsErrorMarker); i >= 0 {
		end := len(log)
		if nl := strings.IndexByte(log[i:], '\n'); nl >= 0 {
			end = i + nl
		}
		start := 0
		if j := strings.LastIndex(lower[:i], actionsStepMarker); j >= 0 {
			start = j
		}
		log = log[start:end]
	}
	return Output(false, log)
}

// FirstContained is the first of needles that the lower-cased text
// contains, or "".
func FirstContained(text string, needles []string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return n
		}
	}
	return ""
}
