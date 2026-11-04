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

package localscripts

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/localexec"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The local-scripts capability is retiring onto the host-script
// capability server (docs/local-scripts.md, "Moving to host-script";
// design/design/unified-grants.design.md §4): each script of a
// declaration becomes one entry of a host-script server's scripts, and a
// local-scripts:<name> grant becomes one
// <server>#host-script={"script": ...} grant per script. This file is
// that conversion. It reads a declaration exactly as the run's
// mcpserver does (localexec.Load, Declaration.Snapshot) and writes
// host-script configuration that runs the same file, with the same
// arguments, working directory, timeout, output cap and variables.

// HostScriptServerID is the capability server a declaration's scripts
// are configured on when the deployment has no host-script server of
// its own running in the daemon's environment (HostScriptTarget).
const HostScriptServerID = "https://grain.dev/capabilities/host-script"

// HostScriptMethod is host-script's one grantable method.
const HostScriptMethod = "host-script"

// hostScriptNamePattern is host-script's own script-name rule
// (pkg/capability/intree hostScriptName), restated because that package
// sits above this one.
var hostScriptNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// hostScriptDescriptionMax is host-script's limit on a description.
const hostScriptDescriptionMax = 1024

// secretBindingPrefix starts the host-script server's own environment
// variable a migrated script's secret is bound to: a script's env entry
// VAR: <secret> becomes VAR: $env:GRAIN_LOCAL_SCRIPTS_<SECRET>, and the
// server's mcpServers env binds that to $secret:<secret>.
const secretBindingPrefix = "GRAIN_LOCAL_SCRIPTS_"

// HostScripts is one declaration as host-script configuration.
type HostScripts struct {
	// Declaration is the declaration's name.
	Declaration string
	// Scripts are host-script scripts entries, one per script the
	// declaration serves, in the order it serves them.
	Scripts []map[string]any
	// Env is what the server's mcpServers env must bind for them: a
	// variable of its own to "$secret:<name>".
	Env map[string]string
}

// Names is the host-script script name of each of h's scripts.
func (h HostScripts) Names() []string {
	out := make([]string, 0, len(h.Scripts))
	for _, s := range h.Scripts {
		name, _ := s["name"].(string)
		out = append(out, name)
	}
	return out
}

// HostScriptName is the host-script script the declaration decl's
// script named file becomes: decl, a dash, and its local-scripts tool
// name without the local_ prefix -- local-scripts:deploy's
// tools/preview.sh, served as local_preview_sh, becomes deploy-preview_sh.
func HostScriptName(decl, file string) (string, error) {
	name := decl + "-" + strings.TrimPrefix(localexec.ToolName(file), "local_")
	if !hostScriptNamePattern.MatchString(name) {
		return "", fmt.Errorf("local-scripts %s: script %s would be host-script %q, which is not a host-script name (%s)",
			decl, file, name, hostScriptNamePattern)
	}
	return name, nil
}

// ConvertToHostScripts reads the declaration name from cfg and returns
// it as host-script configuration. It refuses, rather than weakens, a
// declaration host-script cannot serve as it is served today: one with
// a script that waits for a person's approval (approve: true), which
// host-script has no step for.
func ConvertToHostScripts(cfg Config, name string) (HostScripts, error) {
	path, err := cfg.Path(name)
	if err != nil {
		return HostScripts{}, err
	}
	decl, err := localexec.Load(path)
	if err != nil {
		return HostScripts{}, err
	}
	snap, err := decl.Snapshot()
	if err != nil {
		return HostScripts{}, fmt.Errorf("local-scripts %s: %w", name, err)
	}
	out := HostScripts{Declaration: name, Env: map[string]string{}}
	boundBy := map[string]string{} // server variable -> secret
	for _, s := range snap {
		if s.Approve {
			return HostScripts{}, fmt.Errorf("local-scripts %s: script %s waits for a person to approve each call (approve: true), "+
				"which host-script cannot do; it stays a local-scripts declaration", name, s.Name)
		}
		scriptName, err := HostScriptName(name, s.Name)
		if err != nil {
			return HostScripts{}, err
		}
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = fmt.Sprintf("The %s script of the %s local-scripts declaration.", s.Name, name)
		}
		if len(desc) > hostScriptDescriptionMax {
			desc = strings.ToValidUTF8(desc[:hostScriptDescriptionMax-3], "") + "..."
		}
		entry := map[string]any{
			"name":        scriptName,
			"description": desc,
			// exec keeps the script's own interpreter (its #! line) and its
			// argv exactly; host-script runs this as bash <file> "$@".
			"script":     "exec " + shellQuote(s.Path) + " \"$@\"\n",
			"cwd":        decl.WorkdirPath(),
			"timeout":    timeoutSeconds(s.Timeout),
			"max_output": decl.Cap(),
			// local-scripts never limited a run's calls; host-script's
			// default would.
			"max_calls": math.MaxInt32,
		}
		if len(s.Env) > 0 {
			env := map[string]any{}
			for variable, secret := range s.Env {
				bound := secretBinding(secret)
				if prev, ok := boundBy[bound]; ok && prev != secret {
					return HostScripts{}, fmt.Errorf("local-scripts %s: secrets %q and %q would share the host-script variable %s",
						name, prev, secret, bound)
				}
				boundBy[bound] = secret
				out.Env[bound] = "$secret:" + secret
				env[variable] = "$env:" + bound
			}
			entry["env"] = env
		}
		out.Scripts = append(out.Scripts, entry)
	}
	if len(out.Scripts) == 0 {
		return HostScripts{}, fmt.Errorf("local-scripts %s: the declaration serves no script", name)
	}
	return out, nil
}

// secretBinding is the host-script server variable secret is bound to.
func secretBinding(secret string) string {
	var b strings.Builder
	b.WriteString(secretBindingPrefix)
	for _, r := range strings.ToUpper(secret) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// timeoutSeconds is d in host-script's whole seconds, rounded up and
// kept within host-script's 1..86400.
func timeoutSeconds(d time.Duration) int {
	secs := int(math.Ceil(d.Seconds()))
	return min(max(secs, 1), 86400)
}

// shellQuote quotes s as one bash word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsHostScriptServer reports whether c is a host-script capability
// server.
func IsHostScriptServer(c protocol.ServerConfig) bool {
	if c.Builtin == "host-script" {
		return true
	}
	return c.Command == "grain" && slices.Equal(c.Args, []string{"capability", "host-script"})
}

// HostScriptTarget picks the host-script server in servers a
// declaration's scripts are added to: the one already running in the
// daemon's own environment, as local scripts do, preferring
// HostScriptServerID and otherwise the first by id. A server started
// through hostd (run: host) is passed over, since the state
// repository's checkout it would run the scripts from is the daemon's.
// It is HostScriptServerID, and false, when there is none to add to.
func HostScriptTarget(servers map[string]protocol.ServerConfig) (string, bool) {
	if c, ok := servers[HostScriptServerID]; ok && IsHostScriptServer(c) && !c.OnHost() {
		return HostScriptServerID, true
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if c := servers[id]; IsHostScriptServer(c) && !c.OnHost() {
			return id, true
		}
	}
	return HostScriptServerID, false
}

// AddToHostScriptServer adds each of decls' scripts and secret bindings
// to the host-script server HostScriptTarget picks in servers, creating
// that server when there is none, and returns its id. A script name the
// server already configures is left as the operator has it, so adding
// the same declarations twice changes nothing; a binding the server
// already makes differently is an error, since the scripts would then
// read the wrong secret.
func AddToHostScriptServer(servers map[string]protocol.ServerConfig, decls []HostScripts) (string, error) {
	id, ok := HostScriptTarget(servers)
	entry := servers[id]
	if !ok {
		entry = protocol.ServerConfig{Command: "grain", Args: []string{"capability", "host-script"}, Builtin: "host-script"}
	}
	config := map[string]any{}
	for k, v := range entry.Config {
		config[k] = v
	}
	var scripts []any
	have := map[string]bool{}
	if list, ok := config["scripts"].([]any); ok {
		for _, item := range list {
			scripts = append(scripts, item)
			if m, ok := item.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					have[n] = true
				}
			}
		}
	}
	env := map[string]string{}
	for k, v := range entry.Env {
		env[k] = v
	}
	for _, d := range decls {
		for variable, value := range d.Env {
			if prev, ok := env[variable]; ok && prev != value {
				return "", fmt.Errorf("local-scripts %s: host-script server %s already binds %s to %q, not %q",
					d.Declaration, id, variable, prev, value)
			}
			env[variable] = value
		}
		for _, s := range d.Scripts {
			name, _ := s["name"].(string)
			if have[name] {
				continue
			}
			have[name] = true
			scripts = append(scripts, s)
		}
	}
	config["scripts"] = scripts
	entry.Config = config
	if len(env) > 0 {
		entry.Env = env
	}
	servers[id] = entry
	return id, nil
}

// HostScriptGrant is the recorded grant of host-script script on the
// server id.
func HostScriptGrant(id, script string) (string, error) {
	return model.EncodeCapabilityGrant(protocol.GrantSpec{
		Capability: id + "#" + HostScriptMethod,
		With:       map[string]any{"script": script},
	})
}

// HostScriptGrants is every grant local-scripts:<h.Declaration> becomes
// on the server id, one per script.
func HostScriptGrants(id string, h HostScripts) ([]string, error) {
	var out []string
	for _, name := range h.Names() {
		g, err := HostScriptGrant(id, name)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}
