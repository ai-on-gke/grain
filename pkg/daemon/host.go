package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/grainlog"
	"github.com/bwsalmon/grain/pkg/hosttop"
	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/sysstat"
	"github.com/bwsalmon/grain/pkg/systemlog"
	"github.com/bwsalmon/grain/pkg/ui"
	"github.com/bwsalmon/grain/pkg/upgrade"
)

// hostAction passes a host-power action through on a deployment that
// owns a host, and withholds it on one that does not -- see
// kubernetesMode. nil is what ui.Config's own contract wants for
// "unavailable here": the frontend hides the button rather than
// offering one that can only 404.
func HostAction(cfg Config, action func(ctx context.Context) error) func(ctx context.Context) error {
	if KubernetesMode(cfg) {
		return nil
	}
	return action
}

// hostStatsOrNil and hostTopOrNil are hostAction's equivalents for the
// host *readings* -- two funcs rather than one generic because each has
// its own signature, and the reason each is withheld differs enough to
// be worth writing at the field it fills.
func HostStatsOrNil(cfg Config, disks []HostDisk, floor func() model.DiskFloor) func() (ui.HostPressure, error) {
	if KubernetesMode(cfg) {
		return nil
	}
	// The floor travels with the disks so the pane can draw it on the bar
	// it applies to: it is a share of each volume rather than one number
	// for the deployment, and it is what "how much of this disk is
	// actually available to a job" means (ui.DiskUsage.FloorMB).
	//
	// Asked for per poll rather than resolved once here, since it is a
	// stored setting the daemon re-reads each reconcile tick: a pane
	// still drawing the floor this process started with would be showing
	// an operator the line they had just moved (liveConfig.diskFloor,
	// grain/task-372). nil -- a caller with nothing live to ask -- gets
	// the configuration it handed in.
	if floor == nil {
		floor = cfg.DiskFloor
	}
	return func() (ui.HostPressure, error) { return hostStats(disks, floor()) }
}

// hostVolumesOrNil is ui.Config.SandboxVolumes: the same filesystems
// hostStats above reports, with a floor the *caller* names resolved
// against each of them.
//
// It exists so the Settings pane can show what the floor an operator is
// editing comes to on this deployment's own volumes -- a share means
// nothing until it is applied to a disk, and 10% is 44 GB on the sandbox
// volume and its 5 GiB minimum on the store disk beside it. The floor is
// an argument rather than read from the live configuration because the
// pane asks about the row it is showing, which for a setting just saved
// is a tick ahead of what this daemon has applied.
//
// nil on the same deployments HostStats is: under Kubernetes there is no
// host of this deployment's own to report, and with no volumes resolved
// there is nothing to say.
func HostVolumesOrNil(cfg Config, disks []HostDisk) func(model.DiskFloor) []ui.DiskUsage {
	if KubernetesMode(cfg) || len(disks) == 0 {
		return nil
	}
	return func(floor model.DiskFloor) []ui.DiskUsage { return diskUsage(disks, floor) }
}

// logSources is ui.Config.Logs: the named log streams the System pane's
// Logs tab offers. Which of them exist is the same question
// hostStatsOrNil and hostAction above are asking -- does this process
// have a host of its own? -- because three of the four a host
// deployment shows are readings of that host (grain/task-488).
//
// On a host, all four: "daemon" reads back this same process's own
// journal (it always runs as grain-daemon.service -- scripts/setup.sh's
// own unit -- under a real deployment); "git-proxy-audit" reads the
// audit log startGitProxy's own gitproxy.BuildProxy just wrote alongside
// it, in the same DataDir; "config-sync" reads the rollout loop that
// deployed this process in the first place -- terraform/gcp/
// files/config-sync.sh, installed as grain-config-sync.service by that
// same terraform's startup.sh; "dmesg" reads the kernel log of the
// machine underneath all three, which is where the failures none of them
// can report land -- an agent CLI killed by the OOM killer writes
// nothing about it to the daemon's own journal (pkg/systemlog.Dmesg).
// All four are colocated with this process by construction under a real
// host deployment (v2 runs the daemon directly on the host that
// config-sync watches, unlike v1's nested guest -- deploy.sh's own
// ensure_ops_agent comment) -- unlike Secrets/Credentials above, there
// is no case there where this deployment has one but not the others
// (bwsalmon/agents#444, bwsalmon/agents#542).
//
// Under Kubernetes, two: three of those four cannot answer at all.
// There is no journalctl in grain's image and no grain-daemon.service to
// read with it -- the daemon is a container of a Deployment -- and
// nothing in this shape is deployed by config-sync, so
// grain-config-sync.service does not exist either. dmesg would be the
// *node's* kernel log, describing a machine shared with workloads that
// are nothing to do with grain, if an unprivileged container could read
// it at all. Wiring them would put three sources in the tab that can
// only error. "daemon" is served by `kubectl logs` against this pod
// instead (systemlog.Pod), which is the same log and the one
// deploy/kubernetes/README.md tells an operator to read; and
// "git-proxy-audit" is unchanged, since the proxy writes that file under
// -data-dir, which here is the pod's own volume.
//
// A pod that was not told its own name ($GRAIN_POD_NAME) gets no
// "daemon" source rather than one aimed at a pod guessed by label or
// hostname -- the same refusal daemonHealthFor makes for the restart
// count, and the pane beside the tab says why.
func LogSources(cfg Config) map[string]ui.LogSource {
	auditLog := systemlog.File{Path: filepath.Join(cfg.DataDir, "state", "git-proxy", "audit.log")}
	if KubernetesMode(cfg) {
		sources := map[string]ui.LogSource{"git-proxy-audit": auditLog}
		if pod := ownPodName(); pod != "" {
			_, container := K8sSelfNames(cfg)
			sources["daemon"] = systemlog.Pod{
				Pods:      k8s.Client{Kubectl: cfg.K8sKubectl, Kubeconfig: EffectiveK8sKubeconfig(cfg), Namespace: cfg.K8sNamespace},
				Name:      pod,
				Container: container,
			}
		}
		return sources
	}
	return map[string]ui.LogSource{
		"daemon":          systemlog.Journalctl{Unit: "grain-daemon.service"},
		"git-proxy-audit": auditLog,
		"config-sync":     systemlog.Journalctl{Unit: "grain-config-sync.service"},
		"dmesg":           systemlog.Dmesg{},
	}
}

// logRedactor is ui.Config.LogRedactor: the same redaction list
// read_grain_log answers with (grainLogRedactor), built per request so a
// secret set since this process started is redacted from the next read.
// The Logs tab serves this same host's journal to a browser, so the
// same refusal applies when the list cannot be built: an error, never
// the lines as they are.
func logRedactor(secretsDir string) func() (*grainlog.Redactor, error) {
	return func() (*grainlog.Redactor, error) {
		if r := grainLogRedactor(secretsDir); r != nil {
			return r, nil
		}
		return nil, errors.New("this host's secrets could not be read (see the daemon's own log)")
	}
}

func HostTopOrNil(cfg Config) func(ctx context.Context, lines int) ([]string, error) {
	if KubernetesMode(cfg) {
		return nil
	}
	return hostTop
}

// daemonHealthOrNil is ui.Config.Daemon, and nil where this config was
// never given a health to report -- a config built by a test rather than
// by daemon(), which is the same nil-means-unavailable contract the
// readings above it have.
func daemonHealthOrNil(cfg Config) func() ui.DaemonHealth {
	if cfg.Health == nil {
		return nil
	}
	return cfg.Health.Health
}

// daemonHealthFor picks which of the two readings in daemon_restarts.go
// this deployment can take of itself. A host deployment counts its
// unit's starts in the journal; a Kubernetes one has neither unit nor
// journal, and counts the kubelet's restarts of its own pod's container
// instead -- the same loop, made visible on the shape where it is a
// CrashLoopBackOff of the single replica deploy/kubernetes/grain.yaml
// runs (grain/task-367).
//
// A pod that was not told its own name reports no count and says why,
// the same way a deployment with no journal does. That is the whole of
// the fallback: nothing here goes looking for the pod by label or by
// hostname, because a wrong pod's restart count is worse than none.
func DaemonHealthFor(cfg Config, startedAt time.Time) *daemonHealth {
	if KubernetesMode(cfg) {
		deployment, container := K8sSelfNames(cfg)
		pods := k8s.Client{Kubectl: cfg.K8sKubectl, Kubeconfig: EffectiveK8sKubeconfig(cfg), Namespace: cfg.K8sNamespace}
		return newPodDaemonHealth(startedAt, cfg.UnknownFlags, pods, ownPodName(), container,
			kubeShape(pods.NamespaceOrDefault(), deployment, container))
	}
	return newDaemonHealth(startedAt, cfg.UnknownFlags, systemlog.Journalctl{Unit: "grain-daemon.service"},
		newRestartRequests(cfg.DataDir))
}

// k8sSelfNames is which Deployment and container this process is a pod
// of. Both have flag defaults of "grain" (-k8s-deployment,
// -k8s-container) and both are required once an upgrade is configured,
// so a deployment that named neither is one whose objects are named
// after the manifest in this repository -- which is what these fall back
// to, since the alternative is a health reading that names nothing.
func K8sSelfNames(cfg Config) (deployment, container string) {
	deployment, container = cfg.K8sDeployment, cfg.K8sContainer
	if deployment == "" {
		deployment = "grain"
	}
	if container == "" {
		container = "grain"
	}
	return deployment, container
}

// upgradeImageNote is the startup line for a deployment whose upgrades
// pull an image. On Kubernetes there is no ref file and no restart
// command -- the upgrade repoints this Deployment and the rollout is the
// restart -- and saying "rewrite ; nothing restarts afterwards" there
// named nothing and was wrong
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F5).
func upgradeImageNote(cfg Config) string {
	if deploymentMode(cfg) == upgrade.ModeKube {
		deployment, container := K8sSelfNames(cfg)
		return fmt.Sprintf("grain daemon: upgrades health-check %s:<branch> in a throwaway pod and repoint "+
			"container %s of deployment/%s at it; the rollout restarts this pod", cfg.UpgradeImage, container, deployment)
	}
	return fmt.Sprintf("grain daemon: upgrades pull %s and rewrite %s%s",
		cfg.UpgradeImage, cfg.UpgradeImageRefFile, restartNote(cfg.UpgradeRestartCmd))
}

// restartNote is the tail of the line this daemon logs at startup about
// how upgrading is wired up here. Which command brings the new build up
// -- or that nothing does -- is the half of that configuration an
// operator most often finds out about the hard way, by watching an
// upgrade land and nothing restart, so it goes in the log next to the
// rest of it rather than only in the flags nobody re-reads.
func restartNote(restartCmd []string) string {
	if len(restartCmd) == 0 {
		return "; nothing restarts afterwards (no -upgrade-restart-cmd)"
	}
	return "; then run: " + strings.Join(restartCmd, " ")
}

// defaultRebootCmd is what the UI's reboot-host button runs absent a
// -reboot-cmd: `sudo systemctl reboot`, the exact command v1's
// mcp_server.py already ran for its own `reboot_controller` tool
// (grain/automation/mcp_server.py) and the one line scripts/setup.sh's
// sudoers drop-in used to grant $GRAIN_USER passwordless sudo for. Run
// as the plain, unprivileged user the daemon already runs as, not as
// root itself -- the same "only exactly this one command line"
// restriction v1 gave its own self-repair sudoers file.
var defaultRebootCmd = []string{"sudo", "systemctl", "reboot"}

// The same for the two actions beside it (grain/task-40, pkg/ui's
// Config.Shutdown and Config.Restart): power this machine off, and
// restart the grain service without touching the machine at all. Both
// are what a daemon running directly on the host under systemd would
// run, and both are named here for the same reason the reboot above is
// -- a deployment that wants something else passes the flag.
var (
	defaultShutdownCmd = []string{"sudo", "systemctl", "poweroff"}
	defaultRestartCmd  = []string{"sudo", "systemctl", "restart", "grain-daemon.service"}
)

// rebootHost builds startUIServer's ui.Config.Reboot out of -reboot-cmd,
// and hostCommand under it does the same for the other two.
//
// They are flags rather than constants because a daemon in a container
// has no host systemd to talk to: `systemctl reboot` inside the
// container would either fail outright or, worse, reach a systemd that
// isn't the host's. scripts/setup.sh's container unit points each at a
// `touch` of a file under the data dir instead, which a host-side
// systemd path unit watches and turns into the real command -- the same
// job the sudoers drop-in used to do, done by something that works
// across the container boundary.
func rebootHost(argv []string) func(context.Context) error {
	return hostCommand(argv, defaultRebootCmd)
}

// shutdownHost and restartDaemon are rebootHost's siblings, over
// -shutdown-cmd and -restart-cmd. Kept as three named funcs rather than
// three inline hostCommand calls at the wiring site so that what each
// button ends up running has one place to be read.
func shutdownHost(argv []string) func(context.Context) error {
	return hostCommand(argv, defaultShutdownCmd)
}

func restartDaemon(argv []string) func(context.Context) error {
	return hostCommand(argv, defaultRestartCmd)
}

// hostCommand turns one of those flags into the func the UI calls,
// falling back to the built-in default when the flag was not passed.
//
// The command's own output is discarded: nothing here has anywhere to
// show it, and a successful command takes this process down with it
// before there would be anything to show. What the UI gets is whether it
// started and what it exited with.
func hostCommand(argv, fallback []string) func(context.Context) error {
	if len(argv) == 0 {
		argv = fallback
	}
	return func(ctx context.Context) error {
		return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	}
}

// hostDisk is one filesystem hostStats reports a figure for: a path to
// take the reading through, and the word the UI shows for what of the
// daemon's own state lives on it (ui.DiskUsage.Holds).
type HostDisk struct {
	Holds string
	Path  string
}

// hostDisks lists those filesystems, in the order the pane shows them.
//
// None of these is "/": the daemon runs in a container whose root
// filesystem is an image layer nobody's runs fill up. What fills is
// whichever volume the state below sits on --
//
//	store       -data-dir, the store and the secrets. Small, and on
//	            terraform/gcp a 20 GB disk of its own.
//	sandboxes   -sandbox-dir, orchestrator.HostSandboxes' per-run
//	            checkouts. Empty (and so absent here) under
//	            -granule-sandboxes, which keeps no checkout on this host.
//	docker      docker's data root, which is where the sandbox image and
//	            every granule VM's qcow2 overlay actually land -- granule
//	            creates a VM's writable root inside its own container
//	            (bwsalmon/granule#37), so nothing grain writes itself ever
//	            passes through a path grain names.
//
// -- and only the last two of those grow with what a run does. Reporting
// -data-dir alone, as this did while all three shared one volume, showed
// a near-empty disk reading as healthy at exactly the moment a runaway
// build was filling the 100 GB one beside it (grain/task-148, after
// grain/task-134 gave sandboxes that volume).
//
// Two of these are usually one filesystem: terraform/gcp bind-mounts the
// sandbox volume's own sandboxes/ onto -sandbox-dir and points dockerd's
// data root at its docker/, so those two entries answer identically.
// hostStats folds them together rather than drawing the same bar twice.
func hostDisks(cfg Config) []HostDisk {
	disks := []HostDisk{{Holds: "store", Path: cfg.DataDir}}
	if cfg.SandboxDir != "" {
		disks = append(disks, HostDisk{Holds: "sandboxes", Path: cfg.SandboxDir})
	}
	if root := dockerRootDir(cfg.DockerRootDir); root != "" {
		// Read once, here, rather than left to show as a permanently
		// broken row on every poll. A containerised daemon is normally
		// not shown docker's data root at all -- scripts/setup.sh mounts
		// the socket, not the tree behind it -- and on terraform/gcp that
		// root is on the very volume the sandbox entry above already
		// reports, reached through the bind mount onto -sandbox-dir. So a
		// path dockerd names but this process cannot stat is the ordinary
		// case rather than a fault, and it costs no figure the pane does
		// not already have.
		if _, _, err := sysstat.DiskUsage(root); err != nil {
			log.Printf("grain daemon: docker's data root %s is not readable from here (%v); the host status pane reports no docker disk of its own", root, err)
		} else {
			disks = append(disks, HostDisk{Holds: "docker", Path: root})
		}
	}
	return disks
}

// hostVolumes is that same list as whoever needs it should ask for it:
// the one run() already resolved for this process (config.volumes), or a
// fresh resolution for a config that was never given one -- a config a
// test built, which is the only way that happens in a real daemon.
//
// A method rather than a bare field read so that a caller cannot
// accidentally get an empty list and report a host with no disks at all.
func (c Config) hostVolumes() []HostDisk {
	if c.Volumes != nil {
		return c.Volumes
	}
	return hostDisks(c)
}

// dockerRootTimeout bounds the one `docker info` dockerRootDir runs. It
// is asked once per process, at startup, so this only ever costs a
// deployment whose docker socket is mounted but whose engine is wedged --
// and then it costs it once, not once per poll.
const dockerRootTimeout = 10 * time.Second

// dockerRootDir resolves docker's data root for hostDisks above:
// whatever -docker-root-dir said, or else what the local engine says
// about itself. Empty means "no docker disk figure", which is what a
// deployment with no docker socket mounted (scripts/setup.sh only mounts
// it for granule and the Upgrade button) or no engine running gets.
//
// Asked once and logged once rather than polled: dockerd's data root is
// fixed for the life of the daemon it belongs to, and a deployment that
// moves it restarts both.
func dockerRootDir(configured string) string {
	if configured != "" {
		return configured
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerRootTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "-f", "{{.DockerRootDir}}").Output()
	if err != nil {
		log.Printf("grain daemon: asking docker where its data root is: %v (the host status pane reports no docker disk; pass -docker-root-dir to say)", err)
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		log.Printf("grain daemon: `docker info` named no data root; the host status pane reports no docker disk")
		return ""
	}
	log.Printf("grain daemon: docker's data root is %s; reporting its disk in the host status pane", root)
	return root
}

// hostStats is startUIServer's ui.Config.HostStats: this machine's own
// CPU-load/memory pressure, read straight out of /proc by pkg/sysstat,
// plus one usage figure per filesystem in disks -- see that package's own
// doc comment for why this, not any one sandbox, is what it reports, and
// hostDisks above for which filesystems those are.
//
// A failing disk reading is carried on the disk it belongs to rather
// than failing the whole call: load and memory came from a different
// file and are still good, and the other disks' readings are unaffected
// by one path that has stopped answering. hostDisks has already dropped
// the paths that were never readable to begin with, so a failure here
// means a filesystem that answered at startup and does not now -- a
// volume that went away, which is worth saying rather than worth
// suppressing.
func hostStats(disks []HostDisk, floor model.DiskFloor) (ui.HostPressure, error) {
	snap, err := sysstat.Read()
	if err != nil {
		return ui.HostPressure{}, err
	}
	// Read on every poll rather than once at startup, unlike docker's
	// data root above: this one genuinely changes while the daemon runs
	// -- reloading kvm_intel with a different "nested" is the documented
	// way to turn nesting on, and an operator who has just done it wants
	// the pane to say so without restarting grain.
	nestedVirt, nestedVirtDetail := sysstat.NestedVirtualization()
	return ui.HostPressure{
		LoadAverage1:     snap.LoadAverage1,
		LoadAverage5:     snap.LoadAverage5,
		LoadAverage15:    snap.LoadAverage15,
		CPUs:             snap.CPUs,
		CPUUtilization:   snap.CPUUtilization,
		MemoryUsedMB:     snap.MemUsedMB,
		MemoryTotalMB:    snap.MemTotalMB,
		SwapUsedMB:       snap.SwapUsedMB,
		SwapTotalMB:      snap.SwapTotalMB,
		Disks:            diskUsage(disks, floor),
		NestedVirt:       nestedVirt,
		NestedVirtDetail: nestedVirtDetail,
	}, nil
}

// diskUsage reads each of disks' own filesystem, folding entries that
// turn out to be the same filesystem into one -- st_dev, not the path,
// decides that (sysstat.FilesystemID's own doc comment on why a path
// comparison cannot: the sandbox volume reaches -sandbox-dir through a
// bind mount that shares no prefix with docker's root beside it).
//
// The first path onto a given filesystem keeps the entry and every later
// one only adds its word to Holds, so the order hostDisks chose is the
// order the pane reads in: the store, then the disk a run fills.
//
// A filesystem whose identity cannot be read but whose usage can is left
// unfolded rather than dropped: two identical rows say too much, and no
// row at all says too little.
func diskUsage(disks []HostDisk, floor model.DiskFloor) []ui.DiskUsage {
	var out []ui.DiskUsage
	byFilesystem := map[uint64]int{}
	for _, d := range disks {
		totalMB, usedMB, err := sysstat.DiskUsage(d.Path)
		if err != nil {
			out = append(out, ui.DiskUsage{Holds: []string{d.Holds}, Path: d.Path, Error: err.Error()})
			continue
		}
		if id, idErr := sysstat.FilesystemID(d.Path); idErr == nil {
			if i, ok := byFilesystem[id]; ok {
				out[i].Holds = append(out[i].Holds, d.Holds)
				continue
			}
			byFilesystem[id] = len(out)
		}
		out = append(out, ui.DiskUsage{
			Holds: []string{d.Holds}, Path: d.Path, UsedMB: usedMB, TotalMB: totalMB,
			// Resolved here, against this volume's own size, rather than
			// sent as a share for the pane to apply: the arithmetic has a
			// minimum and a cap in it (model.DiskFloor.FloorMB), and a
			// second implementation of it in JavaScript would be a second
			// answer to "why is this deployment holding work".
			FloorMB: floor.FloorMB(totalMB),
			// And what a sweep of this volume aims to get back to, which is
			// the threshold the pane draws its quieter level at: a volume
			// between the two is admitting work and is a few GB from not
			// (ui.DiskUsage.ReclaimMB).
			ReclaimMB: floor.ReclaimTargetMB(totalMB),
		})
	}
	return out
}

// hostTopTimeout bounds one `top` run. Its own sampling interval is a
// fraction of a second (pkg/hosttop), so anything near this means top is
// stuck rather than slow -- and the System pane polls, so a request left
// hanging would pile a second one on top of it every few seconds.
const hostTopTimeout = 15 * time.Second

// hostTop is startUIServer's ui.Config.HostTop: a `top` snapshot of this
// same process's own machine, for the System overlay's Top tab.
//
// The timeout is added here rather than inside pkg/hosttop for the same
// reason hostStats above takes its path as an argument: the package
// reads what it is asked to read, and how long this deployment is
// willing to wait for it is the daemon's own policy. The request's ctx
// still bounds it from the other end -- whichever fires first kills top.
func hostTop(ctx context.Context, lines int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, hostTopTimeout)
	defer cancel()
	return hosttop.Read(ctx, lines)
}

// openBrowser best-effort launches url in the system's default browser --
// failing to do so (headless box, unknown OS) is never fatal, since the
// server is up and the URL is printed regardless.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("grain: opening browser: %v", err)
	}
}

func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
