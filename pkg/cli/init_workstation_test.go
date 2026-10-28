package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// fakeWorkstation is a machine with exactly the given binaries on PATH,
// a docker that answers or not, and KVM or not. Its docker has the
// grain-sandboxes network once anything creates it, with gateway
// fakeSandboxGateway.
func fakeWorkstation(home string, binaries []string, dockerWorks, kvm bool) (workstationEnv, *[]string) {
	var ran []string
	network := false
	have := map[string]bool{}
	for _, b := range binaries {
		have[b] = true
	}
	return workstationEnv{
		LookPath: func(name string) (string, error) {
			if have[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			if name == "docker" && !dockerWorks {
				return errors.New("permission denied")
			}
			return nil
		},
		Output: func(_ context.Context, name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			ran = append(ran, cmd)
			if name != "docker" || !dockerWorks {
				return "", errors.New("permission denied")
			}
			switch {
			case strings.HasPrefix(cmd, "docker network create ") && strings.HasSuffix(cmd, " grain-sandboxes"):
				network = true
				return "", nil
			case strings.HasPrefix(cmd, "docker network inspect grain-sandboxes ") && network:
				return `[{"Subnet":"10.9.0.0/24","Gateway":"` + fakeSandboxGateway + `"}]` + "\n", nil
			}
			return "", errors.New("no such network")
		},
		Getenv: func(k string) string {
			switch k {
			case "HOME":
				return home
			case "PATH":
				return "/usr/bin:/bin"
			}
			return ""
		},
		KVM: func() bool { return kvm },
	}, &ran
}

const fakeSandboxGateway = "10.9.0.1"

func TestWorkstationPicksTheStrongestSandboxThisMachineRuns(t *testing.T) {
	for name, c := range map[string]struct {
		binaries    []string
		dockerWorks bool
		kvm         bool
		want        string
	}{
		"granule":                 {[]string{"docker", "granule"}, true, true, "granule"},
		"granule without kvm":     {[]string{"docker", "granule"}, true, false, "container"},
		"docker":                  {[]string{"docker"}, true, false, "container"},
		"docker not in its group": {[]string{"docker"}, false, true, "host"},
		"nothing":                 {nil, false, false, "host"},
	} {
		env, _ := fakeWorkstation(t.TempDir(), c.binaries, c.dockerWorks, c.kvm)
		if got := detectSandboxBackend(context.Background(), env); got.Backend != c.want || got.Why == "" {
			t.Errorf("%s: picked %+v, want %s with a reason", name, got, c.want)
		}
	}
}

func TestWorkstationPicksTheModelCLIOnPath(t *testing.T) {
	for _, c := range []struct {
		binaries []string
		want     string
	}{
		{[]string{"codex", "claude"}, "claude"},
		{[]string{"codex", "agy"}, "codex"},
		{[]string{"agy"}, "agy"},
		{nil, "agy"},
	} {
		env, _ := fakeWorkstation(t.TempDir(), c.binaries, false, false)
		if got, _ := detectModelBackend(env); got != c.want {
			t.Errorf("with %v on PATH picked %s, want %s", c.binaries, got, c.want)
		}
	}
}

func TestTheStarterPolicyFailsClosed(t *testing.T) {
	widgets := &model.RepoRef{Owner: "acme", Name: "widgets"}
	text := renderWorkstationPolicy(workstationOptions{
		Repos: []string{"acme/widgets"}, Owners: []string{"me@example.com"}, Network: "none",
	})
	p, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatalf("the starter policy does not parse: %v\n%s", err, text)
	}
	own := model.Task{Target: widgets, Creator: "me@example.com", NetworkAccess: model.NetworkNone}
	if err := p.Check(own, model.NetworkNone); err != nil {
		t.Errorf("the owner's own task on the named repository was refused: %v", err)
	}
	// grain grants grain-ci to every task on a repository using grain
	// CI; refusing it held every such task forever (grain/task-4160).
	ownWithCI := own
	ownWithCI.Grants = []model.Grant{{Capability: grainci.CapabilityName, Via: model.GrantByDefault}}
	if err := p.Check(ownWithCI, model.NetworkNone); err != nil {
		t.Errorf("the owner's own task on a grain CI repository was refused: %v", err)
	}
	for name, task := range map[string]model.Task{
		"another repository":  {Target: &model.RepoRef{Owner: "acme", Name: "other"}, Creator: "me@example.com"},
		"a capability":        {Target: widgets, Creator: "me@example.com", Grants: []model.Grant{{Capability: "gcp-key"}}},
		"open network":        {Target: widgets, Creator: "me@example.com", NetworkAccess: model.NetworkOpen},
		"unapproved stranger": {Target: widgets, Creator: "stranger@example.com"},
	} {
		if err := p.Check(task, model.NetworkNone); err == nil {
			t.Errorf("the starter policy let %s through", name)
		}
	}
	if err := p.CheckUnisolated(p.IsOwn(policy.Requester{Name: "stranger@example.com"})); err == nil {
		t.Error("the starter policy runs a stranger's task on host sandboxes")
	}

	withCaps, err := policy.Parse([]byte(renderWorkstationPolicy(workstationOptions{
		Repos: []string{"acme/widgets"}, Owners: []string{"me"}, Capabilities: []string{"gcp-key"}, Network: "open",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if err := withCaps.Check(model.Task{Target: widgets, Creator: "me", Grants: []model.Grant{{Capability: "gcp-key:team-a"}}}, ""); err != nil {
		t.Errorf("a named capability was refused: %v", err)
	}
	if err := withCaps.Check(model.Task{Target: widgets, Creator: "me", Grants: []model.Grant{{Capability: "self-debug"}}}, ""); err == nil {
		t.Error("a capability nobody named was allowed")
	}
	if err := withCaps.Check(model.Task{Target: widgets, Creator: "me", Grants: []model.Grant{{Capability: grainci.CapabilityName}}}, ""); err != nil {
		t.Errorf("naming a capability took grain-ci away: %v", err)
	}
}

func TestInitWorkstationWritesWhatTheServiceRuns(t *testing.T) {
	pinDefaultGitHost(t)
	home := t.TempDir()
	env, ran := fakeWorkstation(home, []string{"docker", "codex", "systemctl"}, true, false)
	var out bytes.Buffer
	err := runInitWorkstation(context.Background(), []string{"-non-interactive", "-repo", "acme/widgets", "-repo", "https://github.com/acme/tools",
		"-owner", "me@example.com", "-network", "none"}, strings.NewReader(""), &out, env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	dataDir := filepath.Join(home, ".grain")
	if _, err := policy.Load(filepath.Join(dataDir, "grain_policy.yml")); err != nil {
		t.Fatalf("the policy it wrote does not load: %v", err)
	}
	launcher, err := os.ReadFile(filepath.Join(dataDir, "daemon.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'-sandbox-backend' 'container'", "'-model-backend' 'codex'",
		"'-container-git-proxy-host' '" + fakeSandboxGateway + "'",
		"'-repo' 'acme/tools'", "'-ui-addr' '127.0.0.1:4242'", "PATH='/usr/bin:/bin'", "\"$@\""} {
		if !strings.Contains(string(launcher), want) {
			t.Errorf("daemon.sh has no %s:\n%s", want, launcher)
		}
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "grain.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ExecStart=" + filepath.Join(dataDir, "daemon.sh"), "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("grain.service has no %s:\n%s", want, unit)
		}
	}
	if got := strings.Join(*ran, "; "); !strings.Contains(got, "systemctl --user enable --now grain.service") {
		t.Errorf("the service was not enabled; ran %s", got)
	}

	// A re-run keeps the owner's own policy.
	edited := []byte("repos: [acme/widgets]\n")
	if err := os.WriteFile(filepath.Join(dataDir, "grain_policy.yml"), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInitWorkstation(context.Background(), []string{"-non-interactive", "-start-service=false"}, strings.NewReader(""), &out, env); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dataDir, "grain_policy.yml")); !bytes.Equal(got, edited) {
		t.Errorf("a re-run replaced the edited policy with:\n%s", got)
	}
}

func TestInitWorkstationAsksWhatItWasNotTold(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeWorkstation(home, []string{"claude"}, false, false)
	answers := strings.Join([]string{
		"",             // data directory: the default
		"acme/widgets", // repositories
		"me@example.com",
		"", // capabilities: none
		"", // sandbox: host, the only one there is
		"", // model: claude, found on PATH
		"", // no GitHub token
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := runInitWorkstation(context.Background(), []string{"-install-service=false"}, strings.NewReader(answers), &out, env); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "no isolation at all") {
		t.Errorf("choosing host sandboxes did not say what they cost:\n%s", out.String())
	}
	launcher, err := os.ReadFile(filepath.Join(home, ".grain", "daemon.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcher), "'-sandbox-backend' 'host'") || !strings.Contains(string(launcher), "'-model-backend' 'claude'") {
		t.Errorf("daemon.sh does not run what was chosen:\n%s", launcher)
	}
}

func TestInitWorkstationRefusesWhatTheDaemonWould(t *testing.T) {
	for name, args := range map[string][]string{
		"no network on host":       {"-sandbox-backend", "host", "-network", "none"},
		"unknown backend":          {"-sandbox-backend", "vm"},
		"container with no docker": {"-sandbox-backend", "container"},
		"git proxy host for host":  {"-sandbox-backend", "host", "-container-git-proxy-host", "10.9.0.1"},
	} {
		env, _ := fakeWorkstation(t.TempDir(), nil, false, false)
		var out bytes.Buffer
		if err := runInitWorkstation(context.Background(), append([]string{"-non-interactive", "-install-service=false"}, args...),
			strings.NewReader(""), &out, env); err == nil {
			t.Errorf("%s: init succeeded:\n%s", name, out.String())
		}
	}
}

// TestInitWorkstationGivesContainerSandboxesAGitProxyHost is the daemon's
// own requirement (-container-git-proxy-host is required with
// -container-sandboxes) met by init: the gateway of the network the
// sandboxes join, created first if docker has none yet, or the address
// the flag names, with no docker asked at all.
func TestInitWorkstationGivesContainerSandboxesAGitProxyHost(t *testing.T) {
	for name, c := range map[string]struct {
		args       []string
		want       string
		wantCreate bool
	}{
		"derived":  {nil, fakeSandboxGateway, true},
		"as given": {[]string{"-container-git-proxy-host", "192.0.2.7"}, "192.0.2.7", false},
	} {
		home := t.TempDir()
		env, ran := fakeWorkstation(home, []string{"docker"}, true, false)
		var out bytes.Buffer
		args := append([]string{"-non-interactive", "-install-service=false", "-sandbox-backend", "container", "-model-backend", "claude"}, c.args...)
		if err := runInitWorkstation(context.Background(), args, strings.NewReader(""), &out, env); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out.String())
		}
		launcher, err := os.ReadFile(filepath.Join(home, ".grain", "daemon.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(launcher), "'-container-git-proxy-host' '"+c.want+"'") {
			t.Errorf("%s: daemon.sh does not pass -container-git-proxy-host %s:\n%s", name, c.want, launcher)
		}
		created := strings.Contains(strings.Join(*ran, "; "), "docker network create --driver bridge --label grain.sandbox=true")
		if created != c.wantCreate {
			t.Errorf("%s: created the sandbox network = %v, want %v; ran %s", name, created, c.wantCreate, strings.Join(*ran, "; "))
		}
	}
}
