package gcpsets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DefaultAccountPrefix is the first part of every service account a set
// creates when the configuration names no account_prefix.
const DefaultAccountPrefix = "grain"

// MaxAccountIDLen and MinAccountIDLen are GCP's bounds on a service
// account id.
const (
	MaxAccountIDLen = 30
	MinAccountIDLen = 6
)

// accountIDPattern is GCP's rule for a service account id.
var accountIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// prefixPattern bounds account_prefix: a letter, then letters and digits,
// no hyphen -- so the prefix always ends at the first hyphen of a name --
// and short enough that a set and a deployment id still fit beside it.
var prefixPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,9}$`)

// deploymentPattern is model.ValidateDeploymentID's rule, the one grain's
// branch names already hold the id to.
var deploymentPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// maxDeploymentLen is model.MaxDeploymentIDLen.
const maxDeploymentLen = 32

// instancePattern is what an instance id may be: grain_instance.id is 32
// hex digits.
var instancePattern = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// instanceTagLen is how much of the instance id the ownership marker
// carries: enough to tell installations apart, short enough to leave
// GCP's 256-character description room for the rest.
const instanceTagLen = 16

// InstanceTag is the part of an instance id the ownership marker holds.
func InstanceTag(instance string) string {
	if len(instance) > instanceTagLen {
		return instance[:instanceTagLen]
	}
	return instance
}

// minSetChars is how much of a set's name a shortened account id keeps
// readable before the deployment id is shortened too.
const minSetChars = 3

// nameHashLen is how many hex digits of a hash stand in for the part of a
// name that was cut.
const nameHashLen = 4

// AccountID is the account id of a set's n'th service account:
// <prefix>-<deployment>-<set>-<n>, e.g. grain-049f1e09-ci-1.
//
// When that is longer than GCP's 30 characters, the set's name is cut
// short first and followed by four hex digits of the hash of project and
// set name, then -- when even the set's first few characters and its hash
// do not fit beside the deployment id -- the deployment id is cut short
// the same way, followed by four hex digits of its own hash. The result
// is deterministic, always a valid account id, and two sets or two
// deployments whose readable parts were cut to the same characters still
// differ in their hashes. The set's hash covers the project as well as
// the name, so the same long set name in two projects gives two
// different-looking ids, and an id read on its own says which project's
// set it is.
//
// Ownership never rests on the name: an account is a set's only when its
// description carries the deployment's marker (OwnerDescription), so a
// name another deployment already took is skipped, not shared.
func AccountID(prefix, deployment, project, set string, n int) string {
	num := strconv.Itoa(n)
	// prefix-D-S-n: three hyphens around what D and S may use.
	avail := MaxAccountIDLen - len(prefix) - len(num) - 3
	if len(deployment)+len(set) <= avail {
		return prefix + "-" + deployment + "-" + set + "-" + num
	}
	keep := min(len(set), minSetChars) + 1 + nameHashLen
	s := shorten(set, nameHash(project+"/"+set), max(avail-len(deployment), keep))
	d := shorten(deployment, nameHash(deployment), avail-len(s))
	return prefix + "-" + d + "-" + s + "-" + num
}

// shorten is s when it fits in max, otherwise as much of s as fits with
// "-" and h after it.
func shorten(s, h string, max int) string {
	if len(s) <= max {
		return s
	}
	k := max - len(h) - 1
	if k < 1 {
		return h[:min(len(h), max)]
	}
	return strings.TrimRight(s[:k], "-") + "-" + h
}

func nameHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:nameHashLen]
}

// LegacyAccountID is the name a set's n'th account had before account
// names carried the deployment: grain-<set>-<n>, the set cut short with
// four hex digits of its name's hash when that was too long. Accounts
// named so are adopted only by a set with adopt_legacy.
func LegacyAccountID(set string, n int) string {
	id := fmt.Sprintf("grain-%s-%d", set, n)
	if len(id) <= MaxAccountIDLen {
		return id
	}
	sum := sha256.Sum256([]byte(set))
	h := hex.EncodeToString(sum[:2])
	suffix := "-" + h + "-" + strconv.Itoa(n)
	keep := MaxAccountIDLen - len("grain-") - len(suffix)
	return "grain-" + strings.TrimRight(set[:keep], "-") + suffix
}

// ValidAccountID reports whether id satisfies GCP's rules for a service
// account id: 6-30 characters, a lowercase letter first, lowercase
// letters, digits and hyphens after it, and no hyphen last.
func ValidAccountID(id string) bool { return accountIDPattern.MatchString(id) }

// Email is a service account's email from its id and project.
func Email(accountID, project string) string {
	return accountID + "@" + project + ".iam.gserviceaccount.com"
}

// ownerTag starts the marker in the description of every service account
// this capability created; nothing without it is ever changed or deleted.
const ownerTag = "grain-gcp-key "

var (
	ownerPattern  = regexp.MustCompile(`\[grain-gcp-key deployment=([a-z0-9-]+) instance=([a-z0-9]*) set=([a-z][a-z0-9-]*)\]`)
	legacyPattern = regexp.MustCompile(`\[grain-gcp-key set=([a-z][a-z0-9-]*)\]`)
)

// Owner is what a service account's ownership marker says: which
// deployment and installation created it, for which set. A Legacy owner
// is the marker written before deployments were recorded, which names
// only the set.
type Owner struct {
	Deployment string
	Instance   string
	Set        string
	Legacy     bool
}

// OwnerDescription is the description a set's service accounts are
// created with: readable, and carrying the ownership marker
// [grain-gcp-key deployment=<deployment> instance=<instance tag> set=<set>].
// At its longest it is within GCP's 256 characters.
func OwnerDescription(deployment, instance, set string) string {
	return "Managed by grain gcp-key for set " + set + " of deployment " + deployment + "; do not edit. [" +
		ownerTag + "deployment=" + deployment + " instance=" + InstanceTag(instance) + " set=" + set + "]"
}

// LegacyOwnerDescription is the description accounts were created with
// before the marker named the deployment.
func LegacyOwnerDescription(set string) string {
	return "Created and managed by grain's gcp-key capability for permission set " + set + "; do not edit. [" + ownerTag + "set=" + set + "]"
}

// OwnedBy is who a service account's description says created it.
func OwnedBy(description string) (Owner, bool) {
	if m := ownerPattern.FindStringSubmatch(description); m != nil {
		return Owner{Deployment: m[1], Instance: m[2], Set: m[3]}, true
	}
	if m := legacyPattern.FindStringSubmatch(description); m != nil {
		return Owner{Set: m[1], Legacy: true}, true
	}
	return Owner{}, false
}

// Ours reports whether o is this deployment's marker. The installation
// decides it when both sides know theirs, so a deployment id renamed in
// Settings keeps its accounts and two deployments an admin gave the same
// id do not share theirs; the deployment id decides it only when the
// configuration carries no instance id. A legacy marker is nobody's in
// particular: it is a set's only through adopt_legacy (Adopts).
func (c *Config) Ours(o Owner) bool {
	if o.Legacy {
		return false
	}
	if c.Instance != "" && o.Instance != "" {
		return o.Instance == InstanceTag(c.Instance)
	}
	return o.Deployment == c.Deployment
}

// accountNumber is the n at the end of an account's id, or 0.
func accountNumber(email string) int {
	id, _, _ := strings.Cut(email, "@")
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}
