package antigravity

import (
	"fmt"
	"slices"
	"sort"
	"sync"
)

// HostToolGrant is a capability grant that admits a fixed, read-only
// handful of the agy host's own native tools -- tools every other run is
// denied by HookDecisionFor -- for a run holding it.
//
// The calls it admits run on the host rather than in the sandbox, as
// whatever identity the agy process there holds, so the pre-tool hook
// records each one before letting it through (HostToolCall), and a run
// is told in its preamble what it may call and what it runs as. A
// package outside core registers one from init()
// (RegisterHostToolGrant); core antigravity knows none by name.
type HostToolGrant struct {
	// Capability is the grant name a run must hold
	// (agent.RunConfig.Grants), which the hook's own command carries.
	Capability string
	// Tools is the whole allowlist, spelled as agy names its native
	// tools in a pre-tool hook payload. Nothing a task, a template or a
	// hook argument says can add to it.
	Tools []string
	// Preamble is what toolPreamble adds, after it has finished saying
	// why the built-ins are unavailable, for a run holding the grant:
	// the tools it admits, what they run as, and what to say when they
	// fail. It should start with a space.
	Preamble string
	// HostIdentity is what the admitted calls run as, for the line the
	// run's transcript gets ("as that host's <HostIdentity>").
	HostIdentity string
	// IsAuthFailure reports whether an admitted call's error text reads
	// as the host's credential having lapsed, and AuthHint is the line
	// an operator is shown when one did, naming host. Either may be nil.
	IsAuthFailure func(text string) bool
	AuthHint      func(host string) string
}

var (
	hostToolMu     sync.RWMutex
	hostToolGrants = map[string]HostToolGrant{}
)

// RegisterHostToolGrant makes g's tools admissible to a run holding
// g.Capability until the returned func is called. It panics on a grant
// with no Capability or no Tools, or one already registered.
func RegisterHostToolGrant(g HostToolGrant) (unregister func()) {
	if g.Capability == "" || len(g.Tools) == 0 {
		panic(fmt.Sprintf("antigravity: RegisterHostToolGrant(%q): a grant needs a capability and tools", g.Capability))
	}
	g.Tools = slices.Clone(g.Tools)
	hostToolMu.Lock()
	defer hostToolMu.Unlock()
	if _, ok := hostToolGrants[g.Capability]; ok {
		panic(fmt.Sprintf("antigravity: RegisterHostToolGrant(%q): already registered", g.Capability))
	}
	hostToolGrants[g.Capability] = g
	return func() {
		hostToolMu.Lock()
		defer hostToolMu.Unlock()
		delete(hostToolGrants, g.Capability)
	}
}

// registeredHostToolGrants is every registered grant, ordered by
// Capability so a run's hook command reads the same way twice.
func registeredHostToolGrants() []HostToolGrant {
	hostToolMu.RLock()
	defer hostToolMu.RUnlock()
	out := make([]HostToolGrant, 0, len(hostToolGrants))
	for _, g := range hostToolGrants {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out
}

// heldHostToolGrants is the registered grants among grants.
func heldHostToolGrants(grants []string) []HostToolGrant {
	var held []HostToolGrant
	for _, g := range registeredHostToolGrants() {
		if slices.Contains(grants, g.Capability) {
			held = append(held, g)
		}
	}
	return held
}

// hostToolGrantFor is the registered grant whose allowlist names tool.
func hostToolGrantFor(tool string) (HostToolGrant, bool) {
	for _, g := range registeredHostToolGrants() {
		if slices.Contains(g.Tools, tool) {
			return g, true
		}
	}
	return HostToolGrant{}, false
}
