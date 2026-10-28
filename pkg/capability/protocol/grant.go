package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// GrantSpec is one entry of a capabilities: list as written (§2.2.1):
// either a bare reference string or {capability, with}.
type GrantSpec struct {
	Capability string         `json:"capability" yaml:"capability"`
	With       map[string]any `json:"with,omitempty" yaml:"with,omitempty"`
	// bare records that the entry was written as a bare string, which is
	// only valid for a method whose args need nothing (§2.2.1 item 1).
	bare bool
}

// IsBare reports whether the entry was written as a bare string.
func (g GrantSpec) IsBare() bool { return g.bare }

// BareGrant is the GrantSpec a bare reference string declares.
func BareGrant(ref string) GrantSpec { return GrantSpec{Capability: ref, bare: true} }

// ParseGrant decodes one capabilities: entry from its JSON form: a string,
// or an object carrying capability and optionally with and nothing else.
func ParseGrant(raw json.RawMessage) (GrantSpec, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return BareGrant(s), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return GrantSpec{}, fmt.Errorf("capability grant must be a reference string or a {capability, with} object")
	}
	var g GrantSpec
	for k, v := range obj {
		switch k {
		case "capability":
			if err := json.Unmarshal(v, &g.Capability); err != nil {
				return GrantSpec{}, fmt.Errorf("capability grant: capability must be a string")
			}
		case "with":
			if err := json.Unmarshal(v, &g.With); err != nil {
				return GrantSpec{}, fmt.Errorf("capability grant: with must be an object")
			}
		default:
			return GrantSpec{}, fmt.Errorf("capability grant: unknown key %q (only capability and with are allowed)", k)
		}
	}
	if g.Capability == "" {
		return GrantSpec{}, fmt.Errorf("capability grant: capability is required")
	}
	return g, nil
}

// ParseCLIGrant parses the grain CLI's -capability value (§2.2.3): a bare
// reference, or a reference followed by = and an inline JSON object.
func ParseCLIGrant(s string) (GrantSpec, error) {
	ref, with, hasWith := strings.Cut(s, "=")
	if !hasWith {
		return BareGrant(s), nil
	}
	g := GrantSpec{Capability: ref}
	if err := json.Unmarshal([]byte(with), &g.With); err != nil || g.With == nil {
		return GrantSpec{}, fmt.Errorf("-capability %s: the value after = must be a JSON object", ref)
	}
	return g, nil
}

// Grant is a canonical grant (§2.3.1): its resolved reference and its
// normalized args, recorded at admission.
type Grant struct {
	Ref  Ref
	Args map[string]any
	// Releasable is copied from the method at admission, so that releasing
	// a grant needs nothing but the grant.
	Releasable bool
}

// Equal is grant equality (§2.3.2): identical canonical references and
// deep-equal normalized args.
func (g Grant) Equal(o Grant) bool {
	return g.Ref == o.Ref && reflect.DeepEqual(g.Args, o.Args)
}

// Lookup finds the attached, enabled server for a capability_id, or
// reports that the reference is unmapped or disabled (§3.2.1).
type Lookup func(capabilityID string) (*Server, error)

// Normalize resolves and validates a run's grants and deduplicates them,
// first occurrence first (§2.3.1-§2.3.2). The index of a grant in the
// result is what its attempt_id is formed from (§2.3.3, AttemptID).
func Normalize(specs []GrantSpec, lookup Lookup) ([]Grant, error) {
	var out []Grant
	for _, spec := range specs {
		g, err := normalizeOne(spec, lookup)
		if err != nil {
			return nil, err
		}
		dup := false
		for _, have := range out {
			if have.Equal(g) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, g)
		}
	}
	return out, nil
}

func normalizeOne(spec GrantSpec, lookup Lookup) (Grant, error) {
	ref, err := ParseRef(spec.Capability)
	if err != nil {
		return Grant{}, err
	}
	srv, err := lookup(ref.CapabilityID)
	if err != nil {
		return Grant{}, err
	}
	if srv.ID != "" && ref.CapabilityID != srv.ID {
		if ref.Method == "" {
			if seg := lastSegment(ref.CapabilityID); srv.Methods[seg] != nil {
				ref.Method = seg
			}
		}
		ref.CapabilityID = srv.ID
	}
	ref, err = srv.Resolve(ref)
	if err != nil {
		return Grant{}, err
	}
	m := srv.Methods[ref.Method]
	args, err := ValidateArgs(m.ArgsSchema, spec.With)
	if err != nil {
		if spec.bare {
			return Grant{}, fmt.Errorf("capability %s needs arguments, so it must be granted as {capability, with}: %v", ref, err)
		}
		return Grant{}, fmt.Errorf("capability %s: %v", ref, err)
	}
	return Grant{Ref: ref, Args: args, Releasable: m.Releasable}, nil
}

// personGranted is the methods only a person may grant
// (RequirePersonGrant), by canonical capability_id#method.
var personGranted sync.Map

// RequirePersonGrant marks capabilityID#method as a grant only a person
// may give: no parent's grant ever covers it (Covers), so a proposal or
// any other agent-filed task asking for it waits for a person even when
// whoever filed it holds the very same grant. a server's submit method is one: a
// server linked into the build calls this for its own methods
// (intree.Builder.PersonGranted).
func RequirePersonGrant(capabilityID, method string) {
	personGranted.Store(capabilityID+"#"+method, true)
}

// PersonGranted reports whether ref names a method only a person may
// grant (RequirePersonGrant).
func PersonGranted(ref Ref) bool {
	_, ok := personGranted.Load(ref.CapabilityID + "#" + ref.Method)
	return ok
}

// GrantKind is who may give one grant of a method, judged from its args
// (SetGrantPolicy).
type GrantKind int

const (
	// GrantCovered is the default: a grant an agent may pass on only when
	// its own task holds the identical one (Covers).
	GrantCovered GrantKind = iota
	// GrantByAgent is a grant any agent may give, held or not -- one
	// scoped to the task it is granted to, as a git branch is: a workspace server's
	// own task workspace.
	GrantByAgent
	// GrantByPerson is a grant only a person may give, like a method
	// RequirePersonGrant marks, but for these args alone: a workspace method
	// naming a workspace other than the task's own.
	GrantByPerson
)

// GrantPolicy says which GrantKind a grant with args is.
type GrantPolicy func(args map[string]any) GrantKind

// grantPolicies are the methods whose grants are judged by their args
// (SetGrantPolicy), by canonical capability_id#method.
var grantPolicies sync.Map

// SetGrantPolicy makes who may grant capabilityID#method depend on the
// grant's args: a server linked into the build calls it for its own
// methods (intree.Builder.GrantPolicies). A method RequirePersonGrant
// marks stays a person's whatever its policy says.
func SetGrantPolicy(capabilityID, method string, policy GrantPolicy) {
	grantPolicies.Store(capabilityID+"#"+method, policy)
}

// GrantKindOf is who may give g: GrantByPerson for a method only a
// person may grant (PersonGranted), else what its policy says of its
// args, else GrantCovered.
func GrantKindOf(g Grant) GrantKind {
	if PersonGranted(g.Ref) {
		return GrantByPerson
	}
	if p, ok := grantPolicies.Load(g.Ref.CapabilityID + "#" + g.Ref.Method); ok {
		return p.(GrantPolicy)(g.Args)
	}
	return GrantCovered
}

// Covers reports whether a child grant is covered by its parent's recorded
// grants (§2.3.4). Anything uncovered must not inherit auto-approval. A
// grant only a person may give (GrantKindOf: a method PersonGranted
// marks, or args its policy reserves) is never covered, and one any agent
// may give (GrantByAgent) always is.
func Covers(parent []Grant, child Grant) bool {
	switch GrantKindOf(child) {
	case GrantByPerson:
		return false
	case GrantByAgent:
		return true
	}
	for _, p := range parent {
		if p.Equal(child) {
			return true
		}
	}
	return false
}

// Uncovered lists, in canonical-reference order, the child grants none of
// the parent's grants cover.
func Uncovered(parent, child []Grant) []Grant {
	var out []Grant
	for _, c := range child {
		if !Covers(parent, c) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out
}

// AttemptID forms the attempt_id of the grant at index within one attempt
// of a task (§1.2, §2.3.3): <deployment>-<task>-<attempt>-<index>.
func AttemptID(deployment, task string, attempt, index int) (string, error) {
	id := fmt.Sprintf("%s-%s-%d-%d", deployment, task, attempt, index)
	if !ValidAttemptID(id) {
		return "", fmt.Errorf("attempt id %q does not match %s", id, attemptIDPattern)
	}
	return id, nil
}
