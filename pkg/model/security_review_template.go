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

package model

import "strings"

// SecurityReviewTemplateID names the security review: the task grain files
// on a change whose pull request touches a trust-boundary path
// (pkg/trustboundary), and whose auto-merge it withdrew until that review
// finishes (orchestrator's securityhold.go, #2425 Layer 2a).
//
// Nothing seeds a schedule for it and no task declares it: it is filed by
// the hold itself, never through Task.ReviewTemplateID.
const SecurityReviewTemplateID = SystemTemplatePrefix + "security-review"

// securityReviewTemplate is the standing prompt for that review.
//
// Code rather than a `template` row for SystemTemplate's own reason, and
// sharper here than anywhere: this prompt is what stands between a change
// to grain's trust boundary and that change merging itself. A template a
// deployment -- or a repository's own `.grain/` files, which are on the
// list being guarded -- could rewrite is a gate that can be edited open.
//
// The paths that put the change on hold are not in the template. They are
// a fact about one pull request, and the orchestrator appends them to the
// task body it files, under the instructions here.
func securityReviewTemplate() *Template {
	// No grain#ci grant here: a security review works in place on the
	// reviewed change's own branch (orchestrator.putSecurityReview), opens no
	// pull request of its own, and never starts or owns a grain CI run.
	// orchestrator.newReview attaches repo-scoped api:ci-read@<repo> to every
	// review targeting a repository -- the narrowest read-only grant that
	// keeps `grain ci checks`, `grain ci list` and `grain ci log` working on
	// both ci_mode: github and ci_mode: grain repositories without exposing
	// run-triggering tools or changing merge gating.
	return &Template{
		ID:   SecurityReviewTemplateID,
		Name: "Security review of a trust-boundary change",
		Body: securityReviewBody(),
	}
}

const securityReviewBullets = "" +
	"- authentication or authorization weakened, bypassed, or routes added without it;\n" +
	"- credentials, tokens or keys exposed to a sandbox, a log, a comment or a URL, or forwarded " +
	"to a host they were not meant for;\n" +
	"- a sandbox or agent gaining a capability, a network route, a command or a file it did not " +
	"have, or the orchestrator acting on agent output it used to check;\n" +
	"- merge, review or CI gates loosened: auto-merge rules, required checks, baselines, " +
	"quarantined tests, workflow triggers or permissions, the list of trust-boundary paths itself;\n" +
	"- a repository's own `.grain` declarations reaching further: a template's `capabilities:` " +
	"(or its unified spelling, `grants:`) naming another repository, or its `push:` adding a ref, broadening a pattern, or adding " +
	"`force: true` or `delete: true` -- each lets the runs of tasks filed from it move refs " +
	"of this repository beyond their own branch, with no other grant, once it merges;\n" +
	"- a security regression test deleted, skipped or made to assert less than it did;\n" +
	"- images, setup scripts or deployment definitions pulling unpinned or new third-party " +
	"code, or running with more privilege than before;\n" +
	"- anything the change does that its own commit messages do not say it does.\n\n"

// ExternalSecurityReviewBody is the findings-only security review prompt for a
// pull request grain did not open (orchestrator.fileExternalPRReview): the
// same trust-boundary criteria and correctness-second rule as
// securityReviewBody, without the in-place fix/push/auto-merge instructions
// that contradict an external review's read-only checkout.
func ExternalSecurityReviewBody() string {
	var b strings.Builder
	b.WriteString("**This is a security review.** Look for anything that widens what an " +
		"untrusted party can do across grain's trust boundary -- authentication and routes, " +
		"capability grants and secrets, what a sandbox may execute and which tools an agent may " +
		"call, the proxies that hold credentials, what decides a build is green, the security " +
		"tests, deployment, CI configuration, and sandbox images. Read what each touched path " +
		"is for before deciding a change to it is harmless, and look in particular for:\n\n")
	b.WriteString(securityReviewBullets)
	b.WriteString("**Correctness, second.** Your primary job is the trust boundary above; do not " +
		"turn this into a style pass. Even so, a correctness bug you notice while reading is " +
		"still a finding: record it with `add_review_comment`, rather than dropping it as " +
		"\"not a security issue\".\n\n")
	b.WriteString("Do not commit, push or open a pull request; do not push to the branch under review.")
	return b.String()
}

func securityReviewBody() string {
	var b strings.Builder
	b.WriteString("**This is a security review.** The change below touches files on grain's trust " +
		"boundary -- authentication and routes, capability grants and secrets, what a sandbox may " +
		"execute and which tools an agent may call, the proxies that hold credentials, what decides a " +
		"build is green, the security tests, deployment, CI configuration, the images sandboxes are " +
		"built from and upgraded to -- so grain " +
		"has stopped it merging itself. It merges automatically again once this review finishes " +
		"without leaving a finding or a question for its owner -- fixes you push yourself do not " +
		"stop it; otherwise it waits for that owner.\n\n")
	b.WriteString("Review the whole diff against its base, starting with the trust-boundary paths " +
		"listed at the end of this task, and read what each of them is for before deciding a change " +
		"to it is harmless. Look in particular for:\n\n")
	b.WriteString(securityReviewBullets)
	b.WriteString("**Correctness, second.** Your primary job is the trust boundary above; do not " +
		"turn this into a style pass. Even so, a correctness bug you notice while reading is " +
		"still a finding: fix it, or record it with `add_review_comment` when the owner " +
		"must decide, rather than dropping it as \"not a security issue\".\n\n")
	b.WriteString("**Fix what you find; ask only what you cannot settle.** You are working on the " +
		"reviewed change's own branch, not a branch of your own: a fix you commit and push lands " +
		"in the change's own pull request, and there is no second pull request to open. For " +
		"anything with an answer you can stand behind -- a bug, a missing check or test, an unpinned " +
		"or unsigned image, a weakened assertion, docs that no longer match -- make the fix, commit " +
		"and push it to this same branch (fetch and rebase first: the change's own task may have " +
		"pushed since you started). Fixes alone do not need a human: once you finish with none " +
		"left open, the change merges itself, cleared at the head you left -- unless your fixes " +
		"touch the trust boundary themselves, in which case another review reads them first -- " +
		"and anything pushed after that is classified again before it merges. Describe each fix in your closing note " +
		"(comment_on_issue), so a reader can see what you changed without having approved it. " +
		"Never leave a finding you could have fixed.\n\n")
	b.WriteString("**Testing and CI.** The change's own GitHub Actions or grain CI run is the test " +
		"signal: read its result (`grain pr open` / check statuses, `grain ci checks <sha>`, " +
		"`grain ci list --ref @branch`, `grain ci log`, or `grain pr status`) rather than re-running " +
		"suites yourself. Do not run `go test ./...`, package-wide or `-race` runs across packages, " +
		"or long suites: when a fix is pushed, CI re-runs on the new head and the merge queue gates " +
		"on it. Do not call `grain ci wait` (or `wait_for_checks`) to wait for CI to finish -- finish " +
		"the review; grain will not merge a head whose CI has not passed. Do run a narrowly targeted " +
		"test (`go test ./pkg/x -run TestName`) when needed to confirm or refute a specific finding, " +
		"or to check a fix you are about to push compiles and passes its own tests, and say which " +
		"tests you ran in your closing note (`comment_on_issue`). After pushing a fix, " +
		"`go build ./...` or `go vet` on the touched packages is enough locally.\n\n")
	b.WriteString("Use an `add_review_comment` call only for what you cannot or should not fix " +
		"yourself, since that is what sends the change to a human: a design " +
		"decision, a trade-off, or a behaviour change the brief does not settle. Write each as a " +
		"question with the answer you recommend, on the line it is about. Any such call holds the " +
		"change for the owner's decision, and their reply sends another run to carry it out; a " +
		"concern recorded any other way -- a comment, your closing note -- holds nothing. If there " +
		"was nothing to fix or ask, finish with a comment_on_issue saying what you checked.\n\n")
	b.WriteString("When the code is right -- or you have fixed it -- but something about the change is " +
		"still not an agent's call, call `request_human_review` instead of leaving a finding: a product " +
		"or design decision, a risky migration, a security-sensitive behaviour change, or a brief whose " +
		"intent is unclear. Give a one-line `reason`, the context and your recommendation in `details`, " +
		"and a `category`. It holds the change for its owner, who alone may Submit or release it -- a " +
		"clean review, CI or a later push do not -- and your review still finishes normally. Keep using " +
		"findings for code defects an agent can fix; a decision dressed as a finding gets \"fixed\" by " +
		"an agent or reads as a defect.\n\n")
	b.WriteString("Some paths only a human may release (`human: required`, `sensitive: true`, or " +
		"grain's own list of them). Fix what you find there too; the change still waits for its " +
		"owner's Submit, however clean your review is.\n")
	return b.String()
}
