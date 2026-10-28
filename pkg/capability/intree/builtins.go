package intree

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

// CanonicalIDPrefix is the namespace of the built-in servers' canonical
// capability_ids: https://grain.dev/capabilities/<name>. It is grain's,
// the same on every deployment, so a grant naming
// https://grain.dev/capabilities/gcp#gcp-key carries across deployments,
// templates and repos. Like any capability_id it is a name only: nothing
// ever fetches it (docs/capabilities-spec.md §3.2).
const CanonicalIDPrefix = "https://grain.dev/capabilities/"

// CanonicalID is the canonical capability_id of the in-tree server called
// name.
func CanonicalID(name string) string { return CanonicalIDPrefix + name }

// retiredBuiltins names built-in capability servers that EnsureBuiltins
// used to register on every deployment and has since retired. An
// unconfigured/default entry for one is dropped on upgrade so it no
// longer lingers as "needs configuring" in Settings; an entry an
// operator configured or aliased is left in place as an external server
// (Builtin cleared).
var retiredBuiltins = []string{"self-debug"}

// BuiltinCommand is the stdio command a built-in entry starts in the
// daemon's own environment: the grain binary, found on PATH, as
// `grain capability <name>`. A built-in set to run: host names the hostd
// allowlist entry called after the server instead (HostEntry).
const BuiltinCommand = "grain"

// HostEntry is the grain hostd allowlist capability entry a built-in set
// to run: host names: grain-<name>, the entry scripts/setup.sh writes as
// `<grain> capability <name>` for every server marked Host. The prefix
// keeps it apart from an operator's own entry of the server's name.
func HostEntry(name string) string { return "grain-" + name }

// Builtin is one in-tree server as it is registered on a deployment.
type Builtin struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Summary     string `json:"summary"`
	NeedsConfig bool   `json:"needsConfig,omitempty"`
	Host        bool   `json:"host,omitempty"`
	// HostOnly is Builder.HostOnly: the built-in's entry is run: host.
	HostOnly bool `json:"hostOnly,omitempty"`
}

// Builtins lists the in-tree servers registered on every deployment --
// every one in the table but the internal ones -- sorted by name.
func Builtins() []Builtin {
	var out []Builtin
	for _, name := range Names() {
		b := builders[name]
		if b.Internal {
			continue
		}
		out = append(out, Builtin{Name: b.Name, ID: b.ID, Summary: b.Summary, NeedsConfig: b.NeedsConfig, Host: b.Host, HostOnly: b.HostOnly})
	}
	return out
}

// BuiltinNamed is the built-in called name.
func BuiltinNamed(name string) (Builtin, bool) {
	for _, b := range Builtins() {
		if b.Name == name {
			return b, true
		}
	}
	return Builtin{}, false
}

// BuiltinEntry is the entry a deployment with no entry for b is given:
// enabled, running in the daemon's own environment, with no
// configuration. Registering it grants nothing to any task: a grant still
// has to name it. A HostOnly built-in's entry is run: host instead,
// naming its hostd allowlist entry with its HostArgs.
func BuiltinEntry(b Builtin) protocol.ServerConfig {
	if b.HostOnly {
		args := append([]string(nil), builders[b.Name].HostArgs...)
		return protocol.ServerConfig{Command: HostEntry(b.Name), Run: protocol.RunHost, Args: args, Builtin: b.Name}
	}
	args := append([]string{"capability", b.Name}, builders[b.Name].Args...)
	return protocol.ServerConfig{Command: BuiltinCommand, Args: args, Builtin: b.Name}
}

// InTreeName is the in-tree server an entry starts, or "": one whose
// command is the grain binary run as `grain capability <name>`, or a
// run: host entry naming that server's hostd allowlist entry.
func InTreeName(cfg protocol.ServerConfig) string {
	if !cfg.Stdio() {
		return ""
	}
	if cfg.OnHost() {
		if b, ok := builders[strings.TrimPrefix(cfg.Command, "grain-")]; ok && !b.Internal && cfg.Command == HostEntry(b.Name) {
			return b.Name
		}
		return ""
	}
	if filepath.Base(cfg.Command) != BuiltinCommand || len(cfg.Args) < 2 || cfg.Args[0] != "capability" {
		return ""
	}
	if cfg.Args[1] == "skill" {
		return "config-map"
	}
	if b, ok := builders[cfg.Args[1]]; ok && !b.Internal {
		return b.Name
	}
	return ""
}

// migrateLegacySkill upgrades a legacy built-in "skill" server entry in out to
// "config-map" while preserving its configuration, flags, and an alias from
// https://grain.dev/capabilities/skill.
func migrateLegacySkill(out map[string]protocol.ServerConfig) {
	const legacyID = CanonicalIDPrefix + "skill"
	for id, cfg := range out {
		if id != legacyID && cfg.Builtin != "skill" {
			continue
		}
		if filepath.Base(cfg.Command) == BuiltinCommand && len(cfg.Args) >= 2 && cfg.Args[0] == "capability" && cfg.Args[1] == "skill" {
			args := append([]string(nil), cfg.Args...)
			args[1] = "config-map"
			cfg.Args = args
		}
		cfg.Builtin = "config-map"
		out[id] = cfg
	}
}

// EnsureBuiltins is servers with an entry for every built-in, reporting
// whether that changed anything. It is what the daemon applies to
// grain_config.mcp_servers on start and on every reload, and it is
// idempotent:
//
//   - a built-in with no entry is given BuiltinEntry under its canonical
//     id;
//   - an operator's entry that runs `grain capability <name>` under an id
//     of its own is adopted: moved to the canonical id, marked builtin,
//     its old id kept as an alias so grants naming it still resolve, and
//     a one-time Notice saying so;
//   - several entries for one built-in with the same settings are merged
//     into one, the others' ids becoming aliases too; one whose settings
//     differ from the adopted entry's is left as the operator's own
//     instance, so no grant or policy rule naming it reaches settings it
//     did not have;
//   - adopting happens only while the built-in is not registered yet: an
//     entry added later that runs the same server, under an id of its
//     own, is an operator's second instance and is left as it is;
//   - an entry's own settings -- disabled, run, env, config -- are never
//     changed, so a disabled built-in stays disabled.
func EnsureBuiltins(servers map[string]protocol.ServerConfig) (map[string]protocol.ServerConfig, bool) {
	out := make(map[string]protocol.ServerConfig, len(servers)+len(builders))
	for id, cfg := range servers {
		out[id] = cfg
	}
	dropRetiredBuiltins(out)
	migrateLegacySkill(out)
	for _, b := range Builtins() {
		// Adoption is a one-time migration: once the built-in is
		// registered, another entry running the same server is an
		// operator's second instance of it, and is left alone.
		registered := false
		for id, cfg := range out {
			if id == b.ID || cfg.Builtin == b.Name {
				registered = true
			}
		}
		var ids []string
		for id, cfg := range out {
			if id == b.ID || cfg.Builtin == b.Name || (!registered && cfg.Builtin == "" && InTreeName(cfg) == b.Name) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			out[b.ID] = BuiltinEntry(b)
			continue
		}
		winner := pickBuiltinEntry(b.ID, ids, out)
		// Only entries with the winner's own settings are merged into
		// it. Merging one configured or enabled differently would hand
		// every grant naming it -- and every policy rule allowing it --
		// the winner's settings instead: a disabled entry's grants would
		// start minting, and a narrow set's holders would get a broad
		// one's. Those are left as the operator's own instances.
		same := ids[:0]
		for _, id := range ids {
			if id == winner || sameBuiltinSettings(out[id], out[winner]) {
				same = append(same, id)
				continue
			}
			if cfg := out[id]; cfg.Builtin != "" {
				cfg.Builtin = ""
				out[id] = cfg
			}
		}
		ids = same
		merged := out[winner]
		merged.Builtin = b.Name
		aliases := map[string]bool{}
		for _, a := range merged.Aliases {
			aliases[a] = true
		}
		var adopted []string
		for _, id := range ids {
			for _, a := range out[id].Aliases {
				aliases[a] = true
			}
			if id != b.ID {
				aliases[id] = true
				adopted = append(adopted, id)
			}
			if id != winner {
				delete(out, id)
			}
		}
		delete(aliases, b.ID)
		merged.Aliases = nil
		for a := range aliases {
			merged.Aliases = append(merged.Aliases, a)
		}
		sort.Strings(merged.Aliases)
		if len(adopted) > 0 {
			sort.Strings(adopted)
			list := strings.Join(adopted, ", ")
			merged.Notice = fmt.Sprintf("grain now registers the %s server itself, under %s: the hand-added %s %s %s adopted as it. "+
				"Grants naming %s still resolve to this entry; new grants should name %s.",
				b.Name, b.ID, plural(len(adopted), "entry", "entries"), list, plural(len(adopted), "was", "were"), list, b.ID)
		}
		if winner != b.ID {
			delete(out, winner)
		}
		out[b.ID] = merged
	}
	return out, !reflect.DeepEqual(normalizeServers(servers), normalizeServers(out))
}

// pickBuiltinEntry chooses which of a built-in's entries the merged one
// keeps the settings of: the one already at the canonical id, otherwise
// the first configured, enabled one, otherwise the first by id.
func pickBuiltinEntry(canonical string, ids []string, servers map[string]protocol.ServerConfig) string {
	sort.Strings(ids)
	for _, id := range ids {
		if id == canonical {
			return id
		}
	}
	rank := func(cfg protocol.ServerConfig) int {
		r := 0
		if len(cfg.Config) == 0 {
			r += 2
		}
		if cfg.Disabled {
			r++
		}
		return r
	}
	best := ids[0]
	for _, id := range ids[1:] {
		if rank(servers[id]) < rank(servers[best]) {
			best = id
		}
	}
	return best
}

// sameBuiltinSettings reports whether two entries for one built-in
// differ only in what adopting them rewrites: their id, aliases and
// notice.
func sameBuiltinSettings(a, b protocol.ServerConfig) bool {
	a.Builtin, a.Aliases, a.Notice = "", nil, ""
	b.Builtin, b.Aliases, b.Notice = "", nil, ""
	return reflect.DeepEqual(a, b)
}

func sameRetiredBuiltinSettings(cfg, def protocol.ServerConfig) bool {
	if _, ok := cfg.Config[protocol.DataPlaneTokenConfig]; len(cfg.Config) == 0 || (len(cfg.Config) == 1 && ok) {
		cfg.Config = nil
	}
	return sameBuiltinSettings(cfg, def)
}

func dropRetiredBuiltins(out map[string]protocol.ServerConfig) {
	for _, name := range retiredBuiltins {
		id := CanonicalID(name)
		def := BuiltinEntry(Builtin{Name: name})
		for k, cfg := range out {
			if k != id && cfg.Builtin != name {
				continue
			}
			if len(cfg.Aliases) == 0 && sameRetiredBuiltinSettings(cfg, def) {
				delete(out, k)
				continue
			}
			if cfg.Builtin == name {
				cfg.Builtin = ""
				out[k] = cfg
			}
		}
	}
}

// ResolveAlias is the entry id a capability_id names: id itself when it is
// an entry, otherwise the entry listing it among its aliases.
func ResolveAlias(servers map[string]protocol.ServerConfig, id string) (string, bool) {
	if _, ok := servers[id]; ok {
		return id, true
	}
	for entry, cfg := range servers {
		for _, a := range cfg.Aliases {
			if a == id {
				return entry, true
			}
		}
	}
	if id == CanonicalID("skill") {
		if _, ok := servers[CanonicalID("config-map")]; ok {
			return CanonicalID("config-map"), true
		}
	}
	return "", false
}

// BuiltinConfigSchema returns the operator-facing configure schema for a
// configurable built-in capability server, or nil when name is not one.
func BuiltinConfigSchema(name string) map[string]any {
	var raw map[string]any
	switch name {
	case "config-map":
		raw = configMapConfigSchema
	case "gchat":
		raw = gchatConfigSchema()
	case "gcp":
		raw = gcpConfigSchema()
	case "host-script":
		raw = hostScriptConfigSchema
	default:
		if raw = builders[name].ConfigSchema; raw == nil {
			return nil
		}
	}
	s, err := protocol.NormalizeArgsSchema(raw)
	if err != nil {
		return raw
	}
	return protocol.OperatorSchema(s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func normalizeServers(in map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	if len(in) == 0 {
		return nil
	}
	return in
}
