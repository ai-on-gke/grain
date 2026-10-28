// Package policy is grain's local Policy Enforcement Point: the check a
// workstation's own grain makes of every task before it is given a
// sandbox, against a small policy its owner writes down
// (grain_policy.yml, named by the daemon's -policy flag).
//
// A task carries who asked for it (Task.Creator), who approved it
// (Task.Approval) and what it may use (Task.Grants, Task.NetworkAccess).
// This package only asks whether those claims fit what this machine's
// owner is willing to run: one of the repositories they attached, an
// approval when the request came from somebody else, and nothing their
// policy forbids.
//
// Check is stateless and fails closed. A task it refuses is held before
// any sandbox is built or any code is cloned; a policy file that cannot
// be read or parsed stops the daemon from starting rather than leaving
// it running with no policy at all.
package policy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bwsalmon/grain/pkg/model"
)

// Policy is one workstation's local policy, as grain_policy.yml writes
// it down. The zero Policy allows no repository at all.
type Policy struct {
	// Repos are the repositories tasks on this machine may target, write
	// or read: owner/name (or host/owner/name), compared without regard
	// to case, where either segment may be a path.Match pattern
	// ("acme/*"). Empty refuses every task that names a repository.
	Repos []string `yaml:"repos"`

	// LocalRequesters are the requesters (Task.Creator) this machine
	// treats as its owner: their tasks need no approval beyond the one
	// that queued them. A task with no requester at all is grain's own
	// (a schedule, a suite, the local CLI) and is local too -- but only
	// one derived from no other task (model.Task.DerivedFrom): a
	// proposal, port, review or repair is judged as its root requester's
	// (Requester), never as grain's own for naming nobody.
	LocalRequesters []string `yaml:"local_requesters"`

	// AllowUnapprovedExternal turns the approval check off: by default a
	// task requested by anybody not in LocalRequesters must carry an
	// approval by a human other than its requester.
	AllowUnapprovedExternal bool `yaml:"allow_unapproved_external"`

	// AllowExternalOnHostSandboxes lets a task that is not this
	// machine's own (IsOwn) run on a daemon whose sandboxes are
	// plain directories on this host (-host-sandboxes). By default such a
	// task is refused there (CheckUnisolated): every one of those runs
	// executes as the owner, on the owner's machine, with the owner's
	// files, keys and logins within reach, so a request from anybody
	// else would be a request to act as the owner.
	AllowExternalOnHostSandboxes bool `yaml:"allow_external_on_host_sandboxes"`

	// Approvers, when set, are the only people whose approval of an
	// external request this machine accepts.
	Approvers []string `yaml:"approvers"`

	// Capabilities limits what a task may be granted (Task.Grants).
	Capabilities CapabilityRules `yaml:"capabilities"`

	// Network lists the network accesses (open, none) a task's sandbox
	// may have. Empty allows either.
	Network []model.NetworkAccess `yaml:"network"`
}

// CapabilityRules is Policy.Capabilities. Each entry is a capability
// pattern, compared without regard to case (capabilityMatch): a bare
// family name ("gcp-key") matches the family and every parameterised
// grant of it ("gcp-key:team-a"), "family:*" matches every parameterised
// grant of the family, and otherwise the family and the parameter are
// each path.Match patterns ("repo-write:acme/*").
type CapabilityRules struct {
	// Allow, when set, is every capability a task may be granted; a
	// grant of anything else refuses the task.
	Allow []string `yaml:"allow"`
	// Deny refuses a task granted any of these, whatever Allow says.
	Deny []string `yaml:"deny"`
}

// Load reads and validates the policy file at path. Unknown keys are an
// error, so a misspelt restriction is never silently dropped.
func Load(file string) (*Policy, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("policy: reading %s: %w", file, err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("policy: %s: %w", file, err)
	}
	return p, nil
}

// Parse reads and validates a policy from its YAML form.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	// An empty file is the zero Policy (io.EOF), not an error.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	// A second document would be silently ignored, and with it whatever
	// restrictions it names, so a file holding more than one is refused.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("parsing: a policy file holds one YAML document, and this one holds more")
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Policy) validate() error {
	for _, r := range p.Repos {
		if _, _, err := splitRepoPattern(r); err != nil {
			return err
		}
	}
	for _, n := range p.Network {
		if !model.ValidNetworkAccess(n) {
			return fmt.Errorf("network: %q is not one of %s", n, model.NetworkAccessNames())
		}
	}
	for _, rule := range []struct {
		key  string
		list []string
	}{{"capabilities.allow", p.Capabilities.Allow}, {"capabilities.deny", p.Capabilities.Deny}} {
		for _, c := range rule.list {
			if err := validCapabilityPattern(c); err != nil {
				return fmt.Errorf("%s: %q %w", rule.key, c, err)
			}
		}
	}
	return nil
}

// validCapabilityPattern reports why pattern cannot be a capabilities
// entry, or nil when it can.
func validCapabilityPattern(pattern string) error {
	if model.IsCapabilityRef(pattern) {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("is not a valid pattern: %w", err)
		}
		return nil
	}
	family, param, hasParam := strings.Cut(strings.TrimSpace(pattern), ":")
	if family == "" || (hasParam && param == "") {
		return errors.New("is not a capability or family:parameter")
	}
	if _, err := path.Match(family, ""); err != nil {
		return fmt.Errorf("is not a valid pattern: %w", err)
	}
	if _, err := path.Match(param, ""); err != nil {
		return fmt.Errorf("is not a valid pattern: %w", err)
	}
	return nil
}

// capabilityMatch reports whether the capabilities entry pattern matches
// the granted capability, as CapabilityRules describes. A parameter can
// itself hold a slash (repo-write:owner/name), which path.Match's * does
// not cross, so a parameter pattern of * alone matches any parameter.
func capabilityMatch(pattern, capability string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	capability = strings.ToLower(strings.TrimSpace(capability))
	// A capability reference (https://...#method, with its args after
	// "=") is matched whole, without them: its ":" is the scheme's, not
	// a family's parameter.
	if model.IsCapabilityRef(pattern) || model.IsCapabilityRef(capability) {
		ref := model.CapabilityGrantRef(capability)
		ok, err := path.Match(pattern, ref)
		return pattern == ref || (err == nil && ok)
	}
	pFamily, pParam, pHasParam := strings.Cut(pattern, ":")
	family, param, hasParam := strings.Cut(capability, ":")
	if ok, err := path.Match(pFamily, family); err != nil || !ok {
		return false
	}
	switch {
	case !pHasParam:
		return true
	case !hasParam:
		return false
	case pParam == "*":
		return true
	}
	ok, err := path.Match(pParam, param)
	return err == nil && ok
}

func matchesCapability(patterns []string, capability string) bool {
	for _, pattern := range patterns {
		if capabilityMatch(pattern, capability) {
			return true
		}
	}
	return false
}

// splitRepoPattern splits a repos entry into its host ("" for the
// default host) and its lower-cased owner/name pattern.
func splitRepoPattern(entry string) (host, ownerName string, err error) {
	entry = strings.TrimSpace(entry)
	parts := strings.Split(entry, "/")
	switch len(parts) {
	case 2:
	case 3:
		h, err := model.NormalizeGitHost(parts[0])
		if err != nil {
			return "", "", fmt.Errorf("repos: %q: %w", entry, err)
		}
		host = model.RepoRef{Host: h}.OffDefaultHost()
		parts = parts[1:]
	default:
		return "", "", fmt.Errorf("repos: %q is not owner/name or host/owner/name", entry)
	}
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repos: %q is not owner/name or host/owner/name", entry)
	}
	ownerName = strings.ToLower(parts[0] + "/" + parts[1])
	if _, err := path.Match(ownerName, ""); err != nil {
		return "", "", fmt.Errorf("repos: %q is not a valid pattern: %w", entry, err)
	}
	return host, ownerName, nil
}

// AllowsRepo reports whether repo is one of p.Repos.
func (p *Policy) AllowsRepo(repo model.RepoRef) bool {
	want := strings.ToLower(repo.Owner + "/" + repo.Name)
	for _, entry := range p.Repos {
		host, pattern, err := splitRepoPattern(entry)
		if err != nil || host != repo.OffDefaultHost() {
			continue
		}
		if ok, _ := path.Match(pattern, want); ok {
			return true
		}
	}
	return false
}

// Check is the whole of the enforcement: nil when task may run on this
// machine, and otherwise an error naming the first claim the policy
// refuses. defaultNetwork is the deployment's own network access, which
// a task asking for none of its own gets.
//
// It judges task's requester from task alone (RequesterOf); a caller that
// can resolve the requester at the root of a derived task's chain passes
// it to CheckAs instead.
func (p *Policy) Check(task model.Task, defaultNetwork model.NetworkAccess) error {
	return p.CheckAs(task, RequesterOf(task), defaultNetwork)
}

// CheckAs is Check judging task as requested by requester: its root
// requester, as the caller resolved it.
func (p *Policy) CheckAs(task model.Task, requester Requester, defaultNetwork model.NetworkAccess) error {
	if p == nil {
		return nil
	}
	if err := p.checkRepos(task); err != nil {
		return err
	}
	if err := p.checkApproval(task, requester); err != nil {
		return err
	}
	if err := p.checkCapabilities(task); err != nil {
		return err
	}
	return p.checkNetwork(task, defaultNetwork)
}

// CheckClaims is Check less the approval: nil when task's repositories,
// capabilities and network fit the policy. A task it refuses can never
// run here, whoever approves it.
func (p *Policy) CheckClaims(task model.Task, defaultNetwork model.NetworkAccess) error {
	if p == nil {
		return nil
	}
	if err := p.checkRepos(task); err != nil {
		return err
	}
	if err := p.checkCapabilities(task); err != nil {
		return err
	}
	return p.checkNetwork(task, defaultNetwork)
}

func (p *Policy) checkRepos(task model.Task) error {
	var repos []model.RepoRef
	if task.Target != nil && task.Target.Owner != "" {
		repos = append(repos, *task.Target)
	}
	for _, w := range task.Writes {
		repos = append(repos, w.Repo)
	}
	repos = append(repos, task.Reads...)
	// A grant can reach a repository the task never names: repo-read:
	// and repo-write: grants. Those are repositories this machine would
	// run against too, so they answer to repos alike. (self-debug reaches
	// none: its grain-src/ is the deployment's baked-in source.)
	for _, g := range task.Grants {
		repos = append(repos, model.CapabilityRepos(g.Capability)...)
	}
	for _, r := range repos {
		if !p.AllowsRepo(r) {
			return fmt.Errorf("local policy does not allow repository %s on this machine", r)
		}
	}
	return nil
}

// Requester is who local policy judges a task as asked for by: the
// requester at the root of the chain of tasks it was derived from
// (model.Task.DerivedFrom) -- a proposal, port, review or repair is its
// root requester's work, whatever filed it.
type Requester struct {
	// Name is the root requester: the first Creator met walking from the
	// task up through the tasks it was derived from. "" is grain's own
	// (a schedule, a suite, the local CLI), and only ever after a walk
	// that reached a task derived from nothing.
	Name string
	// Unresolved is set when the chain names a task that cannot be read,
	// so the root requester is not known: such a task is judged as an
	// external request, never as grain's own.
	Unresolved bool
}

// RequesterOf is task's Requester read from task alone, for a caller
// with no store to walk its chain with. A derived task naming no Creator
// of its own is Unresolved: its root is not known from here, and an
// empty requester is grain's own only on a task derived from nothing.
func RequesterOf(task model.Task) Requester {
	r := Requester{Name: strings.TrimSpace(task.Creator)}
	if r.Name == "" && task.DerivedFrom() != "" {
		r.Unresolved = true
	}
	return r
}

// IsLocal reports whether r is this machine's own: grain itself (a root
// requester of "", resolved) or one of LocalRequesters. A requester
// whose chain cannot be resolved is never local.
func (p *Policy) IsLocal(r Requester) bool {
	if r.Unresolved {
		return false
	}
	name := strings.TrimSpace(r.Name)
	return name == "" || containsFold(p.LocalRequesters, name)
}

// IsLocalRequester reports whether requester is one of LocalRequesters.
// An empty requester is not: whether a task naming none is grain's own
// depends on what it was derived from (IsLocal).
func (p *Policy) IsLocalRequester(requester string) bool {
	requester = strings.TrimSpace(requester)
	return requester != "" && containsFold(p.LocalRequesters, requester)
}

// IsOwn reports whether r is this machine's own, for the host-sandbox
// rule (CheckUnisolated): IsLocal, or with a nil Policy -- a daemon with
// no -policy, which has no other notion of whom it belongs to -- every
// requester but one whose chain cannot be resolved.
func (p *Policy) IsOwn(r Requester) bool {
	if p == nil {
		return !r.Unresolved
	}
	return p.IsLocal(r)
}

// CheckUnisolated is the rule for a daemon whose sandboxes isolate
// nothing from this host (-host-sandboxes): nil when a task may run
// there. local is whether the task's root requester is this machine's
// own (IsOwn). Anything else is refused unless
// AllowExternalOnHostSandboxes says otherwise -- including on a daemon
// with no -policy at all, so a nil Policy refuses too.
func (p *Policy) CheckUnisolated(local bool) error {
	if local || (p != nil && p.AllowExternalOnHostSandboxes) {
		return nil
	}
	return errors.New("this machine runs tasks in unisolated host sandboxes (-host-sandboxes), as its owner, " +
		"and this task is not the owner's own (it was requested by somebody outside " +
		"local_requesters); local policy runs only the owner's tasks there unless allow_external_on_host_sandboxes is set")
}

func (p *Policy) checkApproval(task model.Task, requester Requester) error {
	// Work derived from an external request is judged the same way as its
	// root requester's: it needs an approval of its own, since whatever
	// approved its source did not approve it.
	if p.IsLocal(requester) {
		return nil
	}
	who := strings.TrimSpace(requester.Name)
	if who == "" {
		who = strings.TrimSpace(task.Creator)
	}
	if who == "" {
		who = "an unknown requester"
	}
	if derived := task.DerivedFrom(); derived != "" {
		who += " (through task " + derived + ")"
	}
	if p.AllowUnapprovedExternal {
		return nil
	}
	a := task.Approval
	if a == nil || a.Actor.Kind != model.PrincipalHuman || strings.TrimSpace(a.Actor.ID) == "" {
		return fmt.Errorf("task requested by %s carries no human approval, which local policy requires of an external request", who)
	}
	approver := strings.TrimSpace(a.Actor.ID)
	for _, own := range []string{task.Creator, requester.Name} {
		if own = strings.TrimSpace(own); own != "" && strings.EqualFold(approver, own) {
			return fmt.Errorf("task requested by %s is approved only by its own requester, which local policy does not accept", who)
		}
	}
	if len(p.Approvers) > 0 && !containsFold(p.Approvers, approver) {
		return fmt.Errorf("task requested by %s is approved by %s, who is not one of this machine's approvers", who, approver)
	}
	return nil
}

func (p *Policy) checkCapabilities(task model.Task) error {
	for _, g := range task.Grants {
		if matchesCapability(p.Capabilities.Deny, g.Capability) {
			return fmt.Errorf("local policy forbids capability %q", g.Capability)
		}
		if len(p.Capabilities.Allow) > 0 && !matchesCapability(p.Capabilities.Allow, g.Capability) {
			return fmt.Errorf("local policy does not allow capability %q", g.Capability)
		}
	}
	return nil
}

func (p *Policy) checkNetwork(task model.Task, defaultNetwork model.NetworkAccess) error {
	if len(p.Network) == 0 {
		return nil
	}
	access := task.NetworkAccess
	if access == "" {
		access = model.NormalizeNetworkAccess(defaultNetwork)
	}
	for _, n := range p.Network {
		if n == access {
			return nil
		}
	}
	return fmt.Errorf("local policy does not allow network access %q", access)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), s) {
			return true
		}
	}
	return false
}
