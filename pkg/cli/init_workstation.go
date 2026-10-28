// init_workstation.go is `grain init workstation`: standing up a
// single-tenant Grain for one person or team on a workstation, laptop
// or VM, as that person's own account, with no root and no checkout of
// grain. `grain init` proper (init.go) is the operator's install of a
// shared deployment onto a host it takes over; this is the other end of
// that range.
//
// It writes four things into the data directory and one beside the
// user's other systemd units, and nothing else:
//
//   - grain_policy.yml, the starter local policy (docs/local-policy.md),
//     fail-closed: only the repositories named, no capability but grain
//     CI's own unless one is named, the network chosen. An existing policy is kept, so a
//     re-run never undoes the owner's own edits (-force rewrites it).
//   - daemon.sh, the daemon's command line, with the PATH this ran under
//     so the daemon finds the same docker, granule and model CLI.
//   - secrets/github/workstation.token, when -github-token-file names a
//     token, narrowed to the policy's repositories.
//   - ~/.config/systemd/user/grain.service, which runs daemon.sh.
//
// Interactive by default: every choice not given as a flag is asked,
// with the detected answer as its default. -non-interactive takes the
// defaults instead, which is how CI and scripts run it.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/policy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const initWorkstationUsage = `usage: grain init workstation [flags]

Sets up a single-tenant Grain for this account: a data directory, a
starter local policy that refuses everything it was not told to allow,
the sandbox backend and model CLI this machine has, and a systemd user unit (grain.service) that keeps the daemon
running. Every choice not given as a flag is asked; -non-interactive
takes the detected defaults instead. See docs/quickstart.md.

Flags:
`

// workstationCredentialName is what -github-token-file's token is
// stored as (secrets/github/<name>.token).
const workstationCredentialName = "workstation"

// workstationEnv is everything init workstation reads off the machine,
// so a test can stand in for a host with docker or without.
type workstationEnv struct {
	LookPath func(string) (string, error)
	// Run runs a command to completion, reporting only whether it worked.
	Run func(ctx context.Context, name string, args ...string) error
	// Output runs a command to completion and returns its stdout.
	Output func(ctx context.Context, name string, args ...string) (string, error)
	Getenv func(string) string
	// KVM reports whether /dev/kvm is there and usable by this account.
	KVM func() bool
}

func hostWorkstationEnv() workstationEnv {
	return workstationEnv{
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			// This account's own environment, on purpose: docker and
			// systemctl --user find their daemons through it.
			cmd.Env = os.Environ()
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			return cmd.Run()
		},
		Output: func(ctx context.Context, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = os.Environ()
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil && stderr.Len() > 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
			}
			return string(out), err
		},
		Getenv: os.Getenv,
		KVM: func() bool {
			f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
			if err != nil {
				return false
			}
			f.Close()
			return true
		},
	}
}

// workstationOptions are the answers init workstation writes out.
type workstationOptions struct {
	DataDir        string
	Repos          []string // owner/name (or host/owner/name), resolved
	Owners         []string // local_requesters
	Capabilities   []string // capabilities.allow
	Network        string   // open or none
	SandboxBackend string   // container, granule or host
	// ContainerGitProxyHost is the address container sandboxes reach
	// the daemon at; only with SandboxBackend container.
	ContainerGitProxyHost string
	ModelBackend          string // claude, codex or agy
	UIAddr                string
	GrainBinary           string
	Path                  string // the PATH daemon.sh runs the daemon with
}

// sandboxChoice is detectSandboxBackend's answer and why.
type sandboxChoice struct{ Backend, Why string }

// detectSandboxBackend picks the strongest sandbox this machine can run:
// a granule VM when the granule CLI, KVM and docker are all there, a
// container when docker answers, and otherwise a plain host directory,
// which isolates nothing.
func detectSandboxBackend(ctx context.Context, env workstationEnv) sandboxChoice {
	docker := false
	if _, err := env.LookPath("docker"); err == nil {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		docker = env.Run(c, "docker", "info") == nil
		cancel()
	}
	_, granuleErr := env.LookPath("granule")
	switch {
	case docker && granuleErr == nil && env.KVM():
		return sandboxChoice{string(model.SandboxBackendGranule),
			"granule, KVM and docker are all here: each run gets its own VM"}
	case docker:
		return sandboxChoice{string(model.SandboxBackendContainer),
			"docker answers: each run gets its own container (install granule and use KVM for a VM per run)"}
	default:
		return sandboxChoice{string(model.SandboxBackendHost),
			"no working docker (is this account in the docker group?): runs get a plain directory on this host, " +
				"executing as you with your files, keys and network in reach -- no isolation at all. " +
				"Install docker and run this again for a container per run"}
	}
}

// modelCLIs are the model CLIs init workstation looks for, in the order
// it prefers them, with the -model-backend each one is.
var modelCLIs = []struct{ Binary, Backend string }{
	{"claude", "claude"}, {"codex", "codex"}, {"agy", "agy"},
}

// detectModelBackend is the first model CLI on PATH, or agy (the
// daemon's own default, antigravity) when there is none.
func detectModelBackend(env workstationEnv) (backend, why string) {
	for _, c := range modelCLIs {
		if p, err := env.LookPath(c.Binary); err == nil {
			return c.Backend, "found " + p
		}
	}
	return "agy", "no claude, codex or agy on PATH; agy (Antigravity) is the daemon's default -- install a CLI, or add a connection in the UI"
}

// renderWorkstationPolicy is the starter policy: the shape
// docs/local-policy.md documents, with every restriction spelled out so
// the owner can see what to loosen.
func renderWorkstationPolicy(o workstationOptions) string {
	var b strings.Builder
	b.WriteString("# grain_policy.yml, written by `grain init workstation`. It is yours to\n" +
		"# edit; a re-run keeps it. Every key is described in docs/local-policy.md.\n" +
		"# The daemon reads it at start: `systemctl --user restart grain` after editing.\n\n")
	b.WriteString("# Repositories a task may target, read or write. Nothing else.\n")
	writeYAMLList(&b, "repos", o.Repos)
	b.WriteString("\n# Whose tasks are this machine's own: they need no further approval.\n" +
		"# Anybody else's needs an approval by one of approvers.\n")
	writeYAMLList(&b, "local_requesters", o.Owners)
	writeYAMLList(&b, "approvers", o.Owners)
	b.WriteString("allow_unapproved_external: false\n")
	b.WriteString("\n# Capabilities a task may be granted.\n")
	b.WriteString("capabilities:\n")
	// grain-ci is always allowed: grain grants it to every task on a
	// repository whose CI mode is grain, and it carries no credential --
	// it is the run's own wait_for_checks against that repository's
	// .grain/workflows. Denied, every such task was held at dispatch
	// forever, and a standalone node could not test its own change.
	allow := []string{grainci.CapabilityName}
	for _, c := range o.Capabilities {
		if !strings.EqualFold(c, grainci.CapabilityName) {
			allow = append(allow, c)
		}
	}
	if len(o.Capabilities) == 0 {
		b.WriteString("  # Only grain CI's own: allow lists the ones to permit, and deny wins over it.\n")
	} else {
		b.WriteString("  # Only these, and grain CI's own. Add to deny anything allow's patterns cover too widely.\n")
	}
	b.WriteString("  allow: [" + yamlQuoteAll(allow) + "]\n")
	b.WriteString("\n# The network a task's sandbox may have.\n")
	writeYAMLList(&b, "network", []string{o.Network})
	b.WriteString("\n# Host sandboxes run as you; only your own tasks run there.\n" +
		"allow_external_on_host_sandboxes: false\n")
	return b.String()
}

func writeYAMLList(b *strings.Builder, key string, items []string) {
	fmt.Fprintf(b, "%s: [%s]\n", key, yamlQuoteAll(items))
}

func yamlQuoteAll(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}

// workstationDaemonArgs is daemon.sh's command line after the binary.
func workstationDaemonArgs(o workstationOptions) []string {
	args := []string{"daemon",
		"-data-dir", o.DataDir,
		"-policy", filepath.Join(o.DataDir, "grain_policy.yml"),
		"-ui-addr", o.UIAddr,
		"-sandbox-backend", o.SandboxBackend,
		"-model-backend", o.ModelBackend,
	}
	if o.SandboxBackend == string(model.SandboxBackendContainer) {
		args = append(args, "-container-git-proxy-host", o.ContainerGitProxyHost)
	}
	if o.SandboxBackend == string(model.SandboxBackendHost) {
		args = append(args, "-sandbox-dir", filepath.Join(o.DataDir, "sandboxes"))
	}
	for _, r := range o.Repos {
		args = append(args, "-repo", r)
	}
	return args
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderWorkstationLauncher is daemon.sh: the daemon's whole command
// line in one file, so the unit, a foreground run and a reader asking
// "how is this daemon started" all see the same one. Extra arguments
// are passed through, which is how a one-off flag is tried.
func renderWorkstationLauncher(o workstationOptions) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Written by `grain init workstation`; a re-run rewrites it.\n" +
		"# The PATH is the one init ran under, so the daemon finds the same\n" +
		"# docker, granule and model CLI from under systemd as from a shell.\n")
	fmt.Fprintf(&b, "PATH=%s\nexport PATH\n", shellQuote(o.Path))
	fmt.Fprintf(&b, "%s=%s\nexport %s\n", dataDirEnvVar, shellQuote(o.DataDir), dataDirEnvVar)
	b.WriteString("exec " + shellQuote(o.GrainBinary) + " daemon")
	args := workstationDaemonArgs(o)[1:]
	for i := 0; i < len(args); i++ {
		// One flag and its value to a line.
		b.WriteString(" \\\n  " + shellQuote(args[i]))
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			b.WriteString(" " + shellQuote(args[i]))
		}
	}
	b.WriteString(" \\\n  \"$@\"\n")
	return b.String()
}

// renderWorkstationUnit is grain.service, a systemd user unit. It
// restarts the daemon when it fails but gives up after five failures in
// ten minutes rather than spinning on a bad flag; `systemctl --user
// reset-failed grain` and a start try again. Logs go to the user journal.
func renderWorkstationUnit(o workstationOptions) string {
	launcher := filepath.Join(o.DataDir, "daemon.sh")
	return fmt.Sprintf(`# Written by `+"`grain init workstation`"+`; a re-run rewrites it.
# Logs: journalctl --user -u grain -f
[Unit]
Description=Grain (single-tenant) daemon
Documentation=https://github.com/ai-on-gke/grain/blob/main/docs/quickstart.md
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=10min
StartLimitBurst=5

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=10s
# The daemon drains its live runs on SIGTERM before it exits.
KillMode=mixed
TimeoutStopSec=120s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=grain

[Install]
WantedBy=default.target
`, systemdQuote(launcher))
}

// systemdQuote quotes a path for a unit file's ExecStart: only a path
// with whitespace or a quote needs it.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// userUnitDir is where systemd --user reads units an account installs.
func userUnitDir(env workstationEnv) (string, error) {
	if x := env.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home := env.Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = h
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func initWorkstationCmd(args []string) {
	if err := runInitWorkstation(context.Background(), args, os.Stdin, os.Stdout, hostWorkstationEnv()); err != nil {
		fmt.Fprintln(os.Stderr, "grain init workstation: "+err.Error())
		os.Exit(1)
	}
}

func runInitWorkstation(ctx context.Context, args []string, stdin io.Reader, out io.Writer, env workstationEnv) error {
	fs := flag.NewFlagSet("grain init workstation", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, initWorkstationUsage); fs.PrintDefaults() }
	defaultDataDir := ""
	if home := env.Getenv("HOME"); home != "" {
		defaultDataDir = filepath.Join(home, ".grain")
	}
	if v := strings.TrimSpace(env.Getenv(dataDirEnvVar)); v != "" {
		defaultDataDir = v
	}
	nonInteractive := fs.Bool("non-interactive", false, "ask nothing: take the detected default for every choice not given as a flag")
	dataDir := fs.String("data-dir", defaultDataDir, "the daemon's data directory ($"+dataDirEnvVar+", else ~/.grain)")
	var repos, owners, caps stringSliceFlag
	fs.Var(&repos, "repo", "a repository tasks may work on, as owner/name, a URL or a local checkout; repeatable. None refuses every task that names one")
	fs.Var(&owners, "owner", "a requester whose tasks are this machine's own (local_requesters); repeatable. Defaults to git's user.email; "+
		"this account's name, which the daemon files the local CLI's and UI's tasks as, is always added")
	fs.Var(&caps, "capability", "a capability pattern tasks may be granted (capabilities.allow); repeatable. None denies every capability")
	network := fs.String("network", "", "network a task's sandbox may have: open or none (default open; host sandboxes have the host's own network, so only open)")
	sandboxBackend := fs.String("sandbox-backend", "", "container, granule or host (default: the strongest this machine runs)")
	containerProxyHost := fs.String("container-git-proxy-host", "", "with container sandboxes, the address a sandbox container reaches "+
		"this host at (default: the gateway of docker network "+orchestrator.DefaultContainerNetwork+", which this creates if it is missing)")
	modelBackend := fs.String("model-backend", "", "claude, codex or agy (default: the first on PATH)")
	uiAddr := fs.String("ui-addr", "127.0.0.1:4242", "the address the UI and API listen on")
	githubTokenFile := fs.String("github-token-file", "", "a file holding a GitHub token, stored narrowed to -repo's repositories")
	installService := fs.Bool("install-service", true, "write the grain.service systemd user unit")
	startService := fs.Bool("start-service", true, "enable and start grain.service (needs a systemd user session)")
	force := fs.Bool("force", false, "rewrite grain_policy.yml even if one exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	if err := refuseTokenShapedArgs(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	p := prompter{in: bufio.NewReader(stdin), out: out, nonInteractive: *nonInteractive}

	var err error
	o := workstationOptions{UIAddr: *uiAddr, Path: env.Getenv("PATH")}

	if !set["data-dir"] {
		if *dataDir, err = p.ask("Data directory", *dataDir); err != nil {
			return err
		}
	}
	if *dataDir == "" {
		return errors.New("-data-dir is required: $HOME is not set")
	}
	if o.DataDir, err = filepath.Abs(*dataDir); err != nil {
		return err
	}

	if !set["repo"] {
		a, err := p.ask("Repositories tasks may work on (owner/name, URL or checkout path; comma-separated)", "")
		if err != nil {
			return err
		}
		repos = splitList(a)
	}
	for _, r := range repos {
		ref, err := daemon.ResolveLocalRepo(r)
		if err != nil {
			return fmt.Errorf("-repo %s: %w", r, err)
		}
		o.Repos = append(o.Repos, ref.String())
	}

	if len(owners) == 0 {
		owners = defaultOwners(ctx, env)
		if !set["owner"] && !*nonInteractive {
			a, err := p.ask("Your requester identities (local_requesters)", strings.Join(owners, ","))
			if err != nil {
				return err
			}
			owners = splitList(a)
		}
	}
	// The daemon files the local CLI's and UI's tasks as this account
	// (its default -as), so that account is always an owner: without it
	// the owner's own tasks would be held as a stranger's.
	if u := currentUserName(); u != "" && !containsFold(owners, u) {
		owners = append(owners, u)
	}
	o.Owners = owners
	if !set["capability"] && !*nonInteractive {
		a, err := p.ask("Capabilities tasks may be granted (blank for none)", "")
		if err != nil {
			return err
		}
		caps = splitList(a)
	}
	o.Capabilities = caps

	choice := sandboxChoice{Backend: *sandboxBackend, Why: "as asked"}
	if choice.Backend == "" {
		choice = detectSandboxBackend(ctx, env)
		fmt.Fprintf(out, "Sandbox backend: %s -- %s.\n", choice.Backend, choice.Why)
		if !*nonInteractive {
			if choice.Backend, err = p.ask("Sandbox backend (container, granule, host)", choice.Backend); err != nil {
				return err
			}
		}
	}
	switch choice.Backend {
	case string(model.SandboxBackendContainer), string(model.SandboxBackendGranule), string(model.SandboxBackendHost):
	default:
		return fmt.Errorf("-sandbox-backend=%s: not one of container, granule, host", choice.Backend)
	}
	o.SandboxBackend = choice.Backend
	if o.SandboxBackend == string(model.SandboxBackendContainer) {
		o.ContainerGitProxyHost = strings.TrimSpace(*containerProxyHost)
		if o.ContainerGitProxyHost == "" {
			gw, err := orchestrator.DefaultContainerNetworkGateway(ctx, func(ctx context.Context, args ...string) (string, error) {
				return env.Output(ctx, "docker", args...)
			})
			if err != nil {
				return fmt.Errorf("container sandboxes: %w -- pass -container-git-proxy-host with the address "+
					"a sandbox container reaches this host at", err)
			}
			o.ContainerGitProxyHost = gw
			fmt.Fprintf(out, "Container sandboxes reach the daemon at %s, the gateway of docker network %s.\n",
				gw, orchestrator.DefaultContainerNetwork)
		}
	} else if set["container-git-proxy-host"] {
		return fmt.Errorf("-container-git-proxy-host is for container sandboxes, not -sandbox-backend=%s", o.SandboxBackend)
	}

	o.Network = *network
	if o.Network == "" {
		o.Network = string(model.NetworkOpen)
		if o.SandboxBackend != string(model.SandboxBackendHost) && !*nonInteractive {
			if o.Network, err = p.ask("Sandbox network (open, none)", o.Network); err != nil {
				return err
			}
		}
	}
	if !model.ValidNetworkAccess(model.NetworkAccess(o.Network)) || o.Network == "" {
		return fmt.Errorf("-network=%s: not one of open, none", o.Network)
	}
	if o.SandboxBackend == string(model.SandboxBackendHost) && o.Network != string(model.NetworkOpen) {
		return errors.New("-network=none needs a container or granule sandbox: a host sandbox has this host's own network, so it could not keep that promise")
	}

	o.ModelBackend = *modelBackend
	if o.ModelBackend == "" {
		backend, why := detectModelBackend(env)
		fmt.Fprintf(out, "Model backend: %s -- %s.\n", backend, why)
		if o.ModelBackend, err = p.ask("Model backend (claude, codex, agy)", backend); err != nil {
			return err
		}
	}
	if _, err := daemon.ApplyLocalSettings(daemon.Config{}, daemon.LocalSettings{ModelBackend: o.ModelBackend}, nil); err != nil {
		return err
	}

	if o.GrainBinary, err = os.Executable(); err != nil {
		return fmt.Errorf("finding this grain binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(o.GrainBinary); err == nil {
		o.GrainBinary = resolved
	}

	// Everything is decided; write it.
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return err
	}
	policyPath := filepath.Join(o.DataDir, "grain_policy.yml")
	text := renderWorkstationPolicy(o)
	if _, err := policy.Parse([]byte(text)); err != nil {
		return fmt.Errorf("the starter policy does not parse (a bug): %w", err)
	}
	if _, statErr := os.Stat(policyPath); statErr == nil && !*force {
		if _, err := policy.Load(policyPath); err != nil {
			return fmt.Errorf("%s exists and does not load (fix it, or pass -force to rewrite it): %w", policyPath, err)
		}
		fmt.Fprintf(out, "Kept the existing %s (-force rewrites it).\n", policyPath)
	} else {
		if err := os.WriteFile(policyPath, []byte(text), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "Wrote the starter policy %s.\n", policyPath)
	}
	launcher := filepath.Join(o.DataDir, "daemon.sh")
	if err := os.WriteFile(launcher, []byte(renderWorkstationLauncher(o)), 0o700); err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s, the daemon's command line.\n", launcher)

	if *githubTokenFile == "" && !*nonInteractive {
		if *githubTokenFile, err = p.ask("File holding a GitHub token for these repositories (blank to add one later)", ""); err != nil {
			return err
		}
	}
	if *githubTokenFile != "" {
		if err := writeWorkstationGitHubToken(o, *githubTokenFile); err != nil {
			return err
		}
		fmt.Fprintf(out, "Stored the GitHub token as %q, for %s only.\n", workstationCredentialName, strings.Join(o.Repos, ", "))
	}

	started := false
	if *installService {
		dir, err := userUnitDir(env)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		unit := filepath.Join(dir, "grain.service")
		if err := os.WriteFile(unit, []byte(renderWorkstationUnit(o)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "Wrote the systemd user unit %s.\n", unit)
		start := *startService
		if start && !set["start-service"] && !*nonInteractive {
			if start, err = p.yes("Enable and start grain.service now?", true); err != nil {
				return err
			}
		}
		if start {
			if err := startWorkstationService(ctx, env); err != nil {
				fmt.Fprintf(out, "Could not start grain.service (%v).\nStart it with: systemctl --user daemon-reload && systemctl --user enable --now grain\n", err)
			} else {
				started = true
				fmt.Fprintln(out, "Started grain.service. Logs: journalctl --user -u grain -f")
				fmt.Fprintf(out, "To keep it running after you log out: loginctl enable-linger %s\n", currentUserName())
			}
		}
	}
	if started && o.Network == string(model.NetworkNone) {
		if err := setWorkstationNetwork(ctx, "http://"+o.UIAddr, o.Network); err != nil {
			fmt.Fprintf(out, "Could not make none the sandbox network default (%v): run `grain settings -network-access none` once the daemon is up, "+
				"or the policy refuses every task that does not ask for none itself.\n", err)
		}
	} else if o.Network == string(model.NetworkNone) {
		fmt.Fprintln(out, "Once the daemon is up, run `grain settings -network-access none`: the policy allows only that network.")
	}
	if !started {
		fmt.Fprintf(out, "Run the daemon in the foreground with: %s\n", launcher)
	}
	fmt.Fprintf(out, "Point the grain CLI at it (add this to your shell's profile):\n  export %s=%s\n", dataDirEnvVar, shellQuote(o.DataDir))
	fmt.Fprintf(out, "Then open http://%s, add a model connection under Settings, and file a task:\n"+
		"  grain create -title \"Say hello\" -approve\n", o.UIAddr)
	return nil
}

// defaultOwners is who this machine belongs to: git's user.email when it
// has one, and this account's name, which is the actor tasks filed from
// this machine's own CLI and UI carry.
func defaultOwners(ctx context.Context, env workstationEnv) []string {
	var owners []string
	if _, err := env.LookPath("git"); err == nil {
		cmd := exec.CommandContext(ctx, "git", "config", "--get", "user.email")
		cmd.Env = os.Environ() // git reads this account's own config through $HOME
		if b, err := cmd.Output(); err == nil {
			if e := strings.TrimSpace(string(b)); e != "" {
				owners = append(owners, e)
			}
		}
	}
	if u := currentUserName(); u != "" {
		owners = append(owners, u)
	}
	return owners
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func currentUserName() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

func writeWorkstationGitHubToken(o workstationOptions, tokenFile string) error {
	token, err := daemon.ReadTokenFile(tokenFile)
	if err != nil {
		return fmt.Errorf("-github-token-file: %w", err)
	}
	if len(o.Repos) == 0 {
		return errors.New("-github-token-file needs a -repo to narrow it to: a token for no repository would never be used")
	}
	cs, err := gitproxy.LoadCredentialSet(filepath.Join(o.DataDir, "secrets", "github"))
	if err != nil {
		return err
	}
	if err := cs.SetToken(workstationCredentialName, token); err != nil {
		return err
	}
	// The token's connections are seeded the way every install seeds
	// them: as a `grain connections import` would record them.
	var seed connectionSeed
	for _, r := range o.Repos {
		seed.Git = append(seed.Git, gitConnectionSeed{Credential: workstationCredentialName, Scope: r})
	}
	raw, err := yaml.Marshal(seed)
	if err != nil {
		return err
	}
	if seed, err = parseConnectionSeed(raw); err != nil {
		return fmt.Errorf("connecting -repo to the token: %w", err)
	}
	store, db, err := daemon.OpenStore("", "", o.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	return importConnections(context.Background(), store, o.DataDir, seed, io.Discard)
}

func startWorkstationService(ctx context.Context, env workstationEnv) error {
	if _, err := env.LookPath("systemctl"); err != nil {
		return errors.New("no systemctl on PATH")
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "grain.service"},
	} {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		err := env.Run(c, "systemctl", args...)
		cancel()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// setWorkstationNetwork waits for the daemon at base to answer, then
// makes network its sandbox network default.
func setWorkstationNetwork(ctx context.Context, base, network string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := ui.NewHTTPClient(base)
	for {
		_, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{NetworkAccess: &network})
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(2 * time.Second):
		}
	}
}
