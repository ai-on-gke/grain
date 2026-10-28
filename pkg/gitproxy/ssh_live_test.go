package gitproxy_test

// SSH key credentials end to end: a real git client cloning, fetching
// and pushing through a real GitProxy whose team credential is an SSH
// key, against an in-process SSH server running git-upload-pack and
// git-receive-pack on bare repositories -- the stand-in for an SSH git
// host the way gitHTTPBackend stands in for GitHub's smart HTTP.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// sshGitServer serves root's bare repositories over SSH to one client key.
type sshGitServer struct {
	addr    string
	hostKey ssh.PublicKey
	mu      sync.Mutex
	execs   []string
}

func (s *sshGitServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

func startSSHGitServer(t *testing.T, root string, client ssh.PublicKey) *sshGitServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(client.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	config.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &sshGitServer{addr: ln.Addr().String(), hostKey: hostSigner.PublicKey()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn, config, root)
		}
	}()
	return srv
}

func (s *sshGitServer) serve(conn net.Conn, config *ssh.ServerConfig, root string) {
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			var env []string
			for req := range chReqs {
				switch req.Type {
				case "env":
					var kv struct{ Name, Value string }
					if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" {
						env = append(env, "GIT_PROTOCOL="+kv.Value)
					}
					req.Reply(true, nil)
				case "exec":
					var p struct{ Command string }
					if ssh.Unmarshal(req.Payload, &p) != nil {
						req.Reply(false, nil)
						continue
					}
					req.Reply(true, nil)
					s.mu.Lock()
					s.execs = append(s.execs, p.Command)
					s.mu.Unlock()
					go s.exec(ch, p.Command, env, root)
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

func (s *sshGitServer) exec(ch ssh.Channel, command string, env []string, root string) {
	defer ch.Close()
	service, quoted, _ := strings.Cut(command, " ")
	path := strings.Trim(quoted, "'")
	status := uint32(0)
	if service != "git-upload-pack" && service != "git-receive-pack" || strings.Contains(path, "..") {
		fmt.Fprintf(ch.Stderr(), "unsupported command %q\n", command)
		status = 1
	} else {
		cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), filepath.Join(root, path))
		cmd.Env = append(os.Environ(), env...)
		stdin, _ := cmd.StdinPipe()
		cmd.Stdout = ch
		cmd.Stderr = ch.Stderr()
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(ch.Stderr(), err)
			status = 1
		} else {
			go func() {
				io.Copy(stdin, ch)
				stdin.Close()
			}()
			if err := cmd.Wait(); err != nil {
				status = 128
			}
		}
	}
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

// sshClientKey is a fresh ed25519 key, as the OpenSSH PEM a person
// pastes into the credential editor, and its public half.
func sshClientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), sshPub
}

func knownHostsLine(addr string, key ssh.PublicKey) string {
	host, port, _ := net.SplitHostPort(addr)
	return fmt.Sprintf("[%s]:%s %s", host, port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
}

// sshProxyRig is a GitProxy whose default team clones and pushes with an
// SSH key, and task t1 targeting owner/widgets on sandbox-0.
type sshProxyRig struct {
	upstream string
	server   *sshGitServer
	remote   func(repo string) string
	root     string
	creds    *gitproxy.CredentialSet
	audit    *recordingAudit
}

type recordingAudit struct {
	mu      sync.Mutex
	entries []gitproxy.AuditEntry
}

func (a *recordingAudit) Record(e gitproxy.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *recordingAudit) outcomes() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.entries {
		out = append(out, e.Repo+" "+e.Action+" "+e.Credential+": "+e.Outcome)
	}
	return strings.Join(out, "\n")
}

func newSSHProxyRig(t *testing.T, knownHosts func(srv *sshGitServer) string) *sshProxyRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	upstream := t.TempDir()
	newBareRepoWithOneCommit(t, upstream, "owner", "widgets")
	newBareRepoWithOneCommit(t, upstream, "owner", "other-repo")
	privPEM, pub := sshClientKey(t)
	srv := startSSHGitServer(t, upstream, pub)

	store, ctx := openStore(t)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "test task",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "widgets"},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "t1-1", TaskID: "t1", Sandbox: "sandbox-0", Attempt: 1, StartedAt: time.Now()},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	secrets := filepath.Join(dataDir, "secrets", "github")
	mustWriteFile(t, filepath.Join(secrets, "credentials.json"), `{}`)
	creds, err := gitproxy.LoadCredentialSet(secrets)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(srv.addr)
	var portNum int
	fmt.Sscan(port, &portNum)
	if err := creds.SetSSHKey("team-ssh", gitproxy.SSHKeyFile{
		Host: "127.0.0.1", Port: portNum, PrivateKey: privPEM, KnownHosts: knownHosts(srv),
	}, model.GitHostGit); err != nil {
		t.Fatalf("saving the SSH key: %v", err)
	}
	giveTeamCredential(t, store, ctx, model.DefaultTeamID, "team-ssh")
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"), `{"sandbox-0": "ssh-sandbox-token"}`)

	audit := &recordingAudit{}
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: "127.0.0.1", ForwardTLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy.Audit = audit
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxyServer.Close)
	root := t.TempDir()
	remote := func(repo string) string { return proxyServer.URL + "/owner/" + repo + ".git" }
	if err := mcp.ConfigureGitCredentials(root, remote("widgets"), "ssh-sandbox-token", mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	return &sshProxyRig{upstream: upstream, server: srv, remote: remote, root: root, creds: creds, audit: audit}
}

// sandboxGit runs git as the sandbox would, in the rig's root.
func (r *sshProxyRig) sandboxGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Join(r.root, dir)
	cmd.Env = append(os.Environ(), "HOME="+r.root, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSSHCredentialClonesFetchesAndPushes(t *testing.T) {
	for _, version := range []string{"2", "0"} {
		t.Run("protocol v"+version, func(t *testing.T) {
			r := newSSHProxyRig(t, func(srv *sshGitServer) string { return knownHostsLine(srv.addr, srv.hostKey) })
			proto := "protocol.version=" + version
			if out, err := r.sandboxGit(t, "", "-c", proto, "clone", r.remote("widgets"), "work"); err != nil {
				t.Fatalf("clone over an SSH credential: %v\n%s\n%s", err, out, r.audit.outcomes())
			}
			if out, _ := r.sandboxGit(t, "work", "log", "-1", "--format=%s"); strings.TrimSpace(out) != "initial commit" {
				t.Fatalf("cloned tip = %q", out)
			}

			// Upstream moves on; a fetch picks it up through the proxy.
			seed := filepath.Join(r.upstream, "widgets-seed")
			mustWriteFile(t, filepath.Join(seed, "NEWS"), "news\n")
			run(t, seed, "git", "add", "NEWS")
			run(t, seed, "git", "commit", "-q", "-m", "upstream commit")
			run(t, seed, "git", "push", "-q", "origin", "main")
			if out, err := r.sandboxGit(t, "work", "-c", proto, "fetch", "origin"); err != nil {
				t.Fatalf("fetch over an SSH credential: %v\n%s", err, out)
			}
			if out, _ := r.sandboxGit(t, "work", "log", "-1", "--format=%s", "origin/main"); strings.TrimSpace(out) != "upstream commit" {
				t.Fatalf("fetched origin/main = %q", out)
			}

			// The task's own branch is pushed over SSH.
			r.sandboxGit(t, "work", "config", "user.email", "agent@example.com")
			r.sandboxGit(t, "work", "config", "user.name", "agent")
			mustWriteFile(t, filepath.Join(r.root, "work", "README.md"), "agent was here\n")
			r.sandboxGit(t, "work", "commit", "-qam", "agent commit")
			if out, err := r.sandboxGit(t, "work", "-c", proto, "push", "origin", "HEAD:grain/task-t1"); err != nil {
				t.Fatalf("push over an SSH credential: %v\n%s\n%s", err, out, r.audit.outcomes())
			}
			gitDir := filepath.Join(r.upstream, "owner", "widgets.git")
			if out := runOutput(t, r.upstream, "git", "--git-dir", gitDir, "log", "-1", "--format=%s", "grain/task-t1"); strings.TrimSpace(out) != "agent commit" {
				t.Errorf("upstream grain/task-t1 = %q, want the agent's commit", out)
			}

			// Ref policy: the default branch is refused by the proxy, and
			// nothing reaches the SSH host for it.
			out, err := r.sandboxGit(t, "work", "-c", proto, "push", "--force", "origin", "HEAD:main")
			if err == nil || !strings.Contains(out, "git push origin grain/task-t1") {
				t.Errorf("pushing main over an SSH credential: err=%v\n%s\nwant the proxy's ref refusal", err, out)
			}
			if out := runOutput(t, r.upstream, "git", "--git-dir", gitDir, "log", "-1", "--format=%s", "main"); strings.TrimSpace(out) == "agent commit" {
				t.Error("upstream main has the agent's commit: a sandbox pushed the default branch over SSH")
			}

			audit := r.audit.outcomes()
			if !strings.Contains(audit, "team-ssh: forwarded over ssh: 200") || !strings.Contains(audit, "denied: ref not in scope") {
				t.Errorf("audit log:\n%s\nwant SSH forwards under team-ssh and the ref refusal", audit)
			}
		})
	}
}

func TestSSHCredentialRefusesReposOutsideTheReadScope(t *testing.T) {
	r := newSSHProxyRig(t, func(srv *sshGitServer) string { return knownHostsLine(srv.addr, srv.hostKey) })
	out, err := r.sandboxGit(t, "", "clone", r.remote("other-repo"), "other")
	if err == nil || !strings.Contains(out, "not in scope for this sandbox") {
		t.Fatalf("cloning an out-of-scope repo over SSH: err=%v\n%s", err, out)
	}
	if cmds := r.server.commands(); len(cmds) != 0 {
		t.Errorf("the SSH host was asked %v for a repo the sandbox may not read", cmds)
	}
}

func TestSSHCredentialRefusesAnUnpinnedHostKey(t *testing.T) {
	_, other := sshClientKey(t)
	r := newSSHProxyRig(t, func(srv *sshGitServer) string { return knownHostsLine(srv.addr, other) })
	out, err := r.sandboxGit(t, "", "clone", r.remote("widgets"), "work")
	if err == nil || !strings.Contains(out, "not one pinned for it") {
		t.Fatalf("cloning from a host presenting an unpinned key: err=%v\n%s", err, out)
	}
	if cmds := r.server.commands(); len(cmds) != 0 {
		t.Errorf("commands ran on a host whose key was not pinned: %v", cmds)
	}
}

func TestSSHCredentialPassesTheHostsRefusalThrough(t *testing.T) {
	r := newSSHProxyRig(t, func(srv *sshGitServer) string { return knownHostsLine(srv.addr, srv.hostKey) })
	os.RemoveAll(filepath.Join(r.upstream, "owner", "widgets.git"))
	out, err := r.sandboxGit(t, "", "clone", r.remote("widgets"), "work")
	if err == nil || !strings.Contains(out, `SSH credential "team-ssh"`) {
		t.Fatalf("cloning a repository the SSH host does not have: err=%v\n%s", err, out)
	}
}
