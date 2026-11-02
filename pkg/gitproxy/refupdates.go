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

package gitproxy

// Which refs a push may update. Authorize answers per repository, which
// is all a fetch can be answered at (a sandbox allowed a repository gets
// every object in it), but a push names the refs it moves in plain text
// at the very start of its body, ahead of the packfile:
//
//	<old-oid> SP <new-oid> SP <refname> [NUL <capabilities>] LF
//
// one pkt-line per ref, ended by a flush-pkt. That command list is
// read here and checked before the request is forwarded, so a sandbox
// that may push to acme/widgets may still push only its own task's
// branch of it -- never the default branch, another task's branch, or a
// tag -- whatever branch protection GitHub happens to have, and whatever
// the credential grain forwards with could do there.
//
// The parser fails closed: a body whose command list it cannot read in
// full is refused, never forwarded on the guess that it is harmless. It
// reads only the command list, never the packfile after it, so there is
// no pack parsing here to get subtly wrong.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// RefUpdate is one command of a push: move Ref from Old to New. An
// all-zero New deletes Ref; an all-zero Old creates it.
type RefUpdate struct {
	Old, New, Ref string
}

// RefAuthorizer is an Authorizer that also decides which refs a push may
// update. Optional, like Refuser: Handle asks for it and carries on
// without it, so a test stub need not implement it. ModelAuthorizer does.
type RefAuthorizer interface {
	// AuthorizeRefUpdates returns a non-empty reason when sandbox may not
	// make updates to owner/repo. The reason is returned to the caller.
	AuthorizeRefUpdates(ctx context.Context, sandbox, owner, repo string, updates []RefUpdate) (reason string, err error)
}

// BranchLookup resolves a sandbox to the one branch its live run may
// push. model.Store implements this via GitPushBranch; a test supplies a
// stub instead. live false means no live run, and so no branch.
type BranchLookup interface {
	GitPushBranch(ctx context.Context, sandbox string) (branch string, live bool, err error)
}

// AnyRefLookup is the optional half of BranchLookup that answers whether
// a sandbox's live run holds model.GitPushAnyRefGrant (or the retired
// bare git-push-any-ref it replaced), the team grant that lifts the one-branch rule. model.Store implements it; a
// lookup that does not is read as nobody holding it.
type AnyRefLookup interface {
	GitPushAnyRef(ctx context.Context, sandbox string) (bool, error)
}

// FastForwardOnlyLookup is the optional half of BranchLookup that answers
// whether a sandbox's live run may only fast-forward its own branch,
// because that branch is another task's: a security review working on
// the change it reviews (model.Store.GitPushFastForwardOnly). A lookup
// that does not implement it is read as no run being bound this way.
type FastForwardOnlyLookup interface {
	GitPushFastForwardOnly(ctx context.Context, sandbox string) (bool, error)
}

// A lookup without GitPushFastForwardOnly reads as nothing bound, so the
// store the proxy is built over (Config.Store) must keep answering it.
var _ FastForwardOnlyLookup = (*model.Store)(nil)

// DeclaredPushLookup is the optional half of BranchLookup that answers
// which refs beyond its own branch a sandbox's live run may push by
// declaration: the `push:` list of the repository-declared template its
// task was filed from, as that repository's default branch has it
// (model.Store.GitDeclaredPush, grain/task-2536). A lookup that does not
// implement it is read as no run having one.
type DeclaredPushLookup interface {
	GitDeclaredPush(ctx context.Context, sandbox string) (*model.DeclaredPush, error)
}

// ConfigProposalLookup is the optional half of BranchLookup that answers
// which grain/config branches a sandbox's live run may push beyond its
// own: the proposal branches propose_config_change pushes, under
// model.ConfigProposalBranchPrefix, for a run whose task holds
// grain/config in its Writes (model.Store.GitConfigProposalPrefix). ""
// is a run with none. A lookup that does not implement it is read as no
// run having any.
type ConfigProposalLookup interface {
	GitConfigProposalPrefix(ctx context.Context, sandbox string) (string, error)
}

var _ ConfigProposalLookup = (*model.Store)(nil)

// RefGrant is one update of a ref other than the run's own branch that
// AuthorizeRefUpdates let through, and on whose say-so -- what the audit
// log records for it (GitProxy.refuseRefUpdates).
type RefGrant struct {
	Ref string
	// Kind is "create", "update", "force" or "delete".
	Kind string
	// Via is "declared push" or the capability's name.
	Via         string
	TaskID      string
	TemplateID  string
	Declaration string
}

// pushCheck is what GitProxy.refuseRefUpdates hands AuthorizeRefUpdates
// on the context beyond the command list: the decoded request body, for
// the packfile a fast-forward is shown from (pack.go), and somewhere to
// put the grants it made for the audit log. Carried on the context
// rather than in the signature so RefAuthorizer stays the one method
// every authorizer and test stub already implements.
type pushCheck struct {
	// body opens the decoded request body from its start. It is opened
	// only if a ref asks for the pack, and read as a stream: a push
	// spooled to disk is never held in memory whole.
	body func() (io.Reader, error)
	// upstream fetches, from the repository being pushed to, the commits
	// reachable from wants and not from have: where a walk that leaves
	// the pack carries on (ancestry.go). Nil when there is nobody to ask.
	upstream func(wants []string, have string, hashLen int) (packCommits, error)
	grants   []RefGrant

	commits    packCommits
	commitsErr error
	read       bool
}

type pushCheckKey struct{}

func withPushCheck(ctx context.Context, c *pushCheck) context.Context {
	return context.WithValue(ctx, pushCheckKey{}, c)
}

func pushCheckFrom(ctx context.Context) *pushCheck {
	c, _ := ctx.Value(pushCheckKey{}).(*pushCheck)
	return c
}

// packCommits reads the push's pack once, however many refs ask.
func (c *pushCheck) packCommits(hashLen int) (packCommits, error) {
	if c == nil || c.body == nil {
		return nil, errors.New("no packfile in hand")
	}
	if !c.read {
		c.read = true
		body, err := c.body()
		if err == nil {
			r := bufio.NewReaderSize(body, 64<<10)
			if err = skipToPack(r); err == nil {
				c.commits, err = readPackStream(r, hashLen)
			}
		}
		c.commitsErr = err
	}
	return c.commits, c.commitsErr
}

// fastForward reports whether newTip is shown to descend from oldTip:
// by the commits the push carries, or -- where a walk through them runs
// out, as it does at once when the remote already has newTip -- by the
// commits the remote has (ancestry.go). why says what stopped a proof
// that was not made, when that is more than the walk running out.
func (c *pushCheck) fastForward(newTip, oldTip string) (ok bool, why string) {
	hashLen := len(newTip) / 2
	commits, err := c.packCommits(hashLen)
	if errors.Is(err, errNoPack) {
		commits, err = packCommits{}, nil
	}
	if err != nil {
		return false, ""
	}
	if commits.descends(newTip, oldTip) {
		return true, ""
	}
	if c.upstream == nil {
		return false, ""
	}
	wants, ok := commits.frontier(newTip, maxFrontierCommits)
	if !ok || len(wants) == 0 {
		return false, ""
	}
	fetched, err := c.upstream(wants, oldTip, hashLen)
	if err != nil {
		return false, err.Error()
	}
	for _, w := range wants {
		if fetched.descends(w, oldTip) {
			return true, ""
		}
	}
	return false, ""
}

func (c *pushCheck) grant(g ...RefGrant) {
	if c != nil {
		c.grants = append(c.grants, g...)
	}
}

// isZeroID is the all-zero object name a command uses for "no ref".
func isZeroID(id string) bool { return strings.Trim(id, "0") == "" }

// updateKind is what one update does to its ref.
func updateKind(u RefUpdate) string {
	switch {
	case isZeroID(u.New):
		return "delete"
	case isZeroID(u.Old):
		return "create"
	default:
		return "update"
	}
}

// branches is where ModelAuthorizer reads a sandbox's push branch from:
// Branches if set, else Store when it can answer (model.Store can), else
// nothing -- which refuses every push rather than allowing every ref.
func (a *ModelAuthorizer) branches() BranchLookup {
	if a.Branches != nil {
		return a.Branches
	}
	if b, ok := a.Store.(BranchLookup); ok {
		return b
	}
	return nil
}

// AuthorizeRefUpdates implements RefAuthorizer: every update must be to
// refs/heads/<the run's own branch>, in whichever repository it may push
// to -- its target or any Writes entry alike. Creating, moving,
// force-moving and deleting that one branch are all its own business --
// except for a run whose branch is another task's, which may only
// fast-forward it (refuseBorrowedBranchRewrite). Every other ref is refused, the default branch and the Writes entries'
// base branches included, unless one of two things says otherwise:
//
//   - the `push:` list of the repository-declared template the run's
//     task was filed from (DeclaredPushLookup, grain/task-2536), for refs
//     of that same repository -- with a deletion allowed only where the
//     matching entry says `delete: true`, and a tag moved or a branch
//     moved anywhere the pack does not show is a fast-forward only where
//     it says `force: true`;
//   - model.GitPushAnyRefGrant, the https://grain.dev/capabilities/repo#write
//     grant formerly named git-push-any-ref (AnyRefLookup), a team grant
//     for ad-hoc work.
//
// No team policy and no admin grant sits behind either: the team's own
// git credential, which every push is forwarded with, is the only other
// bound.
func (a *ModelAuthorizer) AuthorizeRefUpdates(ctx context.Context, sandbox, owner, repo string, updates []RefUpdate) (string, error) {
	lookup := a.branches()
	if lookup == nil {
		return "this proxy cannot tell which branch this sandbox may push, so it refuses every push", nil
	}
	branch, live, err := lookup.GitPushBranch(ctx, sandbox)
	if err != nil {
		return "", fmt.Errorf("gitproxy: resolving push branch for %s: %w", sandbox, err)
	}
	if !live || branch == "" {
		return "this sandbox has no live task, so there is no branch it may push", nil
	}
	own := "refs/heads/" + branch
	var others []RefUpdate
	for _, u := range updates {
		if u.Ref != own {
			others = append(others, u)
		}
	}
	if reason, err := a.refuseBorrowedBranchRewrite(ctx, sandbox, own, updates); reason != "" || err != nil {
		return reason, err
	}
	if len(others) == 0 {
		return "", nil
	}
	check := pushCheckFrom(ctx)
	if others, err = a.configProposalUpdates(ctx, sandbox, owner, repo, others, check); err != nil || len(others) == 0 {
		return "", err
	}
	if anyRef, ok := lookup.(AnyRefLookup); ok {
		held, err := anyRef.GitPushAnyRef(ctx, sandbox)
		if err != nil {
			return "", fmt.Errorf("gitproxy: resolving push grants for %s: %w", sandbox, err)
		}
		if held {
			for _, u := range others {
				check.grant(RefGrant{Ref: u.Ref, Kind: updateKind(u), Via: model.RepoWriteRef})
			}
			return "", nil
		}
	}

	var declared *model.DeclaredPush
	if d, ok := lookup.(DeclaredPushLookup); ok {
		if declared, err = d.GitDeclaredPush(ctx, sandbox); err != nil {
			return "", fmt.Errorf("gitproxy: resolving declared push set for %s: %w", sandbox, err)
		}
	}
	cOwner, cRepo := canonicalizeRepo(owner, repo)
	if declared == nil || !canonicalMatches(declared.Declaration.Repo, cOwner, cRepo) {
		refs := make([]string, len(others))
		for i, u := range others {
			refs[i] = u.Ref
		}
		reason := fmt.Sprintf("refusing to update %s in %s/%s: a sandbox may push only its own task's branch, %s "+
			"(push with `git push origin %s`)", strings.Join(refs, ", "), owner, repo, own, branch)
		switch {
		case declared != nil:
			reason += fmt.Sprintf(". This task's template, %s, declares a push set for %s only",
				declared.Declaration.Path, declared.Declaration.Repo)
		default:
			reason += ". A task filed from a template declared in this repository's " + model.TemplateDeclarationDir +
				" pushes other refs of it only where that template lists them under `push:`, on the default branch"
		}
		return reason, nil
	}

	var problems []string
	var grants []RefGrant
	for _, u := range others {
		kind, problem := declaredUpdate(declared.Push, u, check)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		grants = append(grants, RefGrant{
			Ref: u.Ref, Kind: kind, Via: "declared push", TaskID: declared.TaskID,
			TemplateID: declared.TemplateID, Declaration: declared.Declaration.Path,
		})
	}
	if len(problems) > 0 {
		at := ""
		if c := declared.Declaration.Commit; c != "" {
			at = " at " + shortCommit(c)
		}
		return fmt.Sprintf("refusing to update %s/%s: %s. This task may push its own branch, %s, and what "+
			"`push:` in %s declares (as read from %s's default branch%s); a run cannot widen that by editing "+
			"the file on its own branch -- the change has to merge first",
			owner, repo, strings.Join(problems, "; "), own, declared.Declaration.Path, declared.Declaration.Repo, at), nil
	}
	check.grant(grants...)
	return "", nil
}

// configProposalUpdates is others less the updates to the run's own
// grain/config proposal branches (ConfigProposalLookup), which are
// granted -- created, moved, force-moved or deleted, as its own branch
// may be. Only refs strictly under the prefix qualify, and only in
// grain/config: main, and every other task's branches, stay refused.
func (a *ModelAuthorizer) configProposalUpdates(ctx context.Context, sandbox, owner, repo string,
	others []RefUpdate, check *pushCheck) ([]RefUpdate, error) {
	lookup, ok := a.branches().(ConfigProposalLookup)
	if !ok {
		return others, nil
	}
	cOwner, cRepo := canonicalizeRepo(owner, repo)
	if !canonicalMatches(model.ConfigRepo(), cOwner, cRepo) {
		return others, nil
	}
	prefix, err := lookup.GitConfigProposalPrefix(ctx, sandbox)
	if err != nil {
		return nil, fmt.Errorf("gitproxy: resolving config proposal branches for %s: %w", sandbox, err)
	}
	if prefix == "" {
		return others, nil
	}
	refPrefix := "refs/heads/" + prefix
	var rest []RefUpdate
	for _, u := range others {
		if strings.HasPrefix(u.Ref, refPrefix) && len(u.Ref) > len(refPrefix) {
			check.grant(RefGrant{Ref: u.Ref, Kind: updateKind(u), Via: "config proposal"})
			continue
		}
		rest = append(rest, u)
	}
	return rest, nil
}

// refuseBorrowedBranchRewrite is the one bound on a run's own branch: a
// run whose branch is another task's (FastForwardOnlyLookup -- a
// security review working on the change it reviews) may only move it
// forward. Deleting it would close that task's pull request, and creating
// it would revive one that is gone; a force-push would replace the
// commits under review with ones nobody reviewed, which the review would
// then clear as its own fixes. A lookup that does not implement
// FastForwardOnlyLookup is read as no run being bound this way.
func (a *ModelAuthorizer) refuseBorrowedBranchRewrite(ctx context.Context, sandbox, own string, updates []RefUpdate) (string, error) {
	lookup, ok := a.branches().(FastForwardOnlyLookup)
	if !ok {
		return "", nil
	}
	var mine []RefUpdate
	for _, u := range updates {
		if u.Ref == own {
			mine = append(mine, u)
		}
	}
	if len(mine) == 0 {
		return "", nil
	}
	bound, err := lookup.GitPushFastForwardOnly(ctx, sandbox)
	if err != nil {
		return "", fmt.Errorf("gitproxy: resolving whether %s may only fast-forward its branch: %w", sandbox, err)
	}
	if !bound {
		return "", nil
	}
	check := pushCheckFrom(ctx)
	for _, u := range mine {
		switch updateKind(u) {
		case "delete":
			return fmt.Sprintf("refusing to delete %s: it is the branch of the change this task reviews, "+
				"which this task may only fast-forward", own), nil
		case "create":
			return fmt.Sprintf("refusing to create %s: it is the branch of the change this task reviews, "+
				"which is gone, and this task may only fast-forward it", own), nil
		}
		var why string
		if check != nil {
			var ok bool
			if ok, why = check.fastForward(u.New, u.Old); ok {
				continue
			}
		}
		if why != "" {
			why = " (asking the repository for the commits it already has: " + why + ")"
		}
		return fmt.Sprintf("refusing to update %s: it is the branch of the change this task reviews, which "+
			"this task may only fast-forward, and %s could not be shown to descend from %s%s -- "+
			"`git fetch origin`, rebase onto `origin/%s` and push again rather than force-pushing",
			own, shortCommit(u.New), shortCommit(u.Old), why, strings.TrimPrefix(own, "refs/heads/")), nil
	}
	return "", nil
}

// declaredUpdate checks one update against a declared push set: what
// kind of update it is when the set allows it, or why not.
func declaredUpdate(set model.RefPushSet, u RefUpdate, check *pushCheck) (kind, problem string) {
	matched, force, del := set.Allows(u.Ref)
	if !matched {
		return "", fmt.Sprintf("%s is not declared", u.Ref)
	}
	kind = updateKind(u)
	switch kind {
	case "delete":
		if !del {
			return "", fmt.Sprintf("%s may be pushed but not deleted (its entry does not say `delete: true`)", u.Ref)
		}
		return kind, ""
	case "create":
		return kind, ""
	}
	if force {
		return "force", ""
	}
	if strings.HasPrefix(u.Ref, "refs/tags/") {
		return "", fmt.Sprintf("%s already exists, and moving a tag is a force-push its entry does not "+
			"allow (`force: true`)", u.Ref)
	}
	var why string
	if check != nil {
		var ok bool
		if ok, why = check.fastForward(u.New, u.Old); ok {
			return kind, ""
		}
	}
	if why != "" {
		why = " (asking the repository for the commits it already has: " + why + ")"
	}
	return "", fmt.Sprintf("%s: could not show %s is a fast-forward of %s from the commits this push carries "+
		"or the repository already has%s, and its entry does not allow a force-push (`force: true`)",
		u.Ref, shortCommit(u.New), shortCommit(u.Old), why)
}

func shortCommit(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// maxCommandListBytes bounds how much of a push is read to find its
// command list, and how much of a gzip-encoded push is inflated at all. A
// command is a few hundred bytes at most, so a list that does not end
// inside this is not one git sent.
const maxCommandListBytes = 16 << 20

// decodedPush is a receive-pack request body as the client wrote it:
// body itself, or inflated -- as far as maxCommandListBytes, which is as
// much as the command list can need -- when the client gzip-encoded it.
func decodedPush(body io.Reader, contentEncoding string) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("reading gzip-encoded push: %w", err)
		}
		return io.LimitReader(zr, maxCommandListBytes), nil
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q on a push", contentEncoding)
	}
}

// commandList returns the command list at the start of a receive-pack
// request body, decoded: its pkt-lines up to and including the flush-pkt
// that ends it, read off body and no further, so the packfile behind it
// is never read here. A list that is truncated, malformed or longer than
// maxCommandListBytes comes back as far as it was read, for
// parseReceivePack to refuse.
func commandList(body io.Reader, contentEncoding string) ([]byte, error) {
	r, err := decodedPush(body, contentEncoding)
	if err != nil {
		return nil, err
	}
	var list []byte
	for len(list) < maxCommandListBytes {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return list, endOfList(err)
		}
		list = append(list, head[:]...)
		n, err := strconv.ParseUint(string(head[:]), 16, 16)
		if err != nil || n < 4 {
			// The flush-pkt that ends the list, or a length
			// parseReceivePack refuses.
			return list, nil
		}
		payload := make([]byte, n-4)
		read, err := io.ReadFull(r, payload)
		list = append(list, payload[:read]...)
		if err != nil {
			return list, endOfList(err)
		}
	}
	return list, nil
}

// endOfList is a read error commandList returns: none for a body that
// simply ended, which parseReceivePack refuses as a truncated list.
func endOfList(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return fmt.Errorf("reading push: %w", err)
}

// ParseReceivePackCommands reads the command list at the start of a
// git-receive-pack request body: any shallow lines, then one command per
// pkt-line up to the flush-pkt that ends the list. Anything else -- a
// truncated list, a malformed command, a signed push's certificate,
// which carries its commands inside the certificate -- is an error, and
// the caller refuses the push.
func ParseReceivePackCommands(body []byte) ([]RefUpdate, error) {
	updates, _, err := parseReceivePack(body)
	return updates, err
}

// parseReceivePack is ParseReceivePackCommands that also returns the
// capabilities the client sent after the NUL on its first command --
// which say how it wants a refusal answered (rejectPush).
func parseReceivePack(body []byte) (updates []RefUpdate, capabilities []string, err error) {
	for rest := body; ; {
		if len(rest) < 4 {
			return nil, nil, errors.New("push ended before its command list did")
		}
		n, err := strconv.ParseUint(string(rest[:4]), 16, 16)
		if err != nil {
			return nil, nil, fmt.Errorf("malformed pkt-line length %q in push", rest[:4])
		}
		if n == 0 {
			return updates, capabilities, nil
		}
		if n < 4 || int(n) > len(rest) {
			return nil, nil, fmt.Errorf("malformed pkt-line length %d in push", n)
		}
		line := string(rest[4:n])
		rest = rest[n:]
		if i := strings.IndexByte(line, 0); i >= 0 {
			if len(updates) == 0 {
				capabilities = strings.Fields(strings.TrimSuffix(line[i+1:], "\n"))
			}
			line = line[:i]
		}
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "shallow ") && len(updates) == 0 {
			continue
		}
		if strings.HasPrefix(line, "push-cert") {
			return nil, nil, errors.New("signed pushes are not supported through grain's git proxy; push without --signed")
		}
		fields := strings.Split(line, " ")
		if len(fields) != 3 || !isObjectID(fields[0]) || !isObjectID(fields[1]) || fields[2] == "" {
			return nil, nil, fmt.Errorf("malformed ref-update command %q in push", line)
		}
		updates = append(updates, RefUpdate{Old: fields[0], New: fields[1], Ref: fields[2]})
	}
}

// isObjectID is a SHA-1 or SHA-256 object name in hex.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// rejectPush answers a push whose command list was read but refused the
// way receive-pack itself refuses an update: a 200 carrying report-status,
// "ng <ref> <reason>" for every command, so `git push` prints
//
//	! [remote rejected] main -> main (<reason>)
//
// and the whole reason as "remote:" lines, rather than the bare "HTTP
// 403" it prints for any refused POST without ever showing its body.
// Nothing was unpacked and no ref moved: the packfile never left this
// proxy. A client that did not ask for report-status cannot read that
// answer, so it gets the plain-text 403 instead (false).
func rejectPush(updates []RefUpdate, capabilities []string, reason string) (ProxyResponse, bool) {
	var reportStatus, sideband bool
	for _, c := range capabilities {
		switch c {
		case "report-status", "report-status-v2":
			reportStatus = true
		case "side-band", "side-band-64k":
			sideband = true
		}
	}
	if !reportStatus || len(updates) == 0 {
		return ProxyResponse{}, false
	}
	// An ng reason is one line of a pkt-line; the full text goes out on
	// the progress band below, where the client prints every line.
	short := strings.Join(strings.Fields(reason), " ")
	if len(short) > 200 {
		short = short[:200] + "..."
	}
	var report bytes.Buffer
	writePkt(&report, "unpack ok\n")
	for _, u := range updates {
		writePkt(&report, fmt.Sprintf("ng %s %s\n", u.Ref, short))
	}
	report.WriteString("0000")

	var body bytes.Buffer
	if sideband {
		// Band 2 is progress, printed as "remote: <line>"; band 1 carries
		// the report itself. Each band packet is well under the 1000
		// bytes plain side-band allows.
		for _, line := range strings.Split(strings.TrimSpace(reason), "\n") {
			for len(line) > 900 {
				writePkt(&body, "\x02"+line[:900])
				line = line[900:]
			}
			writePkt(&body, "\x02"+line+"\n")
		}
		for data := report.Bytes(); len(data) > 0; {
			n := min(len(data), 900)
			writePkt(&body, "\x01"+string(data[:n]))
			data = data[n:]
		}
		body.WriteString("0000")
	} else {
		body.Write(report.Bytes())
	}
	return ProxyResponse{
		Status: 200,
		Headers: map[string]string{
			"Content-Type":  "application/x-git-receive-pack-result",
			"Cache-Control": "no-cache",
		},
		Body: body.Bytes(),
	}, true
}

// writePkt appends payload to b as one pkt-line.
func writePkt(b *bytes.Buffer, payload string) {
	fmt.Fprintf(b, "%04x%s", len(payload)+4, payload)
}
