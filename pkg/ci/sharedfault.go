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
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A shared fault is one failure showing up on several branches at once:
// the same job, failing the same way, on refs that have nothing to do
// with each other. grain already has everything needed to say so -- it
// keeps a run per branch, and each failing job carries the step that
// failed and what that step printed -- and until this file it said
// nothing, so every branch's reader read a deployment-wide outage as
// their own branch's problem.
//
// The afternoon this was written for: every `image` and `guest build
// under real KVM` job in one repository was red on every branch, because
// a commit on main had moved a debootstrap mirror to https:// and the
// deployment's package cache is HTTP-only. Six branches failed with a
// byte-identical 405 from the same proxy inside the same hour, two
// separate agents produced the same diagnosis hours apart, and the merge
// queue spent the afternoon dispatching repairs onto pull requests that
// could not go green until the cause was fixed.
//
// # What makes two failures "the same fault"
//
// This is the whole judgement in the file, and it is deliberately set
// tight. A row that blames the wrong pull request is worse than a row
// that says nothing: it sends somebody to read a change that has nothing
// to do with their red build, and once it has done that twice nobody
// reads the annotation again. So every condition below has to hold, and
// where the evidence is thin the answer is silence.
//
//   - Same repository, and the same job *id* -- the workflow's own
//     identifier, not its display name, since two workflows may perfectly
//     well both call a job "test".
//   - The same failing step, by name, or the same job-level error for a
//     job that never got as far as a step.
//   - A byte-identical digest of that step's *normalized* tail (see
//     normalizeFailure). Identical, not similar: there is no threshold
//     here to tune and no pair of genuinely different test failures that
//     can collide, because two different failing tests print different
//     lines. Normalization only removes what could not possibly be the
//     same across two runs -- clocks, durations, hashes, the ref and
//     commit themselves, the sandbox's name, temporary paths.
//   - Enough text to be worth comparing (minFaultChars). "exit status 1"
//     is the same on every branch in the world and says nothing about
//     why.
//   - Within one window of each other (DefaultFaultWindow), because this
//     is a claim about what is broken *now*. The same job failing the
//     same way last Tuesday and again today is two faults.
//   - On at least two distinct refs. One branch failing twice is a branch
//     with a problem, which is exactly what the annotation must not be
//     confused with.
//
// # What it does not try to be
//
// It does not cluster by similarity, it does not learn, and it has no
// knobs. A looser rule -- shared prefixes, edit distance, "the same
// error class" -- buys the cases where a fault prints a branch name or a
// line number in the middle of its message, and pays for them with the
// one failure mode this cannot afford. If a real fault is missed, the
// reader is exactly where they were before this file existed.

// DefaultFaultWindow is how far apart two failures may be and still be
// called one fault.
//
// Six hours rather than an hour, because the shape this is for is an
// afternoon: a bad commit lands on a base branch, and branches discover
// it one at a time as each is dispatched, merged or re-run over the rest
// of the working day. Rather than a day, because a window that wide
// starts joining this morning's outage to yesterday's unrelated one, and
// a fault nobody has fixed within six hours will keep proving itself
// with fresh runs anyway.
const DefaultFaultWindow = 6 * time.Hour

// faultTailLines is how much of a failing step's output the digest is
// taken over. The tail, because that is where a failure says what went
// wrong -- a step's head is what it was doing, which is identical on
// every branch whether or not they share a fault.
const faultTailLines = 20

// minFaultChars is how much normalized text a failure has to have before
// it is comparable at all. Below it there is nothing distinctive to be
// wrong about: "exit status 1", "FAIL", "Error: 1" are every failure in
// the repository, and grouping on them would put unrelated branches
// under one heading on the strength of nothing.
const minFaultChars = 40

// maxFaultPaths bounds how many files one failure is allowed to name.
// A stack trace can name hundreds; the ones worth reading are at the
// point the failure was reported, which is where extraction starts.
const maxFaultPaths = 12

// FaultSource identifies which CI system a shared fault came from.
// Correlation is strictly within a source: a grain-CI failure and a
// GitHub-CI failure never join into one SharedFault.
const (
	FaultSourceGrain  = "grain"
	FaultSourceGitHub = "github"
)

func faultSourceOf(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return FaultSourceGrain
}

// FaultRun is one branch's share of a shared fault.
type FaultRun struct {
	RunID  string    `json:"runId"`
	Ref    string    `json:"ref"`
	TaskID string    `json:"taskId,omitempty"`
	Commit string    `json:"commit,omitempty"`
	At     time.Time `json:"at"`
}

// SharedFault is one fault seen on several branches: what failed, how,
// and every run it has been seen in.
type SharedFault struct {
	// Source is which CI system reported these failures ("grain" or "github").
	Source  string     `json:"source"`
	Repo    string     `json:"repo"`
	JobID   string     `json:"jobId"`
	JobName string     `json:"jobName"`
	Step    string     `json:"step,omitempty"`
	Kind    Conclusion `json:"kind"`
	// Digest is carried over the wire so that a reader can tell one
	// fault's annotation from another's on a row that has both, and so
	// that two reports of "the same failure" can be checked against each
	// other rather than taken on trust.
	Digest string `json:"digest"`
	// Refs are the distinct branches this fault has been seen on, newest
	// failure first. Runs is one entry per ref, in the same order.
	Refs []string   `json:"refs"`
	Runs []FaultRun `json:"runs"`
	// Paths are the files every one of those runs' output named -- the
	// intersection, not the union, so a path that is really one branch's
	// own is not offered as the fault's. Empty where the failures name no
	// file they agree on, which is the ordinary case for an outage in the
	// network rather than in the tree.
	Paths []string `json:"paths,omitempty"`
	// OwnerTaskID is the single task assigned to fix this shared fault:
	// either an active base-repair / FixesBaseBranch task, or the first
	// open task in queue order hitting this fault.
	OwnerTaskID string `json:"ownerTaskId,omitempty"`
	// OwnerRef is the branch ref of OwnerTaskID when known.
	OwnerRef string `json:"ownerRef,omitempty"`
}

// OwnedBy reports whether refOrTaskID is the task/branch assigned to fix this fault.
func (f SharedFault) OwnedBy(refOrTaskID string) bool {
	refOrTaskID = strings.TrimSpace(refOrTaskID)
	if refOrTaskID == "" {
		return false
	}
	if f.OwnerRef != "" && strings.TrimPrefix(refOrTaskID, "refs/heads/") == strings.TrimPrefix(f.OwnerRef, "refs/heads/") {
		return true
	}
	if f.OwnerTaskID == "" {
		return false
	}
	tid := TaskIDFromRef(refOrTaskID)
	if tid == "" {
		tid = refOrTaskID
	}
	return f.OwnerTaskID == tid || f.OwnerTaskID == refOrTaskID
}

// OwnedByOther reports whether this fault has an assigned owner task and
// refOrTaskID is not that owner.
func (f SharedFault) OwnedByOther(refOrTaskID string) bool {
	if f.OwnerTaskID == "" && f.OwnerRef == "" {
		return false
	}
	return !f.OwnedBy(refOrTaskID)
}

// TaskIDFromRef extracts a grain task ID from a branch ref such as
// "grain/task-123" or "grain/049f1e09/task-73". Returns "" if ref is not
// a task branch.
func TaskIDFromRef(ref string) string {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "refs/heads/"))
	if ref == "" {
		return ""
	}
	parts := strings.Split(ref, "/")
	last := parts[len(parts)-1]
	if id, ok := strings.CutPrefix(last, "task-"); ok && id != "" {
		return id
	}
	return ""
}

// TaskIDFromRun extracts the task ID associated with run, either from its
// branch ref or its RequestedBy field when not a base/scheduled run.
func TaskIDFromRun(run Run) string {
	if id := TaskIDFromRef(run.Ref); id != "" {
		return id
	}
	req := strings.TrimSpace(run.RequestedBy)
	if req != "" && !strings.HasPrefix(req, "workflow-schedule:") && run.Ref != "main" && run.Ref != "master" {
		return req
	}
	return ""
}

// CompareTaskIDs orders two task IDs in queue tie-break order: numeric task
// IDs (or "t123"/"task-123") are compared by integer value; non-numeric IDs
// fall back to lexicographical order.
func CompareTaskIDs(a, b string) int {
	if a == b {
		return 0
	}
	na, oka := parseTaskNumber(a)
	nb, okb := parseTaskNumber(b)
	if oka && okb && na != nb {
		if na < nb {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func parseTaskNumber(id string) (int64, bool) {
	s := strings.TrimSpace(id)
	s = strings.TrimPrefix(s, "task-")
	s = strings.TrimPrefix(s, "t")
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Seen is how many branches this fault has been seen on.
func (f SharedFault) Seen() int { return len(f.Runs) }

// Others is every ref in the fault but this one -- what a row on that
// ref's own run says out loud ("failing on N other branches").
func (f SharedFault) Others(ref string) []string {
	out := make([]string, 0, len(f.Refs))
	for _, r := range f.Refs {
		if r != ref {
			out = append(out, r)
		}
	}
	return out
}

// SharedFaults groups the failing jobs in runs into the faults that span
// more than one branch, newest first.
//
// runs must carry their steps' output -- a listing does not (RunStore.
// List drops it), so a caller works from whole runs it has read back.
// Runs with no output simply produce no signature and fall out, rather
// than being reported as a fault with nothing behind it.
//
// now and window bound what counts as "at once"; a zero window means
// DefaultFaultWindow.
func SharedFaults(runs []Run, now time.Time, window time.Duration) []SharedFault {
	if window <= 0 {
		window = DefaultFaultWindow
	}
	type member struct {
		sig Signature
		run FaultRun
	}
	type faultGroupKey struct {
		source string
		digest string
	}
	groups := map[faultGroupKey][]member{}
	for _, run := range runs {
		if run.Ref == "" {
			continue
		}
		source := faultSourceOf(run.Source)
		for _, job := range run.Jobs {
			sig, ok := FailureSignature(run, job)
			if !ok {
				continue
			}
			sig.Source = source
			at := faultTime(run, job)
			if at.IsZero() || now.Sub(at) > window || at.After(now.Add(time.Minute)) {
				continue
			}
			key := faultGroupKey{source: source, digest: sig.Digest}
			groups[key] = append(groups[key], member{
				sig: sig,
				run: FaultRun{RunID: run.ID, Ref: run.Ref, TaskID: TaskIDFromRun(run), Commit: run.Commit, At: at},
			})
		}
	}

	var out []SharedFault
	for _, members := range groups {
		sort.SliceStable(members, func(i, j int) bool { return members[i].run.At.After(members[j].run.At) })
		// One run per ref -- the newest. A branch that has failed the same
		// way four times running is one branch with a red build, and
		// counting it four times would let a single branch manufacture a
		// "shared" fault all by itself.
		seen := map[string]bool{}
		kept := members[:0:0]
		for _, m := range members {
			if seen[m.run.Ref] {
				continue
			}
			seen[m.run.Ref] = true
			kept = append(kept, m)
		}
		if len(kept) < 2 {
			continue
		}
		// The window is measured across the members themselves, not only
		// against now: three failures this hour and one six hours ago are
		// today's fault, and the old run is dropped rather than allowed to
		// stretch the claim.
		newest := kept[0].run.At
		within := kept[:0:0]
		for _, m := range kept {
			if newest.Sub(m.run.At) <= window {
				within = append(within, m)
			}
		}
		if len(within) < 2 {
			continue
		}
		fault := SharedFault{
			Source:  faultSourceOf(within[0].sig.Source),
			Repo:    within[0].sig.Repo,
			JobID:   within[0].sig.JobID,
			JobName: within[0].sig.JobName,
			Step:    within[0].sig.Step,
			Kind:    within[0].sig.Kind,
			Digest:  within[0].sig.Digest,
		}
		for i, m := range within {
			fault.Refs = append(fault.Refs, m.run.Ref)
			fault.Runs = append(fault.Runs, m.run)
			if i == 0 {
				// Seeded from the newest member rather than intersected
				// with a nil, which commonPaths reads as "nothing known
				// yet" -- a first member that named no file must narrow the
				// fault to no files, not adopt the second member's.
				fault.Paths = m.sig.Paths
				continue
			}
			fault.Paths = commonPaths(fault.Paths, m.sig.Paths)
		}
		fault.OwnerTaskID, fault.OwnerRef = defaultFaultOwner(fault.Runs)
		out = append(out, fault)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Runs[0].At.Equal(out[j].Runs[0].At) {
			return out[i].Runs[0].At.After(out[j].Runs[0].At)
		}
		if out[i].Digest != out[j].Digest {
			return out[i].Digest < out[j].Digest
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func defaultFaultOwner(runs []FaultRun) (taskID, ref string) {
	var bestAt time.Time
	for _, r := range runs {
		tid := r.TaskID
		if tid == "" {
			tid = TaskIDFromRef(r.Ref)
		}
		if tid == "" {
			continue
		}
		if taskID == "" {
			taskID, ref, bestAt = tid, r.Ref, r.At
			continue
		}
		cmp := CompareTaskIDs(tid, taskID)
		if cmp < 0 || (cmp == 0 && (r.At.Before(bestAt) || (r.At.Equal(bestAt) && r.Ref < ref))) {
			taskID, ref, bestAt = tid, r.Ref, r.At
		}
	}
	return taskID, ref
}

// FaultFor finds the fault a given run's given job belongs to, if any --
// how a row looks itself up in what SharedFaults returned.
func FaultFor(faults []SharedFault, runID, jobID string) (SharedFault, bool) {
	for _, f := range faults {
		if f.JobID != jobID {
			continue
		}
		for _, r := range f.Runs {
			if r.RunID == runID {
				return f, true
			}
		}
	}
	return SharedFault{}, false
}

// FailureSignature reduces one failing job to its Signature, or reports
// ok=false where there is nothing comparable: a job that did not fail, a
// job whose output was dropped (a listing), or a failure with too little
// distinctive text to be worth matching on (minFaultChars).
func FailureSignature(run Run, job JobRun) (Signature, bool) {
	if cached, classified := job.FaultSignatureCache(); classified {
		if cached == nil {
			return Signature{}, false
		}
		return *cached, true
	}
	var kind Conclusion
	switch job.Conclusion {
	case ConclusionFailure, ConclusionErrored:
		kind = job.Conclusion
	default:
		return Signature{}, false
	}

	step, text := failureText(job)
	lines, paths := normalizeFailure(text, run, job)
	if len(lines) == 0 {
		return Signature{}, false
	}
	if len(lines) > faultTailLines {
		lines = lines[len(lines)-faultTailLines:]
	}
	joined := strings.Join(lines, "\n")
	if len(joined) < minFaultChars {
		return Signature{}, false
	}
	// The digest covers what the failure is *and* where it happened, so
	// two identically-worded failures of different jobs can never be one
	// fault -- the caller then only has to compare digests.
	sum := sha256.Sum256([]byte(strings.Join([]string{
		run.Repo, job.JobID, step, string(kind), joined,
	}, "\x00")))
	return Signature{
		Source:  faultSourceOf(run.Source),
		Repo:    run.Repo,
		JobID:   job.JobID,
		JobName: job.Name,
		Step:    step,
		Kind:    kind,
		Digest:  hex.EncodeToString(sum[:]),
		Paths:   paths,
	}, true
}

// failureText is what a failing job has to say for itself: the first
// failing step's output, or -- for a job that failed without one -- the
// job's own error.
//
// The first failing step rather than the last, because the steps after
// it are skipped and everything downstream of a failure is a consequence
// of it. Its Error is folded in beside its output: a step whose *call*
// failed (a transport that stopped answering) may have printed nothing
// at all, and that error is then the only description of the failure
// there is.
func failureText(job JobRun) (step string, text string) {
	for _, s := range job.Steps {
		if s.Conclusion != ConclusionFailure && s.Error == "" {
			continue
		}
		parts := []string{}
		if s.Output != "" {
			parts = append(parts, s.Output)
		}
		if s.Error != "" {
			parts = append(parts, s.Error)
		}
		return s.Name, strings.Join(parts, "\n")
	}
	return "", job.Error
}

// faultTime is when a failing job failed: its own finish, falling back to
// the run's, so that a job read back from a store with no per-job
// timestamps still lands in the right window.
func faultTime(run Run, job JobRun) time.Time {
	switch {
	case job.FinishedAt != nil:
		return *job.FinishedAt
	case run.FinishedAt != nil:
		return *run.FinishedAt
	case run.StartedAt != nil:
		return *run.StartedAt
	}
	return time.Time{}
}

var (
	ansiRe                = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	actionsLogTimestampRe = regexp.MustCompile("^\ufeff?\\d{4}-\\d{2}-\\d{2}T[0-9:.]+Z ?")
	actionsMarkerRe       = regexp.MustCompile(`##\[(?:group|endgroup|error|warning)\]\s*`)
	linuxRunnerWorkRelRe  = regexp.MustCompile(`/home/runner/work/([^/\s"':_][^/\s"':]*)/([^/\s"':_][^/\s"':]*)/`)
	linuxRunnerWorkBareRe = regexp.MustCompile(`/home/runner/work/([^/\s"':_][^/\s"':]*)/([^/\s"':_][^/\s"':]*)\b`)
	linuxRunnerTempRe     = regexp.MustCompile(`/home/runner/work/_temp/[^\s"':]+`)
	linuxRunnerToolRe     = regexp.MustCompile(`/home/runner/work/_tool/([^/\s"':]+)/[^/\s"':]+(?:/(?:x64|x86|arm64|arm))?`)
	hostedToolcacheRe     = regexp.MustCompile(`/opt/hostedtoolcache/([^/\s"':]+)/[^/\s"':]+(?:/(?:x64|x86|arm64|arm))?`)
	winRunnerTempRe       = regexp.MustCompile(`[A-Za-z]:[\\/]a[\\/]_temp[\\/][^\s"':]+`)
	winRunnerToolRe       = regexp.MustCompile(`[A-Za-z]:[\\/]a[\\/]_tool[\\/]([^\\/\s"':]+)[\\/][^\\/\s"':]+(?:[\\/](?:x64|x86|arm64|arm))?([\\/][^\s"':]*)?`)
	winHostedToolcacheRe  = regexp.MustCompile(`[A-Za-z]:[\\/]hostedtoolcache[\\/]windows[\\/]([^\\/\s"':]+)[\\/][^\\/\s"':]+(?:[\\/](?:x64|x86|arm64|arm))?([\\/][^\s"':]*)?`)
	winRunnerWorkRelRe    = regexp.MustCompile(`[A-Za-z]:[\\/]a[\\/]([^\\/\s"':_][^\\/\s"':]*)[\\/]([^\\/\s"':_][^\\/\s"':]*)[\\/]([^\s"':]+)`)
	winRunnerWorkBareRe   = regexp.MustCompile(`[A-Za-z]:[\\/]a[\\/]([^\\/\s"':_][^\\/\s"':]*)[\\/]([^\\/\s"':_][^\\/\s"':]*)[\\/]?`)
	isoTimeRe             = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	clockRe               = regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	durationRe            = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)\b`)
	hexRe                 = regexp.MustCompile(`\b[0-9a-fA-F]{7,}\b`)
	tmpRe                 = regexp.MustCompile(`/tmp/[A-Za-z0-9_.@+-]+`)
	urlRe                 = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)
	// A path is something with a directory in it and an extension on the
	// end. Both halves matter: a bare word is not a file, and a token with
	// no extension is as likely to be a URL path, a job id or a sentence
	// as a source file. This is the conservative end of a conservative
	// feature -- a failure that names its file in some other shape simply
	// names none.
	pathRe = regexp.MustCompile(`(?:\.{0,2}/)?(?:[A-Za-z0-9_.@+-]+/)+[A-Za-z0-9_.@+-]+\.[A-Za-z0-9]{1,8}`)
)

// systemRoots are absolute paths that cannot be a file in the repository
// under test, however much they look like one. Dropping them is not an
// optimisation: a log full of /usr/lib/... is a log full of candidate
// paths to match a pull request against, and every one of them is a
// chance to blame the wrong change.
var systemRoots = []string{
	"/usr/", "/etc/", "/var/", "/lib/", "/lib64/", "/bin/", "/sbin/",
	"/proc/", "/sys/", "/dev/", "/opt/", "/snap/", "/nix/", "/root/.",
	"/home/runner/",
}

// noisyDirs are directories whose contents are not this repository's
// source even when they sit inside the checkout.
var noisyDirs = []string{"node_modules/", "vendor/", ".git/", ".cache/", "dist/", "build/"}

// normalizeFailure turns a failing step's output into the lines a digest
// is taken over, and the file paths it names.
//
// Everything masked here is something that cannot be equal across two
// runs of the same fault and is not evidence about the fault itself:
// clocks, elapsed times, hashes and container ids, the ref and commit
// under test, the sandbox the job ran in, temporary directories, and
// GitHub Actions log framing (RFC3339 line timestamps, ##[...] workflow
// markers, and runner checkout/toolcache paths). Numbers
// are otherwise left exactly as they are -- the 405 in "the requested URL
// returned error: 405" is the whole of what makes that failure that
// failure, and a normalizer that scrubbed digits would have joined it to
// every other proxy error in the deployment.
func normalizeFailure(text string, run Run, job JobRun) (lines []string, paths []string) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	seenPath := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		line := ansiRe.ReplaceAllString(raw, "")
		line = strings.TrimRight(line, " \t\r")
		line = actionsLogTimestampRe.ReplaceAllString(line, "")
		line = actionsMarkerRe.ReplaceAllString(line, "")
		line = normalizeRunnerPaths(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, p := range pathsIn(line) {
			if seenPath[p] || len(paths) >= maxFaultPaths {
				continue
			}
			seenPath[p] = true
			paths = append(paths, p)
		}
		lines = append(lines, maskVolatile(line, run, job))
	}
	return lines, paths
}

// normalizeRunnerPaths strips GitHub Actions runner checkout prefixes
// (/home/runner/work/<repo>/<repo>/ and D:\a\<repo>\<repo>\) so repository
// files read as repo-relative paths across runners, and masks runner temp and
// hostedtoolcache version directories (/opt/hostedtoolcache/..., D:\a\_temp\...,
// D:\a\_tool\...).
func normalizeRunnerPaths(line string) string {
	line = linuxRunnerTempRe.ReplaceAllString(line, "/tmp/<tmp>")
	line = linuxRunnerToolRe.ReplaceAllString(line, "/opt/hostedtoolcache/$1/_ver_")
	line = linuxRunnerWorkRelRe.ReplaceAllString(line, "")
	line = linuxRunnerWorkBareRe.ReplaceAllString(line, "<work>")
	line = hostedToolcacheRe.ReplaceAllString(line, "/opt/hostedtoolcache/$1/_ver_")
	line = winRunnerTempRe.ReplaceAllString(line, "/tmp/<tmp>")
	line = replaceWinToolcache(winRunnerToolRe, line)
	line = replaceWinToolcache(winHostedToolcacheRe, line)
	line = winRunnerWorkRelRe.ReplaceAllStringFunc(line, func(m string) string {
		sub := winRunnerWorkRelRe.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return strings.ReplaceAll(sub[3], `\`, "/")
	})
	line = winRunnerWorkBareRe.ReplaceAllString(line, "<work>")
	return line
}

func replaceWinToolcache(re *regexp.Regexp, line string) string {
	return re.ReplaceAllStringFunc(line, func(m string) string {
		sub := re.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		rest := ""
		if len(sub) >= 3 {
			rest = strings.ReplaceAll(sub[2], `\`, "/")
		}
		return "/opt/hostedtoolcache/" + sub[1] + "/_ver_" + rest
	})
}

// maskVolatile is the per-line half of normalizeFailure.
func maskVolatile(line string, run Run, job JobRun) string {
	// The run's own identifiers first: a commit is a hex run and a
	// sandbox name may be anything, so replacing them by name is both
	// more precise than the patterns below and has to happen before them.
	for _, pair := range [][2]string{
		{run.Ref, "<ref>"},
		{run.Commit, "<commit>"},
		{run.ID, "<run>"},
		{job.Sandbox, "<sandbox>"},
	} {
		if len(pair[0]) >= 4 {
			line = strings.ReplaceAll(line, pair[0], pair[1])
		}
	}
	line = isoTimeRe.ReplaceAllString(line, "<time>")
	line = clockRe.ReplaceAllString(line, "<clock>")
	line = durationRe.ReplaceAllString(line, "<dur>")
	line = tmpRe.ReplaceAllString(line, "/tmp/<tmp>")
	line = hexRe.ReplaceAllString(line, "<hex>")
	return strings.TrimSpace(line)
}

// pathsIn is the file paths one line of output names.
//
// URLs go first and whole: a URL's own path looks exactly like a file
// path, and "https://deb.example/pool/main/g/grain/grain_1.0.deb" is not
// a file in anybody's repository.
func pathsIn(line string) []string {
	line = urlRe.ReplaceAllString(line, " ")
	var out []string
	for _, m := range pathRe.FindAllString(line, -1) {
		if p, ok := cleanPath(m); ok {
			out = append(out, p)
		}
	}
	return out
}

// cleanPath trims what a log puts around a path and rejects what cannot
// be a file in the repository under test.
func cleanPath(p string) (string, bool) {
	p = strings.Trim(p, `.,;:'"()[]{}<>`)
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasSuffix(p, "/") {
		return "", false
	}
	for _, root := range systemRoots {
		if strings.HasPrefix(p, root) {
			return "", false
		}
	}
	for _, dir := range noisyDirs {
		if strings.Contains(p, "/"+dir) || strings.HasPrefix(p, dir) {
			return "", false
		}
	}
	return p, true
}

// SameFile reports whether a path a failure printed and a path a pull
// request touches are the same file.
//
// Equal, or one a whole-segment suffix of the other, because a log prints
// both spellings of the same file: a checkout's absolute path
// ("/home/agent/work/scripts/build.sh") and a repo-relative one
// ("scripts/build.sh"), and a compiler may print either. The suffix has
// to land on a separator so that "lib/state.go" is never the same file as
// "ui/relib/state.go".
func SameFile(printed, touched string) bool {
	printed = strings.TrimPrefix(strings.TrimSpace(printed), "./")
	touched = strings.TrimPrefix(strings.TrimSpace(touched), "./")
	if printed == "" || touched == "" {
		return false
	}
	if printed == touched {
		return true
	}
	return strings.HasSuffix(printed, "/"+touched) || strings.HasSuffix(touched, "/"+printed)
}

// commonPaths is the intersection of two path lists, keeping the order
// of the first. A path only one member of a fault printed is that
// member's own -- a branch's test file, a generated artifact -- and
// offering it as the fault's would be the annotation naming a change
// that has nothing to do with anybody else's red build.
//
// A plain intersection, with no "empty means everything" case in it: the
// caller seeds the list from the first member (see SharedFaults), and
// every member after it can only narrow what is left.
func commonPaths(have, next []string) []string {
	out := have[:0:0]
	for _, p := range have {
		for _, q := range next {
			if SameFile(p, q) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
