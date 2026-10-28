// This is docs/test-plans/2026-09-13.md section 6 as one durable run. It
// goes beyond the local-path remotes in orchestrator's checkout tests: git
// crosses a real SSH authentication boundary, and the sandbox receives its
// only key as a repo-scoped capability placement. The same live run pushes,
// rebuilds through recreate_sandbox, pushes again, and closes out its PR.
package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

const (
	ambientSSHCapabilityName = "self-debug"
	ambientSSHCredentialName = "ambient-ssh-key"
)

// exactCredentialResolver gives the provider one named secret and nothing
// else, keeping the fixture's credential boundary as narrow as the production
// capability contract it proves.
type exactCredentialResolver struct {
	name, material string
	mu             sync.Mutex
	requested      []string
}

func (r *exactCredentialResolver) Resolve(_ context.Context, name string) (string, error) {
	r.mu.Lock()
	r.requested = append(r.requested, name)
	r.mu.Unlock()
	if name != r.name {
		return "", fmt.Errorf("credential %q is outside this resolver's scope", name)
	}
	return r.material, nil
}

func (r *exactCredentialResolver) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requested...)
}

// ambientSSHCapability turns one named controller-side secret into ordinary
// OpenSSH files. Git sees the key through HOME; neither the clone URL nor a
// run_command argument ever carries credential material.
type ambientSSHCapability struct {
	model.BaseCapability
	knownHosts string
}

func (ambientSSHCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:      ambientSSHCapabilityName,
		Provision: model.ProvisionPlace,
		Requires:  []string{ambientSSHCredentialName},
	}
}

func (p ambientSSHCapability) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	privateKey, err := cc.Credentials.Resolve(ctx, ambientSSHCredentialName)
	if err != nil {
		return model.Materialization{}, err
	}
	gitConfig := strings.Join([]string{
		"[user]",
		"	name = grain ambient-remote test",
		"	email = grain-ambient@localhost",
		"[core]",
		"	sshCommand = ssh -i \"$HOME/.ssh/id_ed25519\" -o UserKnownHostsFile=\"$HOME/.ssh/known_hosts\" -o StrictHostKeyChecking=yes -o IdentitiesOnly=yes -o BatchMode=yes -o LogLevel=ERROR",
		"",
	}, "\n")
	return model.Materialization{Placements: []model.Placement{
		{Side: model.SideSandbox, Path: "/.ssh/id_ed25519", Content: privateKey},
		{Side: model.SideSandbox, Path: "/.ssh/known_hosts", Content: p.knownHosts},
		{Side: model.SideSandbox, Path: "/.gitconfig", Content: gitConfig},
	}}, nil
}

func (ambientSSHCapability) PromptSection(context.Context, model.CapabilityContext, []model.Placement) (string, error) {
	return "Git authentication for this repository is ambient; use origin without reading or printing credential files.", nil
}

// sshGitService is a disposable public-key-only Git server. It accepts only
// the two SSH exec requests smart Git needs and maps the stable owner/name path
// to one bare repository.
type sshGitService struct {
	listener net.Listener
	config   *ssh.ServerConfig
	bareRepo string

	mu       sync.Mutex
	commands []string
	wg       sync.WaitGroup
}

func newSSHGitService(t *testing.T, bareRepo string) (*sshGitService, string, string) {
	t.Helper()
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating client SSH key: %v", err)
	}
	clientKey, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatalf("encoding client SSH public key: %v", err)
	}
	privateBlock, err := ssh.MarshalPrivateKey(clientPrivate, "grain ambient remote")
	if err != nil {
		t.Fatalf("encoding client SSH private key: %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(privateBlock))

	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host SSH key: %v", err)
	}
	hostKey, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatalf("encoding host SSH key: %v", err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, offered ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(offered.Marshal(), clientKey.Marshal()) {
				return nil, errors.New("public key is not authorized for this repository")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for disposable SSH Git service: %v", err)
	}
	service := &sshGitService{listener: listener, config: config, bareRepo: bareRepo}
	service.wg.Add(1)
	go service.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		service.wg.Wait()
	})

	knownHosts := knownhosts.Line([]string{listener.Addr().String()}, hostKey.PublicKey()) + "\n"
	return service, privatePEM, knownHosts
}

func (s *sshGitService) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(conn)
		}()
	}
}

func (s *sshGitService) serveConn(conn net.Conn) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, requests, err := incoming.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveSession(channel, requests)
		}()
	}
}

func (s *sshGitService) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		service, path, ok := strings.Cut(payload.Command, " ")
		path = strings.Trim(strings.TrimSpace(path), "'\"")
		if !ok || (service != "git-upload-pack" && service != "git-receive-pack") ||
			strings.TrimPrefix(path, "/") != "acme/widgets.git" {
			_ = request.Reply(false, nil)
			return
		}
		s.mu.Lock()
		s.commands = append(s.commands, service)
		s.mu.Unlock()

		cmd := exec.Command(service, s.bareRepo)
		cmd.Stdin = channel
		cmd.Stdout = channel
		cmd.Stderr = channel.Stderr()
		if err := cmd.Start(); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		err := cmd.Wait()
		status := uint32(0)
		if err != nil {
			status = 1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				status = uint32(exitErr.ExitCode())
			}
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func (s *sshGitService) commandCount(service string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, command := range s.commands {
		if command == service {
			count++
		}
	}
	return count
}

func (s *sshGitService) commandSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func callAmbientRunCommand(t *testing.T, p *mcpProcess, command string) string {
	t.Helper()
	result, err := p.CallTool(context.Background(), "run_command", map[string]any{"command": command})
	if err != nil {
		t.Fatalf("tools/call run_command: %v\nstderr:\n%s", err, p.stderr.String())
	}
	if result.IsError {
		t.Fatalf("run_command failed: %s", result.Text())
	}
	return result.Text()
}

func TestAmbientCredentialRemoteSurvivesLiveSandboxRecreation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client not installed; the sandbox image recipe installs it for SSH repository support")
	}
	bin := buildGrainBinary(t)
	w := newWorld(t)

	const owner, repoName = "acme", "widgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}
	bareRepo := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sshGit, privateKey, knownHosts := newSSHGitService(t, bareRepo)
	cloneURL := "ssh://git@" + sshGit.listener.Addr().String() + "/" + owner + "/" + repoName + ".git"
	secretNeedle := strings.Split(privateKey, "\n")[1]

	// A process without the scoped key cannot even read the remote.
	unauthorized := exec.Command("git", "ls-remote", cloneURL)
	unauthorized.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_SSH_COMMAND=ssh -F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
	)
	if err := unauthorized.Run(); err == nil {
		t.Fatal("the disposable SSH remote accepted a read without its scoped key")
	}

	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatalf("loading an explicitly empty GitHub credential set: %v", err)
	}
	if _, ok := credentials.Select(owner, repoName); ok {
		t.Fatal("the test target unexpectedly has a GitHub credential")
	}

	recreations := orchestrator.NewSandboxRecreations()
	daemonClient := ui.NewClient(ui.Config{
		Actor:           ui.DefaultActor("operator"),
		Capabilities:    ui.OfferedCapabilities(),
		Credentials:     credentials,
		SandboxRecreate: registryBackedRecreator{recreations},
	}, w.store)
	pollInterval, maxWorkers := "30s", 1
	githubHost := "github.com"
	if _, err := daemonClient.UpdateSettings(w.ctx, ui.UpdateSettingsRequest{
		PollInterval: &pollInterval, MaxMergesPerRepo: &maxWorkers,
		GitHubHost: &githubHost,
	}); err != nil {
		t.Fatalf("saving baseline deployment settings: %v", err)
	}
	daemonURL := newTestServer(t, accessCodeServer(daemonClient))

	// Configure the remote and capability through the public CLI. No
	// RepoConfig or Grant is seeded behind that surface.
	var cliSurface strings.Builder
	cliSurface.WriteString(runCLI(t, bin, "-server", daemonURL, "repo", "add", "-clone-url", cloneURL, owner+"/"+repoName))
	cliSurface.WriteString(runCLI(t, bin, "-server", daemonURL, "repo", "capabilities", "-set", ambientSSHCapabilityName, owner+"/"+repoName))
	created := runCLI(t, bin, "-json", "-server", daemonURL, "create",
		"-title", "prove ambient SSH survives recreation",
		"-body", "push once, rebuild the sandbox, and push again",
		"-repo", owner+"/"+repoName, "-approve")
	cliSurface.WriteString(created)
	var filed ui.Task
	if err := json.Unmarshal([]byte(created), &filed); err != nil {
		t.Fatalf("parsing grain create output: %v\n%s", err, created)
	}
	if filed.Repo != owner+"/"+repoName {
		t.Fatalf("filed task repo = %q, want Grain identity %s", filed.Repo, repo)
	}
	if !ambientContains(filed.Capabilities, ambientSSHCapabilityName) {
		t.Fatalf("filed task capabilities = %v, want repo-scoped %q", filed.Capabilities, ambientSSHCapabilityName)
	}
	repoConfig, err := w.store.GetRepoConfig(w.ctx, repo)
	if err != nil || repoConfig == nil {
		t.Fatalf("reading CLI-configured repo: %+v, %v", repoConfig, err)
	}
	if !repoConfig.Repo.Same(repo) || repoConfig.CloneURL != cloneURL {
		t.Fatalf("stored repo config = %+v, want identity %s and its custom remote", repoConfig, repo)
	}

	resolver := &exactCredentialResolver{name: ambientSSHCredentialName, material: privateKey}
	provider := ambientSSHCapability{knownHosts: knownHosts}
	sim := githubsim.New(owner, repoName, bareRepo, "main")
	fw := pausedFramework{live: make(chan agent.RunConfig), release: make(chan struct{})}
	deps := orchestrator.Deps{
		Store: w.store, Client: github.NewClient(sim, nil),
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()), MaxWorkers: 1,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) { return fw, nil },
		Config: orchestrator.Config{
			GitRemoteBase:      "http://127.0.0.1:1/unreachable-github-proxy",
			Capabilities:       model.NewCapabilityRegistry(provider),
			Credentials:        resolver,
			SandboxRecreations: recreations,
		},
		// Deliberately nil: no GitHub proxy token exists to restore.
		MintSandboxToken: nil,
	}

	var orchestrationLog bytes.Buffer
	oldLogWriter, oldLogFlags := log.Writer(), log.Flags()
	log.SetOutput(io.MultiWriter(oldLogWriter, &orchestrationLog))
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldLogWriter)
		log.SetFlags(oldLogFlags)
	}()

	var cycleErr error
	cycleDone := make(chan struct{})
	go func() {
		defer close(cycleDone)
		cycleErr = orchestrator.RunCycle(w.ctx, deps, baseTime)
	}()
	var releaseOnce sync.Once
	finishRun := func() {
		releaseOnce.Do(func() { close(fw.release) })
		select {
		case <-cycleDone:
		case <-time.After(2 * time.Minute):
			t.Error("ambient-remote dispatch did not finish within 2 minutes")
		}
	}
	t.Cleanup(finishRun)

	var runConfig agent.RunConfig
	select {
	case runConfig = <-fw.live:
	case <-cycleDone:
		t.Fatalf("cycle ended without dispatching %s: %v", filed.ID, cycleErr)
	case <-time.After(2 * time.Minute):
		t.Fatal("ambient-remote run did not become live within 2 minutes")
	}
	work := filepath.Join(runConfig.SandboxRoot, orchestrator.CheckoutDir)
	// The branch the task was filed with: this deployment's first
	// Settings save gave it the deployment-unique template
	// (model.NewDeploymentConfig, grain/task-3091).
	branch := filed.Branch
	if branch == "" || branch == model.BranchName(filed.ID) {
		t.Fatalf("filed task branch = %q, want the grain/<deployment>/task-%s a new deployment names", branch, filed.ID)
	}
	if got := strings.TrimSpace(runOutput(t, work, "git", "remote", "get-url", "origin")); got != cloneURL {
		t.Fatalf("initial checkout origin = %q, want configured SSH remote", got)
	}
	if got := strings.TrimSpace(runOutput(t, work, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != branch {
		t.Fatalf("initial checkout branch = %q, want %q", got, branch)
	}

	p := startMCPServer(t, bin, "-sandbox-root", runConfig.SandboxRoot, "-server", daemonURL, "-task", filed.ID)
	statusResult, err := p.CallTool(context.Background(), "update_status", map[string]any{"status": "pushing through the scoped SSH remote"})
	if err != nil {
		t.Fatalf("updating live status: %v", err)
	}
	if statusResult.IsError {
		t.Fatalf("updating live status: %s", statusResult.Text())
	}
	activity, err := w.store.TaskActivityOf(w.ctx, filed.ID)
	if err != nil || activity == nil || activity.Note != "pushing through the scoped SSH remote" {
		t.Fatalf("live task activity = %+v, %v", activity, err)
	}

	firstPush := callAmbientRunCommand(t, p, "cd work && echo before > ambient-before.txt && git add ambient-before.txt && git commit -q -m 'push before sandbox rebuild' && git push -q origin "+branch)
	if sshGit.commandCount("git-upload-pack") == 0 || sshGit.commandCount("git-receive-pack") == 0 {
		t.Fatalf("direct SSH clone/push were not both observed: %v", sshGit.commandSnapshot())
	}

	recreated := callRecreateSandbox(t, p)
	if recreated.IsError {
		t.Fatalf("recreate_sandbox failed: %s\nstderr:\n%s", recreated.Text(), p.stderr.String())
	}
	if strings.Contains(recreated.Text(), "could not put back") {
		t.Fatalf("recreate_sandbox returned warnings: %s", recreated.Text())
	}
	if !strings.Contains(recreated.Text(), "the files this task's capabilities place in the sandbox") {
		t.Fatalf("recreate report did not confirm restoration of the scoped credential: %s", recreated.Text())
	}
	if strings.Contains(recreated.Text(), "git proxy") {
		t.Fatalf("recreate report says it restored a GitHub proxy credential this repo never had: %s", recreated.Text())
	}
	if got := strings.TrimSpace(runOutput(t, work, "git", "remote", "get-url", "origin")); got != cloneURL {
		t.Fatalf("rebuilt checkout origin = %q, want configured SSH remote", got)
	}
	if _, err := os.Stat(filepath.Join(work, "ambient-before.txt")); err != nil {
		t.Fatalf("rebuilt checkout lost the commit pushed before recreation: %v", err)
	}
	secondPush := callAmbientRunCommand(t, p, "cd work && echo after > ambient-after.txt && git add ambient-after.txt && git commit -q -m 'push after sandbox rebuild' && git push -q origin "+branch)
	if sshGit.commandCount("git-upload-pack") < 2 || sshGit.commandCount("git-receive-pack") < 2 {
		t.Fatalf("SSH service calls after rebuild = %v, want two clones and two pushes", sshGit.commandSnapshot())
	}
	if got := resolver.requests(); len(got) != 1 || got[0] != ambientSSHCredentialName {
		t.Fatalf("credential resolver requests = %v, want one scoped materialization", got)
	}

	finishRun()
	if cycleErr != nil {
		t.Fatalf("RunCycle: %v", cycleErr)
	}
	if len(sim.PullRequests) != 1 || sim.PullRequests[0].Head != branch {
		t.Fatalf("pull requests = %+v, want one for %s", sim.PullRequests, branch)
	}
	if task, err := w.store.GetTask(w.ctx, filed.ID); err != nil || task == nil ||
		task.Target == nil || !task.Target.Same(repo) {
		t.Fatalf("task identity before close-out = %+v, %v; want %s", task, err, repo)
	}

	// Stand in for a reviewer closing the PR, then let normal sync close Grain's
	// task as well.
	sim.PullRequests[0].State = "closed"
	if err := orchestrator.RunCycle(w.ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle (pull-request close-out): %v", err)
	}
	if state, err := w.store.State(w.ctx, filed.ID); err != nil || state != model.StateClosed {
		t.Fatalf("state after pull-request close-out = %q, %v; want closed", state, err)
	}

	runs, err := w.store.Runs(w.ctx, filed.ID)
	if err != nil {
		t.Fatalf("reading persisted runs for leak check: %v", err)
	}
	pullRequestRecord, err := json.Marshal(sim.PullRequests)
	if err != nil {
		t.Fatalf("encoding pull-request records: %v", err)
	}
	runRecords, err := json.Marshal(runs)
	if err != nil {
		t.Fatalf("encoding run records: %v", err)
	}
	surfaces := strings.Join([]string{
		cliSurface.String(), statusResult.Text(), activity.Note,
		firstPush, recreated.Text(), secondPush, p.stderr.String(),
		orchestrationLog.String(), string(pullRequestRecord), string(runRecords),
	}, "\n")
	for label, forbidden := range map[string]string{
		"custom clone URL": cloneURL,
		"SSH private key":  privateKey,
		"SSH key material": secretNeedle,
	} {
		if strings.Contains(surfaces, forbidden) {
			t.Errorf("%s leaked into a status, log, warning, run, or pull-request record", label)
		}
	}
}

func ambientContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
