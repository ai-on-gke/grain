package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ContainerNamePrefix is prepended to a sandbox's own name to make its
// container name. ReapOrphans goes by the grain.sandbox label rather
// than by this prefix.
const ContainerNamePrefix = "grain-sandbox-"

// DefaultContainerNetwork is the Docker network sandbox containers are
// attached to when ContainerConfig.Network is left empty. It is a bridge
// of grain's own, created on first use with inter-container traffic
// disabled, so one run's container cannot reach another's -- nor
// anything else a host runs on Docker's shared default bridge. Egress to
// the internet and to the host itself (where grain's git proxy and
// package cache listen) is unaffected.
const DefaultContainerNetwork = "grain-sandboxes"

// DefaultContainerPidsLimit caps the processes one sandbox container may
// hold, so a fork bomb in task code exhausts its own container rather
// than the host's pid space. A build with a parallel toolchain comes
// nowhere near it.
const DefaultContainerPidsLimit = 4096

// ContainerConfig is what ContainerSandboxes needs to run one docker
// container per run on the local docker daemon: the docker binary
// ("docker" when empty), the sandbox image and workspace (both
// required), and the deployment-wide default shape a run that asked for
// none falls back to.
type ContainerConfig struct {
	Docker, Image, Workspace                    string
	DefaultCPUs, DefaultMemoryMB, DefaultDiskGB int
	DefaultNetworkAccess                        model.NetworkAccess
	// Network is the Docker network sandbox containers join. Empty means
	// DefaultContainerNetwork, which is created if it does not exist; any
	// other name is used as given and must already exist ("bridge" is
	// Docker's shared default, and what this backend used before it had
	// a network of its own).
	Network string
	// CapAdd names Linux capabilities handed back after every one is
	// dropped. The stock sandbox image runs as an unprivileged user and
	// needs none; this is for a custom image that genuinely does.
	CapAdd []string
	// PidsLimit caps the processes in one container; 0 means
	// DefaultContainerPidsLimit and a negative value means no limit.
	PidsLimit int
	// GrainSourceDir is the deployment's baked copy of grain's source
	// (orchestrator.Config.GrainSourceDir), as this process sees it. A
	// sandbox acquired with Shape.GrainSource has it mounted read-only
	// from a per-revision volume (grainSourceVolume); empty means none
	// is, and placeGrainSource streams a copy in as it does elsewhere.
	GrainSourceDir string
}

// ContainerSandboxes builds one docker container per run and removes it
// when the run is done. The isolation is a container's: a run shares the
// host's kernel, and it cannot be given model.NetworkNone (see Acquire).
type ContainerSandboxes struct {
	cfg      ContainerConfig
	netMu    sync.Mutex
	netReady bool
	mu       sync.Mutex
	// image is SetImage's replacement for cfg.Image (live_image.go).
	image    liveImage
	live     map[string]*containerSandbox
	building map[string]bool
	defaults Shape
	// srcMu serializes filling grain-src volumes, and srcReady names the
	// ones this process has already seen filled.
	srcMu    sync.Mutex
	srcReady map[string]bool
}

// NewContainerSandboxes returns a ContainerSandboxes configured by cfg.
func NewContainerSandboxes(cfg ContainerConfig) *ContainerSandboxes {
	return &ContainerSandboxes{cfg: cfg, live: map[string]*containerSandbox{}, building: map[string]bool{}, srcReady: map[string]bool{}, defaults: Shape{
		CPUs: cfg.DefaultCPUs, MemoryMB: cfg.DefaultMemoryMB, DiskGB: cfg.DefaultDiskGB,
		NetworkAccess: cfg.DefaultNetworkAccess,
	}}
}

func (c *ContainerSandboxes) docker() string {
	if c.cfg.Docker != "" {
		return c.cfg.Docker
	}
	return "docker"
}

func (c *ContainerSandboxes) network() string {
	if c.cfg.Network != "" {
		return c.cfg.Network
	}
	return DefaultContainerNetwork
}

// IsolatesNetwork reports false: sandbox containers are kept apart from
// each other, but none of them can be refused the internet while still
// reaching grain's git proxy -- see Acquire.
func (c *ContainerSandboxes) IsolatesNetwork() bool { return false }

// SetDefaultShape replaces the deployment-wide default the next Acquire
// falls back to -- GranuleSandboxes.SetDefaultShape's counterpart.
func (c *ContainerSandboxes) SetDefaultShape(shape Shape) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaults = shape
}

// SetImage implements SandboxImageSetter: the image the next sandbox is
// created from.
func (c *ContainerSandboxes) SetImage(image string) { c.image.set(image) }

// CurrentImage implements SandboxImageSetter.
func (c *ContainerSandboxes) CurrentImage() string { return c.image.get(c.cfg.Image) }

// DefaultShape reads back the deployment-wide default container shape
// currently in effect on this backend.
func (c *ContainerSandboxes) DefaultShape() Shape {
	return c.defaultShape()
}

func (c *ContainerSandboxes) defaultShape() Shape {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.defaults
}

// SandboxCount returns the number of sandbox containers currently held by
// live runs without invoking Docker to inspect any of them.
func (c *ContainerSandboxes) SandboxCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.live)
}

func (c *ContainerSandboxes) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.docker(), args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// ensureNetwork creates DefaultContainerNetwork the first time a sandbox
// needs it. A network named in ContainerConfig.Network is the operator's
// and is never created or altered here.
func (c *ContainerSandboxes) ensureNetwork(ctx context.Context) error {
	if c.cfg.Network != "" {
		return nil
	}
	c.netMu.Lock()
	defer c.netMu.Unlock()
	if c.netReady {
		return nil
	}
	if _, err := c.run(ctx, "network", "inspect", DefaultContainerNetwork); err != nil {
		if _, err := c.run(ctx, DefaultContainerNetworkCreateArgs()...); err != nil {
			// Two daemons sharing one Docker may race to create it.
			if _, inspectErr := c.run(ctx, "network", "inspect", DefaultContainerNetwork); inspectErr != nil {
				return fmt.Errorf("orchestrator: creating sandbox network %q: %w", DefaultContainerNetwork, err)
			}
		}
	}
	c.netReady = true
	return nil
}

// runArgs is the `docker run` argument list for one sandbox container.
// Beyond the shape's resource limits it drops every capability, forbids
// gaining privileges through setuid binaries, caps the process count and
// joins the sandbox network, so task code gets the container boundary
// and nothing the Docker daemon would otherwise hand it by default.
// srcVolume, when not empty, is a grain-src volume mounted read-only
// (grainSourceMountArgs) -- for a self-debug run's sandbox only.
func (c *ContainerSandboxes) runArgs(name string, resolved Shape, srcVolume string) []string {
	args := []string{"run", "-d", "--name", name, "--label", "grain.sandbox=true",
		"--cpus", strconv.Itoa(resolved.CPUs), "--memory", strconv.Itoa(resolved.MemoryMB) + "m",
		"--memory-swap", strconv.Itoa(resolved.MemoryMB) + "m",
		// First in line for the OOM killer, ahead of the daemon and hostd
		// (which run strongly negative): under auto overcommit a
		// sandbox is what is sacrificed, and retried.
		"--oom-score-adj", "500",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", c.network()}
	for _, capability := range c.cfg.CapAdd {
		if capability = strings.TrimSpace(capability); capability != "" {
			args = append(args, "--cap-add", capability)
		}
	}
	switch {
	case c.cfg.PidsLimit == 0:
		args = append(args, "--pids-limit", strconv.Itoa(DefaultContainerPidsLimit))
	case c.cfg.PidsLimit > 0:
		args = append(args, "--pids-limit", strconv.Itoa(c.cfg.PidsLimit))
	}
	if srcVolume != "" {
		args = append(args, grainSourceMountArgs(srcVolume)...)
	}
	return append(args, "--workdir", c.cfg.Workspace, c.CurrentImage(), "sleep", "infinity")
}

func (c *ContainerSandboxes) create(ctx context.Context, name string, shape Shape, srcVolume string) error {
	_, _ = c.run(context.Background(), "rm", "-f", name)
	if err := c.ensureNetwork(ctx); err != nil {
		return err
	}
	args := c.runArgs(name, shape.orDefault(DefaultShape()), srcVolume)
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("orchestrator: creating sandbox container %q: %w", name, err)
	}
	return nil
}

// Acquire implements Sandboxes: replace whatever container already
// carries this run's name with a fresh one and hand back a handle that
// removes it on Release. It refuses model.NetworkNone, since a container
// on a docker bridge cannot be kept off the internet while still reaching
// grain's git proxy on the host.
func (c *ContainerSandboxes) Acquire(ctx context.Context, sandbox string, shape Shape) (Sandbox, error) {
	if c.CurrentImage() == "" {
		return nil, errors.New("orchestrator: container sandbox image is required")
	}
	if c.cfg.Workspace == "" {
		return nil, errors.New("orchestrator: container sandbox workspace is required")
	}
	resolved := shape.orDefault(c.defaultShape())
	if resolved.NetworkAccess == model.NetworkNone {
		return nil, fmt.Errorf("orchestrator: sandbox %q asks for no network access, but the local container backend cannot withhold the internet while retaining access to grain's git proxy", sandbox)
	}
	// Only a self-debug run's sandbox gets grain's source mounted, and
	// failing to provide it is never a failed Acquire: placeGrainSource
	// streams a copy into a sandbox that has none.
	var srcVolume, srcRevision string
	if shape.GrainSource {
		var err error
		if srcVolume, srcRevision, err = c.grainSourceVolume(ctx); err != nil {
			log.Printf("orchestrator: sandbox %q: mounting grain's baked source: %v; it will be copied in instead", sandbox, err)
		}
	}
	name := ContainerNamePrefix + sandbox
	c.mu.Lock()
	if c.building == nil {
		c.building = map[string]bool{}
	}
	c.building[name] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.building, name)
		c.mu.Unlock()
	}()
	if err := c.create(ctx, name, resolved, srcVolume); err != nil {
		// A `docker run -d` that fails or is interrupted can still leave
		// the container behind -- the daemon carries on creating it after
		// the CLI is killed -- with nothing holding a handle to release.
		c.removeQuietly(ctx, name)
		return nil, err
	}
	sb := &containerSandbox{owner: c, name: sandbox, container: name, shape: shape,
		srcVolume: srcVolume, srcRevision: srcRevision, startedAt: time.Now(),
		runner: &mcp.ContainerExecRunner{Docker: c.cfg.Docker, Container: name}}
	c.mu.Lock()
	c.live[sandbox] = sb
	c.mu.Unlock()
	return sb, nil
}

// containerCleanupTimeout bounds a `docker rm -f` run for a caller that
// is already on its way out, so a wedged docker daemon cannot pin it.
const containerCleanupTimeout = 30 * time.Second

// removeQuietly force-removes a container that never became usable,
// dropping the error: the caller is already failing for a reason it can
// act on, and ReapOrphans catches whatever this misses at the next
// startup. It runs detached from ctx's cancellation, since an interrupted
// Acquire is the ordinary way to get here and exec.CommandContext on a
// cancelled context would not run docker at all.
func (c *ContainerSandboxes) removeQuietly(ctx context.Context, name string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerCleanupTimeout)
	defer cancel()
	_, _ = c.run(cleanup, "rm", "-f", name)
}

// ReapOrphans removes every container carrying the grain.sandbox label
// that this process is not itself holding, and reports how many it
// removed. A daemon calls it once, at startup, before anything can
// dispatch. The label is shared by every daemon on one docker host, so
// deployments sharing a docker daemon will reap each other's sandboxes.
func (c *ContainerSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	out, err := c.run(ctx, "ps", "-aq", "--filter", "label=grain.sandbox=true")
	if err != nil {
		return 0, fmt.Errorf("orchestrator: listing sandbox containers: %w", err)
	}
	c.mu.Lock()
	live := map[string]bool{}
	for _, sb := range c.live {
		live[sb.container] = true
	}
	for name := range c.building {
		live[name] = true
	}
	c.mu.Unlock()
	var count int
	var errs []error
	for _, id := range strings.Fields(out) {
		name, err := c.run(ctx, "inspect", "-f", "{{.Name}}", id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if live[strings.TrimPrefix(strings.TrimSpace(name), "/")] {
			continue
		}
		if _, err := c.run(ctx, "rm", "-f", id); err != nil {
			errs = append(errs, err)
		} else {
			count++
		}
	}
	return count, errors.Join(errs...)
}

type containerSandbox struct {
	owner           *ContainerSandboxes
	name, container string
	shape           Shape
	// srcVolume is the grain-src volume this container was created with
	// mounted, "" for none, and srcRevision the revision it holds.
	srcVolume, srcRevision string
	runner                 sandboxRunner
	// startedAt is when this process acquired (or readopted) the
	// sandbox -- SandboxHealth.StartedAt.
	startedAt time.Time
	// root is the runner RunAsRootInSandbox uses; nil means a `docker
	// exec -u 0` into container. Tests set it to a fake.
	root sandboxRunner
}

func (s *containerSandbox) Name() string          { return s.name }
func (s *containerSandbox) ContainerName() string { return s.container }
func (s *containerSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	return mcp.NewSSHSandboxTools(s.runner, s.owner.cfg.Workspace), nil
}
func (s *containerSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	return mcp.ConfigureGitCredentialsOverSSH(s.runner, remoteURL, token, identity)
}
func (s *containerSandbox) PlaceFile(ctx context.Context, path, content, mode string) error {
	return mcp.PlaceFileOverSSH(ctx, s.runner, path, content, mode)
}

// RunInSandbox implements SandboxCommander -- see that interface's own
// doc comment: the controller's own route to a command inside this
// container, which is what writing the package-cache configuration into
// /etc needs.
func (s *containerSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.runner.Run(ctx, argv, stdin)
}

// RunAsRootInSandbox implements SandboxRootCommander: the same `docker
// exec`, as uid 0. The stock image runs as an unprivileged user with no
// sudo, so this is the only way the package-cache writes into /etc can
// happen here at all. It is the controller's route and never a tool's:
// a run's own commands keep the image's user. Root here is still root in
// a container started with --cap-drop ALL and no-new-privileges, owning
// the container's own /etc and nothing else.
func (s *containerSandbox) RunAsRootInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.rootRunner().Run(ctx, argv, stdin)
}

func (s *containerSandbox) rootRunner() sandboxRunner {
	if s.root != nil {
		return s.root
	}
	return &mcp.ContainerExecRunner{Docker: s.owner.cfg.Docker, Container: s.container, User: "0"}
}

// Rebuild implements SandboxRebuilder: remove this run's container and
// start a fresh one under the same name and shape.
func (s *containerSandbox) Rebuild(ctx context.Context) error {
	return s.owner.create(ctx, s.container, s.shape.orDefault(s.owner.defaultShape()), s.srcVolume)
}

// Release removes the container, on a context detached from ctx's
// cancellation so a run ended by a closed task or a stopping daemon does
// not leave it behind.
func (s *containerSandbox) Release(ctx context.Context) error {
	s.owner.mu.Lock()
	delete(s.owner.live, s.name)
	s.owner.mu.Unlock()
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerCleanupTimeout)
	defer cancel()
	if _, err := s.owner.run(cleanup, "rm", "-f", s.container); err != nil {
		return fmt.Errorf("orchestrator: deleting sandbox container %q: %w", s.container, err)
	}
	return nil
}

// Readopt implements SandboxReadopter: the container a previous process
// started for sandbox, claimed so ReapOrphans leaves it running.
func (c *ContainerSandboxes) Readopt(ctx context.Context, sandbox string) (Sandbox, error) {
	name := ContainerNamePrefix + sandbox
	if _, err := c.run(ctx, "inspect", "--type", "container", "--format", "{{.Name}}", name); err != nil {
		return nil, fmt.Errorf("orchestrator: readopting sandbox container %q: %w", name, err)
	}
	sb := &containerSandbox{owner: c, name: sandbox, container: name, startedAt: time.Now(),
		runner: &mcp.ContainerExecRunner{Docker: c.cfg.Docker, Container: name}}
	c.mu.Lock()
	c.live[sandbox] = sb
	c.mu.Unlock()
	return sb, nil
}
