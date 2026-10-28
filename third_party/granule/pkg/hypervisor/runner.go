package hypervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/extents"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

// killGrace bounds how long the runtime waits after a forced vmm.shutdown
// (and, as a last resort, SIGTERM) before it gives up and sends SIGKILL.
// Overridable (package-private) so tests don't have to wait out the real
// value.
var killGrace = 5 * time.Second

// Runner starts and supervises a single cloud-hypervisor process.
type Runner struct {
	cfg config.Config
	cmd *exec.Cmd
	api *APIClient

	extentServers []*extents.Server
	extentStores  []*extents.Store

	// restored records whether Start booted by restoring cfg.SnapshotPath
	// (see BuildArgs and SnapshotExists) rather than booting fresh. Only
	// meaningful after Start returns.
	restored bool

	// done is closed once the process has exited; exitErr is only safe
	// to read after done is closed.
	done    chan struct{}
	exitErr error

	shutdownOnce sync.Once
}

// New returns a Runner for cfg. Call Start to launch the VM.
func New(cfg config.Config) *Runner {
	return &Runner{
		cfg: cfg,
		api: NewAPIClient(cfg.APISocket),
	}
}

// Start execs cloud-hypervisor with the arguments derived from the config
// and returns once the process has been launched. It does not wait for the
// guest to finish booting.
func (r *Runner) Start(stdout, stderr io.Writer) error {
	// Private, because whoever can connect to the API socket owns the VM:
	// it pauses, snapshots (guest memory and all), resizes and kills it.
	// See vmmUmask for the socket itself.
	if err := os.MkdirAll(filepath.Dir(r.cfg.APISocket), 0o700); err != nil {
		return fmt.Errorf("creating api socket directory: %w", err)
	}
	// cloud-hypervisor refuses to bind an api-socket path that already
	// exists from a previous run -- but one a running VM is still
	// listening on is not this run's to remove. See RemoveStaleSocket.
	if err := RemoveStaleSocket(r.cfg.APISocket, "cloud-hypervisor's API socket"); err != nil {
		return err
	}

	r.restored = SnapshotExists(r.cfg.SnapshotPath)
	if err := r.startExtentServers(); err != nil {
		return err
	}
	args, err := BuildArgs(r.cfg)
	if err != nil {
		r.closeExtentServers()
		return err
	}
	log.Printf("starting: %s", String(r.cfg.BinaryPath, args))

	cmd := exec.Command(r.cfg.BinaryPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil

	if err := startWithUmask(cmd, vmmUmask); err != nil {
		r.closeExtentServers()
		return fmt.Errorf("starting %s: %w", r.cfg.BinaryPath, err)
	}
	r.cmd = cmd
	r.done = make(chan struct{})

	go func() {
		r.exitErr = cmd.Wait()
		r.closeExtentServers()
		close(r.done)
	}()

	return nil
}

func (r *Runner) startExtentServers() error {
	for i := range r.cfg.Disks {
		d := &r.cfg.Disks[i]
		if d.VhostUserSocket == "" && !extents.IsExtentDir(d.Path) && r.cfg.StorageBackend != config.StorageBackendExtents {
			continue
		}
		if _, err := os.Stat(d.Path); err != nil {
			continue
		}
		sock := d.VhostUserSocket
		if sock == "" {
			sock = guestimage.VhostBlkSocket
		}
		if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
			sock = filepath.Join(filepath.Dir(r.cfg.APISocket), fmt.Sprintf("vhost-blk-%d.sock", i))
		}
		d.VhostUserSocket = sock

		var opts extents.Options
		if extents.IsExtentDir(d.Path) {
			opts = extents.Options{
				Dir:             d.Path,
				BasePath:        d.BasePath,
				ExtentSizeBytes: uint64(r.cfg.ExtentSizeMB) << 20,
				ReadOnly:        d.ReadOnly,
			}
		} else {
			opts = extents.Options{
				Dir:             filepath.Join(filepath.Dir(r.cfg.APISocket), fmt.Sprintf("extents-disk-%d", i)),
				BasePath:        d.Path,
				ExtentSizeBytes: uint64(r.cfg.ExtentSizeMB) << 20,
				ReadOnly:        d.ReadOnly,
			}
		}
		st, err := extents.Open(opts)
		if err != nil {
			r.closeExtentServers()
			return fmt.Errorf("opening extent store for disk %d (%s): %w", i, d.Path, err)
		}
		numQueues := d.NumQueues
		if numQueues == 0 {
			numQueues = r.cfg.DiskNumQueues
		}
		srv, err := extents.NewServer(st, sock, numQueues)
		if err != nil {
			_ = st.Close()
			r.closeExtentServers()
			return fmt.Errorf("starting vhost-user-blk server on %s: %w", sock, err)
		}
		srv.Start()
		r.extentStores = append(r.extentStores, st)
		r.extentServers = append(r.extentServers, srv)
	}
	return nil
}

func (r *Runner) closeExtentServers() {
	for _, srv := range r.extentServers {
		_ = srv.Close()
	}
	r.extentServers = nil
	for _, st := range r.extentStores {
		_ = st.Close()
	}
	r.extentStores = nil
}

// vmmUmask is the umask cloud-hypervisor runs under. Everything it
// creates is a way into the guest or a copy of it: the API socket (full
// control of the VM), the vsock socket (granule-agent's root exec in the
// guest) and a snapshot's memory-ranges (the guest's RAM, secrets and
// all). Under the usual 022 a socket comes out srwxr-xr-x and a snapshot
// 0644, readable by every account that can see the directory; under 077
// they are the owner's alone.
const vmmUmask = 0o077

// umaskMu serializes startWithUmask: the umask is per process, not per
// thread, so two overlapping calls would restore each other's value.
var umaskMu sync.Mutex

// startWithUmask starts cmd with the process umask set to mask, so the
// child inherits it, and puts the previous umask back once it has
// forked. os/exec has no per-child umask of its own.
func startWithUmask(cmd *exec.Cmd, mask int) error {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	return cmd.Start()
}

// Restored reports whether Start booted this VM by restoring a
// previously suspended snapshot (see Suspend) rather than booting it
// fresh. A restored VM has already been through whatever one-time setup
// produced that snapshot, so callers (cmd/granule-shim's runVM) skip
// re-running it.
func (r *Runner) Restored() bool {
	return r.restored
}

// Suspend pauses the running guest, writes a full snapshot of its state
// to cfg.SnapshotPath (see BuildArgs, which restores from it on a later
// run instead of booting fresh), and resumes it -- so this run carries
// on exactly as if Suspend had never been called. It only leaves behind
// a checkpoint for the *next* granule run to pick up.
func (r *Runner) Suspend(ctx context.Context) error {
	for _, st := range r.extentStores {
		if err := st.Flush(); err != nil {
			return fmt.Errorf("flushing extent store before snapshot: %w", err)
		}
	}
	overlayPath := ""
	if r.cfg.DiskMode == config.DiskModeOverlay {
		overlayPath = r.cfg.OverlayPath
	}
	return TakeSnapshot(ctx, r.api, r.cfg.SnapshotPath, overlayPath, true)
}

// TakeSnapshot pauses the running VM, captures a full snapshot of its state
// (RAM, registers, and the disk overlay at overlayPath if present) into snapshotPath,
// and optionally resumes the VM if resume is true.
func TakeSnapshot(ctx context.Context, api *APIClient, snapshotPath, overlayPath string, resume bool) error {
	if snapshotPath == "" {
		return fmt.Errorf("no snapshot path configured")
	}

	if err := api.Pause(ctx); err != nil {
		return fmt.Errorf("pausing vm: %w", err)
	}

	tmp := snapshotPath + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clearing stale snapshot staging directory %s: %w", tmp, err)
	}
	// A snapshot is the guest's memory, so it is its owner's alone: the
	// directory 0700 and everything in it 0600. See makePrivate.
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o700); err != nil {
		return fmt.Errorf("creating snapshot parent directory: %w", err)
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return fmt.Errorf("creating snapshot staging directory: %w", err)
	}

	if err := api.Snapshot(ctx, tmp); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("snapshotting vm: %w", err)
	}
	if err := makePrivate(tmp); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("restricting the snapshot's permissions: %w", err)
	}

	// cloud-hypervisor's own snapshot is memory and device state; the
	// disk behind it stays wherever it was. In the default disk mode
	// that disk is a qcow2 overlay inside this container's writable
	// layer (see config.PrepareOverlay), which the next run -- a new
	// container, which is the whole point of snapshotting to a volume
	// -- does not have: it would make a fresh one from the base image
	// and resume a guest whose memory believes in files the disk under
	// it no longer holds. So the overlay goes into the snapshot too,
	// while the VM is paused and it is not being written, and
	// RestoreDiskOverlay puts it back before the next boot restores.
	if overlayPath != "" {
		if extents.IsExtentDir(overlayPath) {
			if err := extents.CopyExtentDir(overlayPath, filepath.Join(tmp, SnapshotDiskOverlayName)); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("saving the guest's extent overlay into the snapshot: %w", err)
			}
		} else if _, err := os.Stat(overlayPath); err == nil {
			if err := copyFile(overlayPath, filepath.Join(tmp, SnapshotDiskOverlayName)); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("saving the guest's disk overlay into the snapshot: %w", err)
			}
		}
	}

	if err := os.Rename(tmp, snapshotPath); err != nil {
		return fmt.Errorf("publishing snapshot to %s: %w", snapshotPath, err)
	}

	if resume {
		if resumeErr := api.Resume(ctx); resumeErr != nil {
			return fmt.Errorf("resuming vm after snapshot: %w", resumeErr)
		}
	}
	return nil
}

// makePrivate takes away group and other access from everything under
// dir: 0700 for directories, 0600 for files. cloud-hypervisor writes a
// snapshot's files under its own umask, which is vmmUmask for a VMM
// Runner.Start launched but whatever the process that started it had for
// any other -- "granule snapshot" can be pointed at a VMM an older
// granule started -- so the files are fixed up after the fact rather
// than trusted to have come out right. Symlinks are left alone: chmod
// would follow them out of the snapshot.
func makePrivate(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return os.Chmod(path, 0o700)
		default:
			return os.Chmod(path, 0o600)
		}
	})
}

// SnapshotDiskOverlayName is the file name a snapshot directory carries
// the guest's writable overlay under (see Runner.snapshot and
// RestoreDiskOverlay). It sits alongside cloud-hypervisor's own
// config.json/state.json/memory-ranges, which name themselves and ignore
// anything else in the directory.
const SnapshotDiskOverlayName = "granule-disk-overlay.qcow2"

// RestoreDiskOverlay copies the disk overlay saved inside the snapshot at
// snapshotPath back to dst, reporting whether the snapshot had one to
// put back at all -- a snapshot taken before granule saved it does not,
// and a guest resumed from one gets whatever disk the container it lands
// in already has (see Runner.snapshot for why that matters).
//
// It writes through a sibling temporary file and renames, so a copy
// interrupted partway through leaves the destination as it was rather
// than a truncated overlay that config.PrepareOverlay would go on to reuse
// as if it were whole.
func RestoreDiskOverlay(snapshotPath, dst string) (bool, error) {
	if snapshotPath == "" || dst == "" {
		return false, nil
	}
	src := filepath.Join(snapshotPath, SnapshotDiskOverlayName)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking the disk overlay saved at %s: %w", src, err)
	}

	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return false, fmt.Errorf("creating the overlay directory for %s: %w", dst, err)
	}

	tmp := dst + ".restoring"
	if extents.IsExtentDir(src) {
		_ = os.RemoveAll(tmp)
		if err := extents.CopyExtentDir(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return false, fmt.Errorf("restoring the extent disk overlay saved at %s: %w", src, err)
		}
		_ = os.RemoveAll(dst)
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return false, fmt.Errorf("putting the restored extent disk overlay at %s: %w", dst, err)
		}
		return true, nil
	}
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("restoring the disk overlay saved at %s: %w", src, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("putting the restored disk overlay at %s: %w", dst, err)
	}
	return true, nil
}

// copyFile copies src over dst, creating dst if it isn't there, and
// flushes it to disk: both callers are writing something a *later* run
// (or a later container) has to be able to read back intact.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Wait blocks until the VMM process exits and returns its error, following
// the same convention as exec.Cmd.Wait (nil for a clean exit, *exec.ExitError
// for a non-zero exit code). Safe to call from multiple goroutines.
func (r *Runner) Wait() error {
	<-r.done
	return r.exitErr
}

// Shutdown asks the guest to power off gracefully, escalating to a forced
// VMM stop and finally to SIGKILL if it does not exit in time. It returns
// once the process has exited. Safe to call at most once per Runner.
func (r *Runner) Shutdown(ctx context.Context) {
	r.shutdownOnce.Do(func() { r.shutdown(ctx) })
}

func (r *Runner) shutdown(ctx context.Context) {
	log.Printf("requesting graceful guest shutdown (timeout %s)", r.cfg.ShutdownTimeout)
	pbCtx, cancel := context.WithTimeout(ctx, killGrace)
	// The api-socket is created early in cloud-hypervisor's startup, but
	// Start returns as soon as the process is forked, so there is a
	// small window where it isn't listening yet.
	err := r.api.WaitReady(pbCtx)
	if err == nil {
		err = r.api.PowerButton(pbCtx)
	}
	cancel()
	if err != nil {
		log.Printf("power-button request failed: %v", err)
	} else if r.waitUntil(r.cfg.ShutdownTimeout) {
		return
	}

	log.Printf("guest did not shut down in time, forcing vmm shutdown")
	forceCtx, cancel := context.WithTimeout(ctx, killGrace)
	err = r.api.WaitReady(forceCtx)
	if err == nil {
		err = r.api.ShutdownVMM(forceCtx)
	}
	cancel()
	if err != nil {
		log.Printf("vmm.shutdown request failed: %v", err)
	} else if r.waitUntil(killGrace) {
		return
	}

	if r.cmd.Process != nil {
		log.Printf("vmm still running, sending SIGTERM")
		_ = r.cmd.Process.Signal(syscall.SIGTERM)
		if r.waitUntil(killGrace) {
			return
		}

		log.Printf("vmm still running, sending SIGKILL")
		_ = r.cmd.Process.Kill()
	}
	<-r.done
}

// waitUntil blocks until the process exits or timeout elapses, reporting
// which happened. Safe to call repeatedly / concurrently with Wait.
func (r *Runner) waitUntil(timeout time.Duration) (exited bool) {
	select {
	case <-r.done:
		return true
	case <-time.After(timeout):
		return false
	}
}
