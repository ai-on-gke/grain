// Package localscripts is the "local-scripts:<name>" capability: the
// grant that serves a run the scripts of one local-scripts declaration
// (pkg/localexec, docs/local-scripts.md) as MCP tools that run on the
// node host.
//
// A declaration is found in one of two places, and never in the
// repository the agent works on, which it can push to: the deployment's
// state repository, at local-scripts/<name>/local-scripts.yaml with its
// scripts beside it, or an absolute path a host-managed installation
// names for it (Config.Host). The capability is write-scoped in the
// sense repo-write:, package-publish and github-sandbox are: what it
// hands a run is code that runs on the host with the host's own
// credentials, so it is refused to every run grain does not trust -- a
// scheduled run, and any CI job (Resolve).
package localscripts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bwsalmon/grain/pkg/localexec"
	"github.com/bwsalmon/grain/pkg/model"
)

// CapabilityPrefix starts every local-scripts capability ID.
const CapabilityPrefix = "local-scripts:"

// DeclarationFile is a declaration's file name inside its folder.
const DeclarationFile = "local-scripts.yaml"

// StateRepoDir is the state repository's directory of declarations.
const StateRepoDir = "local-scripts"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Name returns the declaration a capability ID names, and whether id is
// a local-scripts capability with a well-formed name at all.
func Name(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, CapabilityPrefix)
	if !ok || !namePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

// CapabilityName is the capability that grants the declaration name.
func CapabilityName(name string) string { return CapabilityPrefix + name }

// Granted is every declaration name task's grants name, in grant order,
// once each.
func Granted(task model.Task) []string {
	var out []string
	for _, g := range task.Grants {
		if name, ok := Name(g.Capability); ok && !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Untrusted reports why task may not hold a local-scripts capability, or
// "" when it may: a task a schedule filed runs with nobody having asked
// for that run, which is the run every write-scoped capability is kept
// from.
func Untrusted(task model.Task) string {
	if task.Origin.Reason == model.ReasonSchedule {
		return "a scheduled run is never served local scripts: they run on the node host, " +
			"so a person has to ask for the run that holds them"
	}
	return ""
}

// Config says where declarations are found.
type Config struct {
	// StateDir is the node host's checkout of the deployment's state
	// repository; "" when this host has none.
	StateDir string
	// Host maps a declaration name to the absolute path of a
	// declaration file a host-managed installation keeps. It wins over
	// the state repository's declaration of the same name.
	Host map[string]string
}

// Path is the declaration file for name: Host's when it names one,
// otherwise the state repository's. It is an error for neither to hold
// a file.
func (c Config) Path(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a local-scripts declaration name", name)
	}
	if p, ok := c.Host[name]; ok {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("local-scripts %s: host-managed declaration %q is not an absolute path", name, p)
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("local-scripts %s: %w", name, err)
		}
		return p, nil
	}
	if c.StateDir == "" {
		return "", fmt.Errorf("local-scripts %s: this host has no state repository checkout and no host-managed declaration of that name", name)
	}
	p := filepath.Join(c.StateDir, StateRepoDir, name, DeclarationFile)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("local-scripts %s: no declaration at %s in the state repository: %w",
			name, filepath.Join(StateRepoDir, name, DeclarationFile), err)
	}
	return p, nil
}

// ParseHost parses one -local-scripts NAME=PATH flag value into c.Host.
func (c *Config) ParseHost(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || !namePattern.MatchString(name) {
		return fmt.Errorf("-local-scripts %q: want NAME=/absolute/path/%s", value, DeclarationFile)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("-local-scripts %q: %q is not an absolute path", value, path)
	}
	if c.Host == nil {
		c.Host = map[string]string{}
	}
	c.Host[name] = path
	return nil
}

// Provider is one declaration's capability.
type Provider struct {
	model.BaseCapability
	name string
	cfg  Config
}

// DynamicProvider resolves local-scripts:<name> capability IDs against cfg.
func DynamicProvider(cfg Config) model.DynamicProvider {
	return func(id string) (model.CapabilityProvider, bool) {
		name, ok := Name(id)
		if !ok {
			return nil, false
		}
		return &Provider{name: name, cfg: cfg}, true
	}
}

// Spec describes the capability.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName(p.name),
		Label:       "Local scripts: " + p.name,
		Description: fmt.Sprintf("Serve the scripts of the %q local-scripts declaration as tools that run on the node host", p.name),
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Resolve refuses an untrusted run and a declaration that is not there,
// and honours everything else. A CI job is never served the scripts --
// a CI job has no MCP server, and the push or pull request behind it is
// whatever a contributor wrote -- so a CI context is refused outright,
// whatever its event.
func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if cc.CI {
		return model.RefusedBecause(CapabilityName(p.name) + " is never granted to a CI job: local scripts run on the node host, " +
			"and a CI job's steps are whatever the pushed branch says"), nil
	}
	if why := Untrusted(cc.Task); why != "" {
		return model.RefusedBecause(why), nil
	}
	if _, err := p.cfg.Path(p.name); err != nil {
		return model.RefusedBecause(err.Error()), nil
	}
	return model.Honoured(), nil
}

// SupportsCIEvent admits no CI event: see Resolve.
func (*Provider) SupportsCIEvent(string) bool { return false }

// Credentials is a run's credentials file: each secret a granted
// declaration names, by secret name, resolved.
type Credentials map[string]string

// ResolveCredentials resolves every secret the declarations at paths
// name.
func ResolveCredentials(ctx context.Context, resolver model.CredentialResolver, paths []string) (Credentials, error) {
	out := Credentials{}
	for _, path := range paths {
		d, err := localexec.Load(path)
		if err != nil {
			return nil, err
		}
		for _, secret := range d.Secrets() {
			if _, ok := out[secret]; ok {
				continue
			}
			if resolver == nil {
				return nil, fmt.Errorf("local scripts %s: secret %q: no credential resolver is configured", path, secret)
			}
			value, err := resolver.Resolve(ctx, secret)
			if err != nil {
				return nil, fmt.Errorf("local scripts %s: secret %q: %w", path, secret, err)
			}
			out[secret] = value
		}
	}
	return out, nil
}

// WriteCredentialsFile writes creds to a new 0600 file in dir (the
// system temporary directory when ""), for the run's forked mcpserver
// to read (-local-scripts-env-file) -- the values travel in a file, never
// in argv, for mcp.WriteSandboxEnvFile's reason.
func WriteCredentialsFile(dir string, creds Credentials) (string, error) {
	f, err := os.CreateTemp(dir, "grain-local-scripts-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := json.NewEncoder(f).Encode(creds); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// ReadCredentialsFile reads back WriteCredentialsFile's file, refusing
// one any other user could read.
func ReadCredentialsFile(path string) (Credentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by others (mode %v); it must be 0600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return creds, nil
}

// ScriptEnv turns a snapshot's env entries (variable -> secret) into
// localexec.Config.Env (script -> variable -> value). A secret creds
// does not hold is an error: the script would run without it.
func ScriptEnv(scripts []localexec.Snapshotted, creds Credentials) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	var errs []error
	for _, s := range scripts {
		for variable, secret := range s.Env {
			value, ok := creds[secret]
			if !ok {
				errs = append(errs, fmt.Errorf("script %s: secret %q (for %s) was not delivered", s.Name, secret, variable))
				continue
			}
			if out[s.Name] == nil {
				out[s.Name] = map[string]string{}
			}
			out[s.Name][variable] = value
		}
	}
	return out, errors.Join(errs...)
}

// Open snapshots the declaration at each path and returns one Executor
// for each, serving its scripts with creds delivered per script. Any
// refusal fails the whole call, as Declaration.Snapshot fails the whole
// snapshot: a run is never served part of what it was granted without
// saying so. Two declarations serving the same tool name are refused.
func Open(paths []string, creds Credentials, approver localexec.Approver) ([]*localexec.Executor, error) {
	var out []*localexec.Executor
	closeAll := func() {
		for _, e := range out {
			e.Close()
		}
	}
	seen := map[string]string{}
	for _, path := range paths {
		d, err := localexec.Load(path)
		if err != nil {
			closeAll()
			return nil, err
		}
		scripts, err := d.Snapshot()
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("local scripts %s: %w", path, err)
		}
		for _, s := range scripts {
			if other, ok := seen[s.Tool]; ok {
				closeAll()
				return nil, fmt.Errorf("local scripts: %s is served by both %s and %s", s.Tool, other, path)
			}
			seen[s.Tool] = path
		}
		env, err := ScriptEnv(scripts, creds)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("local scripts %s: %w", path, err)
		}
		out = append(out, localexec.New(localexec.Config{
			Scripts:   scripts,
			Workdir:   d.WorkdirPath(),
			Environ:   localexec.BaseEnviron(os.Environ()),
			Env:       env,
			OutputCap: d.Cap(),
			Approver:  approver,
		}))
	}
	return out, nil
}
