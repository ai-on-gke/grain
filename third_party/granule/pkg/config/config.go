// Package config loads the settings for a single cloud-hypervisor VM from
// environment variables, so the container can be driven entirely through a
// Kubernetes pod spec (env / envFrom) without a mounted config file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/extent"
	"github.com/bwsalmon/granule/pkg/extents"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/qcow2"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

const (
	envDiskImage                 = "CHV_DISK_IMAGE"
	envDiskReadonly              = "CHV_DISK_READONLY"
	envDiskMode                  = "CHV_DISK_MODE"
	envDiskOverlayPath           = "CHV_DISK_OVERLAY_PATH"
	envDiskSizeMB                = "CHV_DISK_SIZE_MB"
	envExtentSizeMB              = "CHV_EXTENT_SIZE_MB"
	envDiskExtentSizeMB          = "CHV_DISK_EXTENT_SIZE_MB"
	envStorageBackend            = "CHV_STORAGE_BACKEND"
	envDiskNumQueues             = "CHV_DISK_NUM_QUEUES"
	envDiskIOThreads             = "CHV_DISK_IOTHREADS"
	envDiskIOEngine              = "CHV_DISK_IO_ENGINE"
	envExtraDisks                = "CHV_EXTRA_DISKS"
	envKernel                    = "CHV_KERNEL"
	envInitramfs                 = "CHV_INITRAMFS"
	envFirmware                  = "CHV_FIRMWARE"
	envCmdline                   = "CHV_CMDLINE"
	envCPUs                      = "CHV_CPUS"
	envVCPUs                     = "CHV_VCPUS"
	envCPUsMax                   = "CHV_CPUS_MAX"
	envMemoryMB                  = "CHV_MEMORY_MB"
	envMemoryMaxMB               = "CHV_MEMORY_MAX_MB"
	envMemoryHotplug             = "CHV_MEMORY_HOTPLUG"
	envMemoryShared              = "CHV_MEMORY_SHARED"
	envNet                       = "CHV_NET"
	envAPISocket                 = "CHV_API_SOCKET"
	envVsockSocket               = "CHV_VSOCK_SOCKET"
	envBinaryPath                = "CHV_BINARY_PATH"
	envExtraArgs                 = "CHV_EXTRA_ARGS"
	envShutdownTimeout           = "CHV_SHUTDOWN_TIMEOUT"
	envSetupScript               = "CHV_SETUP_SCRIPT"
	envSnapshotPath              = "CHV_SNAPSHOT_PATH"
	envMemAgent                  = "CHV_MEM_AGENT"
	envMemAgentAddr              = "CHV_MEM_AGENT_ADDR"
	envMemAgentToken             = "CHV_MEM_AGENT_TOKEN"
	envMemAgentStepMB            = "CHV_MEM_AGENT_STEP_MB"
	envMemAgentCooldown          = "CHV_MEM_AGENT_COOLDOWN"
	envMemAgentScaleDownCooldown = "CHV_MEM_AGENT_SCALE_DOWN_COOLDOWN"
	envFS                        = "CHV_FS"
	envVolumes                   = "CHV_VOLUMES"

	// envContainerOverride is a containerspec.Override as JSON: the
	// entrypoint, command, environment, user and working directory this
	// VM's wrapped container runs with instead of the ones its image was
	// wrapped with. See Config.ContainerOverride.
	envContainerOverride = "CHV_CONTAINER_OVERRIDE"

	// envWorkloadStdout and envWorkloadStderr name where a wrapped
	// container's own stdout and stderr are written, apart from the VM's
	// console. See Config.WorkloadStdout.
	envWorkloadStdout = "GRANULE_WORKLOAD_STDOUT"
	envWorkloadStderr = "GRANULE_WORKLOAD_STDERR"

	// envWorkloadExitFile names where a wrapped container's exit status
	// is written once its VM has stopped. See Config.WorkloadExitFile.
	envWorkloadExitFile = "GRANULE_WORKLOAD_EXIT_FILE"

	// envWorkloadStdin names where a wrapped container's stdin is read
	// from, envWorkloadTTY runs it on a terminal, and envWorkloadResize
	// names where that terminal's sizes are read from. See
	// Config.WorkloadStdin.
	envWorkloadStdin  = "GRANULE_WORKLOAD_STDIN"
	envWorkloadTTY    = "GRANULE_WORKLOAD_TTY"
	envWorkloadResize = "GRANULE_WORKLOAD_RESIZE"

	// envGuestContractMin raises the oldest guest contract version this
	// container will boot above guestimage.MinimumBootContract -- for a
	// deployment that packs every guest it runs and wants an undeclared
	// one refused rather than accepted as version 0. It only ever
	// tightens: a value below the built-in floor is an error, because
	// loosening it is how a refusal that exists to stop an unreachable VM
	// becomes a warning.
	envGuestContractMin = "CHV_GUEST_CONTRACT_MIN"

	defaultCPUs = 2

	// defaultMemoryMB is deliberately small: with memory hotplug on by
	// default (defaultMemoryHotplug), the guest can grow up to
	// defaultMemoryMaxMB on demand (see BuildArgs/Resize), so there is no
	// need to pay for a large VMM memory footprint from the very first
	// boot the way a fixed CHV_MEMORY_MB always did.
	defaultMemoryMB      = 256
	defaultMemoryMaxMB   = 2048
	defaultMemoryHotplug = true
	defaultAPISocket     = "/run/cloud-hypervisor/api.sock"
	// defaultVsockSocket is the host end of the guest's virtio-vsock
	// device: a unix socket in this container that "granule exec" dials
	// to reach granule-agent inside the guest (see pkg/guestexec and
	// cmd/granule-agent). Under cloud-hypervisor's hybrid vsock the host
	// side is a socket file rather than anything on a network, which is
	// what lets exec work with no guest networking at all.
	defaultVsockSocket = "/run/granule/vsock.sock"
	// defaultVsockCID is the guest's context id. Any value above the two
	// reserved ones (0 is hypervisor, 2 is host) does, and nothing on
	// either side reads it back: the guest binds VMADDR_CID_ANY and the
	// host addresses the guest by connecting to the socket above, not by
	// CID. cloud-hypervisor requires one anyway.
	defaultVsockCID        = 3
	defaultBinaryPath      = "/usr/local/bin/cloud-hypervisor"
	defaultShutdownTimeout = 20 * time.Second

	// defaultMemAgent is off by default: unlike CHV_MEMORY_HOTPLUG (which
	// merely wires up the *capability* to resize), enabling this actually
	// lets the guest drive resizes on its own, unprompted -- new enough
	// behavior, and enough of a departure from every other knob in this
	// package (all of which are either inert until an operator acts, or
	// bounded by values the operator themselves set), that it's opt-in
	// rather than on by default. See pkg/memagent.
	defaultMemAgent = false

	// defaultMemAgentAddr matches netshim's own default control link
	// address (see pkg/netshim's defaultControlCIDR): the
	// guest-side agent (deploy/guest-image's granule-mem-agent) is told
	// that address by granule-control-net at boot and signals it on this
	// fixed port. Overriding NETSHIM_CONTROL_CIDR therefore also
	// requires overriding this to match, or the guest's signals go
	// nowhere.
	defaultMemAgentAddr              = "169.254.100.1:30090"
	defaultMemAgentStepMB            = 256
	defaultMemAgentCooldown          = 30 * time.Second
	defaultMemAgentScaleDownCooldown = 2 * time.Minute

	// defaultDiskImage is the guest disk image baked into the granule OCI
	// image itself (see the Dockerfile's guest-image stage): a minimal
	// Debian system carrying granule-agent, usable out of the box without
	// a separately-managed disk image. CHV_DISK_IMAGE overrides this for
	// any other guest.
	//
	// This and the four paths below are the guest half of the image, and
	// they are pkg/guestimage's constants rather than literals
	// because the set is something other callers have to agree on: an
	// image assembled from two provenances (somebody else's guest under
	// this deployment's granule binary) only works if both ends put the
	// same files at the same paths. See that package's comment.
	defaultDiskImage = guestimage.DiskImage

	// DiskModeOverlay, DiskModePersistent and DiskModeReadOnly are the
	// three things "a writable root disk" can mean, which CHV_DISK_READONLY
	// could not express as a boolean.
	//
	//   overlay    (the default) the guest writes into an overlay of its
	//              own, made from the disk image, which is only ever read.
	//              If the underlying disk is a flat file and the local fs
	//              supports reflink, it copies the flat file with reflink.
	//              Otherwise, it adds a new qcow2 overlay layer.
	//              Discarding the overlay resets the guest.
	//   persistent the guest writes through to the disk image itself. What
	//              "granule guest build" needs, since the point there is
	//              for the changes to end up in the image being committed,
	//              and what CHV_DISK_READONLY=false has always meant.
	//   readonly   the disk is attached read-only, as CHV_DISK_READONLY=true.
	DiskModeOverlay    = "overlay"
	DiskModePersistent = "persistent"
	DiskModeReadOnly   = "readonly"

	// StorageBackendStandard and StorageBackendExtents select how the
	// guest root disk is stored and presented to Cloud-Hypervisor
	// (CHV_STORAGE_BACKEND). Standard (qcow2 overlay / flat disk.img) is
	// the default; extents uses an N-MiB extent directory served over an
	// in-process vhost-user-blk socket.
	StorageBackendStandard = "standard"
	StorageBackendExtents  = "extents"

	NetModeInline = "inline"
	NetModeNone   = "none"

	// defaultOverlayPath is where DiskModeOverlay puts the guest's overlay.
	// Inside the container's own writable layer rather than on a tmpfs, so
	// a stopped-and-started container keeps whatever the guest had written
	// -- the same thing that happened when writes landed in the disk image
	// -- and only "docker rm" discards it. Override with CHV_DISK_OVERLAY_PATH.
	defaultOverlayPath = guestimage.Overlay

	// defaultContainerOverrideDisk is where the device carrying
	// CHV_CONTAINER_OVERRIDE to the guest is written: beside the overlay,
	// in the container's own writable layer, because a snapshot restored
	// in this container later names the same path in its disk list.
	defaultContainerOverrideDisk = guestimage.OverlayDir + "/container-override.img"

	// defaultKernel is the guest kernel baked into the granule OCI image
	// itself (see the Dockerfile's build-kernel stage): a
	// cloud-hypervisor/linux ch_defconfig build, plus IPv6, with PVH
	// entry and virtio-pci/virtio-blk/virtio-net/virtio-mem support, matching what
	// the bundled disk image (defaultDiskImage) itself needs to boot and
	// what CONFIG_VIRTIO_MEM memory hotplug (see "Memory hotplug" in the
	// README) needs to actually take effect. Used only when neither
	// CHV_KERNEL nor CHV_FIRMWARE is set, so an operator supplying either
	// (e.g. for a custom guest that needs its own kernel, or a
	// firmware/bootloader-based guest) always overrides it as before.
	defaultKernel = guestimage.RuntimeKernel

	// defaultGuestKernel and defaultGuestInitramfs are the guest's *own*
	// kernel and initramfs, present only when the image was built with
	// GUEST_KERNEL_PACKAGE (see the Dockerfile's guest-customized stage).
	// A guest that brings its own kernel needs it and nothing else:
	// booting such a rootfs under defaultKernel above instead gives it a
	// kernel whose modules are not the ones in its /lib/modules, which
	// fails late and obscurely rather than at boot. So when both are
	// there they win over defaultKernel, and when they are not -- the
	// default build -- nothing changes at all.
	defaultGuestKernel    = guestimage.GuestKernel
	defaultGuestInitramfs = guestimage.GuestInitramfs
)

// Disk describes one virtio-blk device to attach to the VM.
type Disk struct {
	Path      string
	ReadOnly  bool
	NumQueues int
	IOThreads bool
	IOEngine  string

	// BasePath, when set on an extent overlay disk, names the read-only
	// backing source (either a disk.extents directory or a flat disk.img).
	BasePath string

	// VhostUserSocket, when set, attaches the disk via vhost-user-blk
	// (--disk vhost_user=true,socket=...) instead of a direct image path.
	VhostUserSocket string

	// Serial is the virtio-blk serial the guest sees the disk with (in
	// /sys/block/vdX/serial), which is how it finds a disk by purpose
	// rather than by the order it was attached in. Empty gives it none.
	Serial string
}

// Filesystem describes one virtio-fs or shared filesystem device to attach to the VM.
type Filesystem struct {
	Tag       string
	Socket    string
	SharedDir string
	ReadOnly  bool

	// Destination, when set, is an absolute path inside a wrapped
	// container that granule-container mounts this share on before the
	// container starts (read-only when ReadOnly is set). It travels to
	// the guest as a containerspec.Mount in ContainerOverride. Empty
	// leaves the share attached and unmounted, for the guest to mount
	// itself, as it always has been.
	Destination string
}

// Config holds everything needed to boot exactly one cloud-hypervisor VM.
type Config struct {
	// Disks[0] is always the primary boot disk (CHV_DISK_IMAGE). Any
	// further entries come from CHV_EXTRA_DISKS.
	Disks []Disk

	// Filesystems describes virtio-fs shared filesystem devices (CHV_FS / CHV_VOLUMES).
	Filesystems []Filesystem

	// DiskMode is how Disks[0] is attached: DiskModeOverlay,
	// DiskModePersistent or DiskModeReadOnly. In overlay mode Disks[0]
	// still names the *source* image here; the caller creates the overlay
	// and repoints it (see PrepareOverlay) before the VM boots.
	DiskMode string

	// StorageBackend is how Disks[0] is backed: StorageBackendStandard
	// (default) or StorageBackendExtents (CHV_STORAGE_BACKEND).
	StorageBackend string

	// OverlayPath is where DiskModeOverlay writes the guest's overlay.
	// Its default (guestimage.Overlay) is named disk.qcow2;
	// what PrepareOverlay puts there is a qcow2 overlay (or reflink raw copy),
	// and the name is not what decides how it is attached -- see
	// hypervisor.BuildArgs.
	OverlayPath string

	// DiskSizeMB is the size of the writable disk the guest boots from,
	// in MiB -- the overlay DiskModeOverlay gives it (see
	// PrepareOverlay), which is created at this size and grown to it on
	// a later boot if it already exists at a smaller one. Growing only,
	// and only ever the overlay: the source image itself is never
	// touched, so it must be at least as large as that image.
	//
	// Zero (the default) means the source image's own size, which is what
	// an overlay has always been given. Only meaningful in
	// DiskModeOverlay: the other two modes write to (or refuse to write
	// to) the image itself, and there is no overlay to size.
	//
	// This sizes the block device the guest sees and nothing else --
	// growing the partition table and filesystem inside it is the guest's
	// own job. The bundled reference guest does that for itself on every
	// boot (granule-growfs, see deploy/guest-image/README.md); a guest
	// supplied via CHV_DISK_IMAGE has to arrange its own.
	DiskSizeMB int

	// ExtentSizeMB is the chunk size in MiB for flat multi-extent VMDK overlays
	// created in DiskModeOverlay. Zero (the default) means the default 64 MiB extent size.
	ExtentSizeMB int

	// DiskNumQueues configures virtio-blk multi-queue count (CHV_DISK_NUM_QUEUES).
	// 0 means single queue / hypervisor default.
	DiskNumQueues int

	// DiskIOThreads enables dedicated host iothreads for block devices (CHV_DISK_IOTHREADS).
	DiskIOThreads bool

	// DiskIOEngine configures the host asynchronous I/O backend (CHV_DISK_IO_ENGINE: "io_uring", "epoll", or "sync").
	DiskIOEngine string

	// Direct kernel boot. Kernel is mutually exclusive with Firmware:
	// setting both is an error, but leaving both unset is not -- Kernel
	// then defaults to defaultKernel, the kernel baked into the image
	// (see FromEnv).
	Kernel    string
	Initramfs string
	Firmware  string
	Cmdline   string

	// CPUs is the guest's vCPU count at boot. With CPUsMax greater than
	// CPUs, it can grow later via cloud-hypervisor's ACPI CPU hotplug
	// (see hypervisor.APIClient.ResizeCPUs and "granule resize").
	CPUs int

	// CPUsMax is the ceiling CPUs can grow to via hotplug. Left at its
	// default (CPUs itself, i.e. no headroom), no hotplug device is
	// attached at all -- same fixed-vCPU behavior as before this
	// existed. Must be at least CPUs.
	CPUsMax int

	// MemoryMB is the guest's memory size at boot. With MemoryHotplug on,
	// it can grow up to MemoryMaxMB later, either from within the guest
	// or via the cloud-hypervisor API (see hypervisor.APIClient.Resize
	// and "granule resize").
	MemoryMB int

	// MemoryMaxMB is the ceiling MemoryMB can grow to via hotplug. Ignored
	// when MemoryHotplug is false. Must be at least MemoryMB.
	MemoryMaxMB int

	// MemoryHotplug attaches a virtio-mem hotplug device sized for growth
	// up to MemoryMaxMB, so long as MemoryMaxMB is actually greater than
	// MemoryMB (otherwise there is no room to grow into, and no device is
	// attached at all). Requires MemoryShared, since virtio-mem needs a
	// shared memory backend.
	MemoryHotplug bool

	MemoryShared bool

	// Nets holds one --net value per guest NIC, each passed through
	// verbatim, e.g. "tap=eth0,mac=02:00:00:00:00:01". CHV_NET supplies
	// a single entry; a netshim-managed sandbox (see cmd/granule-shim)
	// replaces the whole list with values it derives from the
	// namespace's own identity, which is how the guest gets both its
	// spliced NIC and its control NIC. Left empty, the VM boots with no
	// network device.
	Nets []string

	APISocket string

	// VsockSocket is the host end of the guest's virtio-vsock device --
	// the unix socket cloud-hypervisor creates and "granule exec" dials.
	// Empty attaches no vsock device at all, which leaves the guest with
	// no way to be exec'd into.
	VsockSocket string

	// VsockCID is the guest's vsock context id. See defaultVsockCID for
	// why its value does not matter.
	VsockCID   int
	BinaryPath string

	// ExtraArgs is split on whitespace and appended to the
	// cloud-hypervisor invocation as-is, for flags this package does not
	// model directly (e.g. --device, --vsock).
	ExtraArgs []string

	// ShutdownTimeout bounds how long the runtime waits for the guest to
	// power off gracefully after receiving SIGTERM before forcing the
	// VMM to stop.
	ShutdownTimeout time.Duration

	// SetupScript, if set, is run once inside the guest over SSH (see
	// pkg/guestexec, the same machinery "granule exec" uses) right
	// after a fresh (i.e. not restored) boot finishes. If SnapshotPath is
	// also set, the VM is then suspended to it so a later granule run
	// resumes that exact post-setup state instead of booting fresh and
	// re-running the script; left unset, the script just runs again on
	// every fresh boot instead.
	SetupScript string

	// SnapshotPath, if SetupScript is also set, is where SetupScript's
	// completion is suspended to (see hypervisor.Runner.Suspend). Either
	// way, if a complete snapshot is already present there at startup,
	// it's restored from instead of a fresh boot (see
	// hypervisor.BuildArgs). Must be an absolute path: it's turned
	// directly into a "file://" URL for cloud-hypervisor's own
	// snapshot/restore API.
	SnapshotPath string

	// MemAgent starts pkg/memagent's listener alongside the VM: a
	// guest-side agent (deploy/guest-image's granule-mem-agent, installed
	// in both guest image variants) watches its own memory pressure and
	// signals this listener when it's under strain, which then grows
	// the guest's memory the same way an operator's "granule resize"
	// would, up to MemoryMaxMB. Requires MemoryHotplug.
	MemAgent bool

	// MemAgentAddr is the address the listener above binds -- reachable
	// from the guest over netshim's control link (see
	// defaultMemAgentAddr). Ignored unless MemAgent is set.
	MemAgentAddr string

	// MemAgentToken is the secret pkg/memagent requires on every pressure
	// signal from the guest. Optional here only because "granule run"
	// generates one when it is unset; the listener never runs without
	// one. It is delivered to the guest over vsock, never on the kernel
	// command line -- see pkg/memagent's "The token".
	MemAgentToken string

	// MemAgentStepMB is how much each pressure signal grows the guest's
	// memory by, capped at MemoryMaxMB. The step is added to the size
	// cloud-hypervisor currently holds for the VM, read back per signal
	// (see memagent.Server), so a "granule resize" from outside moves
	// where the next step starts rather than being stepped over.
	MemAgentStepMB int

	// MemAgentCooldown is the minimum time between two resizes the
	// listener performs, so a guest still under pressure right after a
	// grow (which is asynchronous -- see hypervisor.APIClient.Resize)
	// doesn't trigger another one before that grow has had a chance to
	// help.
	MemAgentCooldown time.Duration

	// MemAgentScaleDownCooldown is the minimum time between a resize and a
	// subsequent scale-down resize the listener performs, to prevent
	// thrashing when memory demands fluctuate.
	MemAgentScaleDownCooldown time.Duration

	// GuestContract is what the guest half beside Disks[0] declares about
	// which version of the guest-side contract it speaks. FromEnv has
	// already refused the boot if it is one this granule cannot honour
	// (see guestimage.BootContract's Check), so what it is carried here
	// for is the log line: an accepted version is worth stating once per
	// boot, and an *undeclared* guest especially so, since that is the
	// one a later floor bump will stop.
	GuestContract guestimage.BootContract

	// ContainerOverride, when set, is merged over the wrapped image's own
	// container spec by granule-container inside the guest: the same
	// image, run with this VM's entrypoint, command, environment, user
	// and working directory (CHV_CONTAINER_OVERRIDE). It reaches the guest
	// on a small read-only disk of its own -- see containerspec.Override
	// for why a disk -- which PrepareContainerOverride writes to
	// ContainerOverrideDisk and attaches. A guest that runs no wrapped
	// container ignores the disk.
	ContainerOverride *containerspec.Override

	// ContainerOverrideDisk is where that disk's image is written.
	ContainerOverrideDisk string

	// WorkloadStdout and WorkloadStderr, when either is set, are where
	// the wrapped container's own stdout and stderr are written, each on
	// its own -- a regular file to append to, or a FIFO, which is waited
	// on until something reads it (GRANULE_WORKLOAD_STDOUT and
	// GRANULE_WORKLOAD_STDERR). They arrive over the guest's vsock device
	// from granule-container (see pkg/workloadio), which keeps what the
	// workload writes before anything is reading. The console is
	// unchanged either way: the workload's output still reaches it too,
	// merged with the kernel's and init's, as it always has. Empty, the
	// stream goes nowhere but the console.
	WorkloadStdout string
	WorkloadStderr string

	// WorkloadExitFile, when set, is where the wrapped container's exit
	// status is written, as a workloadio.ExitStatus, once the VM has
	// stopped (GRANULE_WORKLOAD_EXIT_FILE) -- for a caller that is not
	// the shim's parent and so cannot read its exit code. Setting it also
	// declares that this VM runs a wrapped container: one that stops
	// without granule-container having reported a status is then
	// workloadio.ExitUnreported rather than cloud-hypervisor's own exit
	// code. The shim's exit code follows the workload's either way, set
	// or not; see pkg/workloadio's "Exit status".
	WorkloadExitFile string

	// WorkloadStdin, when set, is what the wrapped container's stdin
	// reads (GRANULE_WORKLOAD_STDIN): a FIFO, which is waited on until
	// something writes to it, or a regular file. When its writer closes
	// it -- or the file ends -- the container reads end-of-file. Setting
	// it starts the container with its stdin open (`docker run -i`;
	// containerspec.Override.Stdin, merged into ContainerOverride), and
	// it travels on the workload's stream connection (see pkg/workloadio's
	// "Stdin"). Empty, the container reads /dev/null as it always has,
	// unless its own spec opens its stdin.
	WorkloadStdin string

	// WorkloadTTY runs the wrapped container on a terminal
	// (GRANULE_WORKLOAD_TTY; `docker run -t`, containerspec.Override.Tty):
	// its output is the terminal's, so it all arrives on WorkloadStdout.
	WorkloadTTY bool

	// WorkloadResize, when set, is a FIFO (or file) the terminal's size
	// is read from, a line at a time as "COLS ROWS" (GRANULE_WORKLOAD_RESIZE).
	// A FIFO is opened again whenever its writer closes it, so a caller
	// may open it for each resize. It needs WorkloadTTY.
	WorkloadResize string
}

// FromEnv builds a Config from the process environment and validates it.
// The disk image (and kernel/firmware, if given explicitly) are expected
// to already be present on the local filesystem: this runtime never
// fetches images, so startup only pays for booting the VM itself.
// CHV_DISK_IMAGE defaults to the guest image baked into the granule OCI
// image (defaultDiskImage), and, unless CHV_FIRMWARE is given instead,
// CHV_KERNEL defaults to the matching kernel baked in alongside it
// (defaultKernel) -- set either explicitly to boot a different guest
// instead.
// GuestConsole is the serial device a guest kernel is told to log to,
// for the architecture this granule runs its guests on.
//
// It is read off runtime.GOARCH rather than asked of anything, because a
// granule guest is never emulated: cloud-hypervisor runs it on the
// machine granule itself is running on, so the guest's architecture is
// this binary's architecture.
//
// The device is not the same one on both. x86 has an 8250-compatible UART
// at the ISA port addresses Linux enumerates as ttyS0. On aarch64 there
// are no ISA ports and cloud-hypervisor gives the guest an ARM PL011,
// which Linux enumerates as ttyAMA0 -- so a guest told console=ttyS0
// there is told to use a device it does not have.
//
// That is not cosmetic. An arm64 guest booted with console=ttyS0 prints
// nothing at all and never becomes reachable: "granule vm run" fails
// with "vsock port 1024 is not being listened on inside the guest yet",
// and because the console is the thing that is broken there is no output
// to say why. Measured on a Debian guest under nested virtualization on
// aarch64: two boots with ttyS0 sat unreachable for ten and twelve
// minutes, while the same image booted with ttyAMA0 reached ready and
// answered an exec.
func GuestConsole() string { return guestConsoleFor(runtime.GOARCH) }

// guestConsoleFor is GuestConsole's mapping, separated from the running
// binary's own architecture so that both answers can be tested from
// either one.
//
// Anything granule has not been run on keeps ttyS0, which is today's
// answer rather than a guess at a better one: the two architectures
// granule is known to run guests on are amd64 and arm64, and a third
// would need more than its console name looked at before it booted. A
// caller that knows better passes a cmdline of its own (CHV_CMDLINE,
// "granule vm create -cmdline").
func guestConsoleFor(goarch string) string {
	switch goarch {
	case "arm64":
		return "ttyAMA0"
	default:
		return "ttyS0"
	}
}

// defaultCmdline is the kernel command line the VM container falls back
// to when CHV_CMDLINE is unset. A function rather than a constant because
// the console in it depends on the architecture (GuestConsole); vmspec's
// VMSpec.Validate says the same line again for the cmdline "granule vm
// create" derives, and the two have to agree.
func defaultCmdline() string {
	return "console=" + GuestConsole() + " root=/dev/vda rw noresume quiet loglevel=3"
}

func FromEnv() (Config, error) {
	cfg := Config{
		Kernel:       os.Getenv(envKernel),
		Initramfs:    os.Getenv(envInitramfs),
		Firmware:     os.Getenv(envFirmware),
		Cmdline:      CmdlineWith(defaultCmdline(), os.Getenv(envCmdline)),
		Nets:         netsFromEnv(),
		APISocket:    getEnvDefault(envAPISocket, defaultAPISocket),
		VsockSocket:  getEnvDefault(envVsockSocket, defaultVsockSocket),
		VsockCID:     defaultVsockCID,
		BinaryPath:   getEnvDefault(envBinaryPath, defaultBinaryPath),
		MemoryShared: true,
	}

	var err error
	cfg.DiskNumQueues, err = getEnvInt(envDiskNumQueues, 0)
	if err != nil {
		return Config{}, err
	}
	if cfg.DiskNumQueues < 0 {
		return Config{}, fmt.Errorf("%s must not be negative, got %d", envDiskNumQueues, cfg.DiskNumQueues)
	}

	cfg.DiskIOThreads, err = getEnvBool(envDiskIOThreads, false)
	if err != nil {
		return Config{}, err
	}

	if rawEngine := strings.TrimSpace(os.Getenv(envDiskIOEngine)); rawEngine != "" {
		cfg.DiskIOEngine = strings.ToLower(rawEngine)
		if cfg.DiskIOEngine == "iouring" {
			cfg.DiskIOEngine = "io_uring"
		}
		switch cfg.DiskIOEngine {
		case "io_uring", "epoll", "sync":
		default:
			return Config{}, fmt.Errorf("%s must be \"io_uring\", \"epoll\" or \"sync\", got %q", envDiskIOEngine, rawEngine)
		}
	}

	rawBackend := strings.TrimSpace(os.Getenv(envStorageBackend))
	switch strings.ToLower(rawBackend) {
	case "", StorageBackendStandard:
		cfg.StorageBackend = StorageBackendStandard
	case StorageBackendExtents:
		cfg.StorageBackend = StorageBackendExtents
	default:
		return Config{}, fmt.Errorf("%s must be %q or %q, got %q",
			envStorageBackend, StorageBackendStandard, StorageBackendExtents, rawBackend)
	}

	diskPath, hasDiskEnv := os.LookupEnv(envDiskImage)
	if !hasDiskEnv || diskPath == "" {
		if cfg.StorageBackend == StorageBackendExtents && (extents.IsExtentDir(guestimage.DiskExtents) || !fileExists(defaultDiskImage)) {
			diskPath = guestimage.DiskExtents
		} else if rawBackend == "" && !fileExists(defaultDiskImage) && extents.IsExtentDir(guestimage.DiskExtents) {
			cfg.StorageBackend = StorageBackendExtents
			diskPath = guestimage.DiskExtents
		} else if rawBackend == "" && !fileExists(defaultDiskImage) && extent.IsExtentDir(guestimage.DiskExtents) {
			diskPath = guestimage.DiskExtents
		} else {
			diskPath = defaultDiskImage
		}
	} else if rawBackend == "" && extents.IsExtentDir(diskPath) {
		cfg.StorageBackend = StorageBackendExtents
	}
	mode, err := diskModeFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.DiskMode = mode
	defaultOvl := defaultOverlayPath
	if cfg.StorageBackend == StorageBackendExtents {
		defaultOvl = guestimage.OverlayExtents
	}
	cfg.OverlayPath = getEnvDefault(envDiskOverlayPath, defaultOvl)
	var vhostSock string
	if cfg.StorageBackend == StorageBackendExtents {
		vhostSock = guestimage.VhostBlkSocket
	}
	cfg.Disks = append(cfg.Disks, Disk{
		Path:            diskPath,
		ReadOnly:        mode == DiskModeReadOnly,
		NumQueues:       cfg.DiskNumQueues,
		IOThreads:       cfg.DiskIOThreads,
		IOEngine:        cfg.DiskIOEngine,
		VhostUserSocket: vhostSock,
	})

	cfg.DiskSizeMB, err = getEnvInt(envDiskSizeMB, 0)
	if err != nil {
		return Config{}, err
	}
	if cfg.DiskSizeMB == 0 {
		if opt := os.Getenv("CHV_STORAGE_OPT"); opt != "" {
			if mb, err := parseStorageOptSize(opt); err == nil && mb > 0 {
				cfg.DiskSizeMB = mb
			}
		}
		if opt := os.Getenv("CHV_STORAGE_OPTS"); opt != "" {
			if mb, err := parseStorageOptSize(opt); err == nil && mb > 0 {
				cfg.DiskSizeMB = mb
			}
		}
	}
	if cfg.DiskSizeMB < 0 {
		return Config{}, fmt.Errorf("%s must not be negative, got %d", envDiskSizeMB, cfg.DiskSizeMB)
	}
	// Rejected rather than ignored in the other two modes: the only
	// writable disk granule creates for itself is the overlay, so a
	// request to size a disk it doesn't create can't be honoured, and
	// silently booting a guest at a size the operator didn't ask for is
	// the worse of the two answers.
	if cfg.DiskSizeMB > 0 && mode != DiskModeOverlay {
		return Config{}, fmt.Errorf("%s requires %s=%s: only the guest's own overlay is resized, never the disk image itself (got %s=%s)",
			envDiskSizeMB, envDiskMode, DiskModeOverlay, envDiskMode, mode)
	}

	cfg.ExtentSizeMB, err = getEnvInt(envExtentSizeMB, 0)
	if err != nil {
		return Config{}, err
	}
	if cfg.ExtentSizeMB == 0 {
		if v := os.Getenv(envDiskExtentSizeMB); v != "" {
			cfg.ExtentSizeMB, err = getEnvInt(envDiskExtentSizeMB, 0)
			if err != nil {
				return Config{}, err
			}
		}
	}
	if cfg.ExtentSizeMB < 0 {
		return Config{}, fmt.Errorf("%s must not be negative, got %d", envExtentSizeMB, cfg.ExtentSizeMB)
	}
	if cfg.ExtentSizeMB > 0 && mode != DiskModeOverlay && cfg.StorageBackend != StorageBackendExtents {
		return Config{}, fmt.Errorf("%s requires %s=%s: extents are only used with overlay disks (got %s=%s)",
			envExtentSizeMB, envDiskMode, DiskModeOverlay, envDiskMode, mode)
	}

	for _, spec := range splitNonEmpty(os.Getenv(envExtraDisks), ",") {
		path, ro, err := parseExtraDisk(spec)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envExtraDisks, err)
		}
		cfg.Disks = append(cfg.Disks, Disk{
			Path:      path,
			ReadOnly:  ro,
			NumQueues: cfg.DiskNumQueues,
			IOThreads: cfg.DiskIOThreads,
			IOEngine:  cfg.DiskIOEngine,
		})
	}

	cpuVar := envCPUs
	if _, ok := os.LookupEnv(envCPUs); !ok {
		if _, ok := os.LookupEnv(envVCPUs); ok {
			cpuVar = envVCPUs
		}
	}
	cfg.CPUs, err = getEnvInt(cpuVar, defaultCPUs)
	if err != nil {
		return Config{}, err
	}
	if cfg.CPUs < 1 {
		return Config{}, fmt.Errorf("%s must be at least 1, got %d", cpuVar, cfg.CPUs)
	}

	// CHV_CPUS_MAX defaults to CHV_CPUS itself: unlike memory, CPU
	// hotplug headroom is opt-in rather than on by default, since
	// (unlike virtio-mem) newly added vCPUs need the guest to online
	// them itself -- see the README's "CPU hotplug" section.
	cfg.CPUsMax, err = getEnvInt(envCPUsMax, cfg.CPUs)
	if err != nil {
		return Config{}, err
	}
	if cfg.CPUsMax < cfg.CPUs {
		return Config{}, fmt.Errorf("%s (%d) must be at least %s (%d)", envCPUsMax, cfg.CPUsMax, envCPUs, cfg.CPUs)
	}

	cfg.MemoryMB, err = getEnvInt(envMemoryMB, defaultMemoryMB)
	if err != nil {
		return Config{}, err
	}
	if cfg.MemoryMB < 128 {
		return Config{}, fmt.Errorf("%s must be at least 128, got %d", envMemoryMB, cfg.MemoryMB)
	}

	cfg.MemoryShared, err = getEnvBool(envMemoryShared, true)
	if err != nil {
		return Config{}, err
	}
	if cfg.StorageBackend == StorageBackendExtents && !cfg.MemoryShared {
		return Config{}, fmt.Errorf("%s=%s requires %s=true: vhost-user-blk needs a shared memory backend",
			envStorageBackend, StorageBackendExtents, envMemoryShared)
	}

	// CHV_MEMORY_MAX_MB defaults to whichever is larger of
	// defaultMemoryMaxMB and the (possibly overridden) starting size, so
	// raising CHV_MEMORY_MB alone never produces a nonsensical "max below
	// start" default.
	maxDefault := defaultMemoryMaxMB
	if cfg.MemoryMB > maxDefault {
		maxDefault = cfg.MemoryMB
	}
	cfg.MemoryMaxMB, err = getEnvInt(envMemoryMaxMB, maxDefault)
	if err != nil {
		return Config{}, err
	}
	if cfg.MemoryMaxMB < cfg.MemoryMB {
		return Config{}, fmt.Errorf("%s (%d) must be at least %s (%d)", envMemoryMaxMB, cfg.MemoryMaxMB, envMemoryMB, cfg.MemoryMB)
	}

	cfg.MemoryHotplug, err = getEnvBool(envMemoryHotplug, defaultMemoryHotplug)
	if err != nil {
		return Config{}, err
	}
	if cfg.MemoryHotplug && cfg.MemoryMaxMB > cfg.MemoryMB && !cfg.MemoryShared {
		return Config{}, fmt.Errorf("%s requires %s=true when %s (%d) exceeds %s (%d): virtio-mem hotplug needs a shared memory backend", envMemoryHotplug, envMemoryShared, envMemoryMaxMB, cfg.MemoryMaxMB, envMemoryMB, cfg.MemoryMB)
	}

	cfg.ShutdownTimeout, err = getEnvDuration(envShutdownTimeout, defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg.MemAgent, err = getEnvBool(envMemAgent, defaultMemAgent)
	if err != nil {
		return Config{}, err
	}
	cfg.MemAgentAddr = getEnvDefault(envMemAgentAddr, defaultMemAgentAddr)
	cfg.MemAgentToken = os.Getenv(envMemAgentToken)
	cfg.MemAgentStepMB, err = getEnvInt(envMemAgentStepMB, defaultMemAgentStepMB)
	if err != nil {
		return Config{}, err
	}
	if cfg.MemAgentStepMB < 1 {
		return Config{}, fmt.Errorf("%s must be at least 1, got %d", envMemAgentStepMB, cfg.MemAgentStepMB)
	}
	cfg.MemAgentCooldown, err = getEnvDuration(envMemAgentCooldown, defaultMemAgentCooldown)
	if err != nil {
		return Config{}, err
	}
	cfg.MemAgentScaleDownCooldown, err = getEnvDuration(envMemAgentScaleDownCooldown, defaultMemAgentScaleDownCooldown)
	if err != nil {
		return Config{}, err
	}
	if cfg.MemAgent && !cfg.MemoryHotplug {
		return Config{}, fmt.Errorf("%s requires %s=true: the guest agent has nothing to ask for without hotplug wired up", envMemAgent, envMemoryHotplug)
	}
	if cfg.MemAgent && cfg.VsockSocket == "" {
		return Config{}, fmt.Errorf("%s requires a vsock device (%s is set empty): the memory agent's token reaches the guest over it", envMemAgent, envVsockSocket)
	}
	if cfg.MemAgent && cfg.MemoryMaxMB <= cfg.MemoryMB {
		return Config{}, fmt.Errorf("%s requires %s above %s (%d): the guest can only be grown inside the hotplug window, which is sized once at boot", envMemAgent, envMemoryMaxMB, envMemoryMB, cfg.MemoryMB)
	}

	cfg.ExtraArgs = splitNonEmpty(os.Getenv(envExtraArgs), " ")

	cfg.SetupScript = os.Getenv(envSetupScript)
	cfg.SnapshotPath = os.Getenv(envSnapshotPath)
	if cfg.SnapshotPath == "" {
		if _, err := os.Stat(guestimage.SnapshotDir); err == nil {
			cfg.SnapshotPath = guestimage.SnapshotDir
		}
	}
	if cfg.SnapshotPath != "" {
		if !filepath.IsAbs(cfg.SnapshotPath) {
			return Config{}, fmt.Errorf("%s must be an absolute path, got %q", envSnapshotPath, cfg.SnapshotPath)
		}
		parent := filepath.Dir(cfg.SnapshotPath)
		if _, err := os.Stat(parent); err != nil {
			return Config{}, fmt.Errorf("%s's parent directory (%s): %w", envSnapshotPath, parent, err)
		}
	}

	if raw := strings.TrimSpace(os.Getenv(envContainerOverride)); raw != "" {
		o, err := containerspec.ParseOverride([]byte(raw))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envContainerOverride, err)
		}
		cfg.ContainerOverride = &o
		cfg.ContainerOverrideDisk = defaultContainerOverrideDisk
	}

	cfg.WorkloadStdout = strings.TrimSpace(os.Getenv(envWorkloadStdout))
	cfg.WorkloadStderr = strings.TrimSpace(os.Getenv(envWorkloadStderr))
	for _, v := range []struct{ env, path string }{{envWorkloadStdout, cfg.WorkloadStdout}, {envWorkloadStderr, cfg.WorkloadStderr}} {
		if v.path != "" && !filepath.IsAbs(v.path) {
			return Config{}, fmt.Errorf("%s must be an absolute path, got %q", v.env, v.path)
		}
	}
	if (cfg.WorkloadStdout != "" || cfg.WorkloadStderr != "") && cfg.VsockSocket == "" {
		return Config{}, fmt.Errorf("%s and %s require a vsock device (%s is set empty): the workload's streams leave the guest over it", envWorkloadStdout, envWorkloadStderr, envVsockSocket)
	}
	if cfg.WorkloadStdout != "" || cfg.WorkloadStderr != "" {
		cfg.Cmdline = CmdlineWith(cfg.Cmdline, workloadio.CmdlineParam+"=1")
	}
	cfg.WorkloadExitFile = strings.TrimSpace(os.Getenv(envWorkloadExitFile))
	if cfg.WorkloadExitFile != "" && !filepath.IsAbs(cfg.WorkloadExitFile) {
		return Config{}, fmt.Errorf("%s must be an absolute path, got %q", envWorkloadExitFile, cfg.WorkloadExitFile)
	}
	if cfg.WorkloadExitFile != "" && cfg.VsockSocket == "" {
		return Config{}, fmt.Errorf("%s requires a vsock device (%s is set empty): the workload's exit status leaves the guest over it", envWorkloadExitFile, envVsockSocket)
	}

	cfg.WorkloadStdin = strings.TrimSpace(os.Getenv(envWorkloadStdin))
	cfg.WorkloadResize = strings.TrimSpace(os.Getenv(envWorkloadResize))
	if cfg.WorkloadTTY, err = getEnvBool(envWorkloadTTY, false); err != nil {
		return Config{}, err
	}
	for _, v := range []struct{ env, path string }{{envWorkloadStdin, cfg.WorkloadStdin}, {envWorkloadResize, cfg.WorkloadResize}} {
		if v.path != "" && !filepath.IsAbs(v.path) {
			return Config{}, fmt.Errorf("%s must be an absolute path, got %q", v.env, v.path)
		}
	}
	if cfg.WorkloadResize != "" && !cfg.WorkloadTTY {
		return Config{}, fmt.Errorf("%s needs %s: there is no terminal to resize without one", envWorkloadResize, envWorkloadTTY)
	}
	if (cfg.WorkloadStdin != "" || cfg.WorkloadTTY) && cfg.VsockSocket == "" {
		return Config{}, fmt.Errorf("%s and %s require a vsock device (%s is set empty): the workload's input reaches the guest over it", envWorkloadStdin, envWorkloadTTY, envVsockSocket)
	}
	if cfg.WorkloadStdin != "" || cfg.WorkloadTTY {
		// The guest learns to open them the way it learns the rest of
		// this container's process: from the override.
		if cfg.ContainerOverride == nil {
			cfg.ContainerOverride = &containerspec.Override{}
			cfg.ContainerOverrideDisk = defaultContainerOverrideDisk
		}
		on := true
		if cfg.WorkloadStdin != "" {
			cfg.ContainerOverride.Stdin = &on
		}
		if cfg.WorkloadTTY {
			cfg.ContainerOverride.Tty = &on
		}
	}

	var fsSpecs []string
	if raw := os.Getenv(envFS); raw != "" {
		fsSpecs = append(fsSpecs, splitNonEmpty(raw, ";")...)
	}
	if raw := os.Getenv(envVolumes); raw != "" {
		fsSpecs = append(fsSpecs, splitNonEmpty(raw, ";")...)
	}
	var mounts []containerspec.Mount
	for _, spec := range fsSpecs {
		fs, err := parseFilesystem(spec)
		if err != nil {
			return Config{}, fmt.Errorf("parsing filesystem %q: %w", spec, err)
		}
		cfg.Filesystems = append(cfg.Filesystems, fs)
		if fs.Destination != "" {
			mounts = append(mounts, containerspec.Mount{Tag: fs.Tag, Destination: fs.Destination, ReadOnly: fs.ReadOnly})
		}
	}
	if len(mounts) > 0 {
		// The guest learns where to mount them the way it learns the
		// rest of its container: from the override.
		if cfg.ContainerOverride == nil {
			cfg.ContainerOverride = &containerspec.Override{}
			cfg.ContainerOverrideDisk = defaultContainerOverrideDisk
		}
		cfg.ContainerOverride.Mounts = append(cfg.ContainerOverride.Mounts, mounts...)
		if err := cfg.ContainerOverride.Validate(); err != nil {
			return Config{}, fmt.Errorf("%s and %s: %w", envFS, envVolumes, err)
		}
	}

	if cfg.Kernel != "" && cfg.Firmware != "" {
		return Config{}, fmt.Errorf("%s and %s are mutually exclusive", envKernel, envFirmware)
	}
	// Neither set: default to the kernel baked into the image itself,
	// matching the bundled guest disk image the same way -- rather than
	// requiring one of the two to be supplied externally. The guest's
	// own kernel, if the image has one, is the right default for the
	// disk beside it; defaultKernel is the fallback for a guest that
	// brought none. CHV_INITRAMFS set by hand is left alone either way.
	if cfg.Kernel == "" && cfg.Firmware == "" {
		kernel, initramfs := bakedInKernel(defaultGuestKernel, defaultGuestInitramfs, defaultKernel)
		cfg.Kernel = kernel
		if cfg.Initramfs == "" {
			cfg.Initramfs = initramfs
		}
	}

	if err := cfg.checkOptionValues(); err != nil {
		return Config{}, err
	}
	if err := cfg.checkPathsExist(); err != nil {
		return Config{}, err
	}

	// Last, because it is about the disk rather than about the
	// environment, and because a disk that isn't there at all should say
	// so above rather than be reported as a guest with no declaration.
	if err := cfg.checkGuestContract(diskPath); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// checkGuestContract refuses a boot whose guest half was built for a
// different version of the guest-side contract than this granule
// implements. See pkg/guestimage's bootcontract.go for why the
// check is a refusal rather than a warning, and contract.go for what the
// contract covers.
//
// Read from the *source* disk's directory, which is why this runs in
// FromEnv rather than later: PrepareOverlay repoints Disks[0] at an overlay
// in this container, and a declaration is only ever beside the image it
// describes.
func (c *Config) checkGuestContract(diskPath string) error {
	min, err := getEnvInt(envGuestContractMin, guestimage.MinimumBootContract)
	if err != nil {
		return err
	}
	raw, set := os.LookupEnv(envGuestContractMin)
	fromEnv := set && strings.TrimSpace(raw) != ""
	if fromEnv {
		// Only ever upwards. A floor below the built-in one asks granule to
		// boot a guest it has already been told it cannot talk to, and a
		// floor above what it implements can never be met by any guest, so
		// both are refused here where the operator can read which of the
		// two they wrote.
		if min < guestimage.MinimumBootContract {
			return fmt.Errorf("%s=%d is below the minimum guest contract version %d this granule boots: it can require a newer guest half, never an older one -- a guest whose contract this granule cannot honour is a VM nothing can reach, which is not something to downgrade to a warning",
				envGuestContractMin, min, guestimage.MinimumBootContract)
		}
		if min > guestimage.Contract {
			return fmt.Errorf("%s=%d is above the guest contract version %d this granule implements, so no guest could satisfy it: granule packs guests declaring version %d",
				envGuestContractMin, min, guestimage.Contract, guestimage.Contract)
		}
	}

	declaration, err := guestimage.ReadBootContract(diskPath)
	if err != nil {
		return err
	}
	if err := declaration.Check(min); err != nil {
		if fromEnv {
			// Say whose policy refused it: a floor the operator raised
			// themselves is a different thing to go and look at than this
			// build's own.
			return fmt.Errorf("%s=%d: %w", envGuestContractMin, min, err)
		}
		return err
	}
	c.GuestContract = declaration
	return nil
}

// APISocket returns the cloud-hypervisor API socket path from
// CHV_API_SOCKET (or its default) alone, without loading or validating
// the rest of a VM's configuration. Used by modes like "granule resize"
// that only need to reach the API of an already-running VM in this same
// container, not to boot one.
func APISocket() string {
	return getEnvDefault(envAPISocket, defaultAPISocket)
}

// bakedInKernel picks between the two kernels an image can carry: the
// guest's own (guestKernel plus guestInitramfs, present only when the
// image was built with GUEST_KERNEL_PACKAGE) and fallback, the
// build-kernel build that has always been there. Both halves of the pair
// have to exist to take it -- a vmlinuz with no matching initrd would
// boot without the modules and udev rules the initramfs carries, which
// is exactly the silent unreachable-guest failure the pair exists to
// avoid. The fallback pairs with no initramfs, as before.
func bakedInKernel(guestKernel, guestInitramfs, fallback string) (kernel, initramfs string) {
	if fileExists(guestKernel) && fileExists(guestInitramfs) {
		return guestKernel, guestInitramfs
	}
	return fallback, ""
}

// fileExists reports whether path is present and is a regular file --
// used to pick between the two baked-in kernels, where "is it in this
// image" is the whole question. Any error (including a directory in the
// way) reads as absent: the fallback it selects is the kernel that has
// always been used, so a wrong answer here degrades to today's behavior
// rather than to a boot failure.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// checkOptionValues refuses a value that would be read as more than one
// value once it is in cloud-hypervisor's argv.
//
// BuildArgs passes each of these inside a "key=value,key=value" list --
// "--disk path=...,readonly=...", "--api-socket path=...", "--vsock
// socket=...", "--fs tag=...,socket=...", "--restore source_url=...,
// resume=true" -- and cloud-hypervisor splits that list on every comma
// with no quoting. A disk path of "/d.img,readonly=off" therefore set a
// second option rather than naming a file, overriding the readonly=on
// granule appends for a read-only disk. A comma is refused outright
// rather than escaped, since cloud-hypervisor has no escape for one.
//
// The memory agent's token is checked here too: memagent reads it back as
// the first whitespace-separated field of every signal line, so a token
// containing whitespace could never match and the agent would refuse
// every signal without saying why.
func (c Config) checkOptionValues() error {
	type value struct{ what, v string }
	values := []value{
		{envAPISocket, c.APISocket},
		{envVsockSocket, c.VsockSocket},
		{envSnapshotPath, c.SnapshotPath},
		{envDiskOverlayPath, c.OverlayPath},
	}
	for i, d := range c.Disks {
		what := envExtraDisks
		if i == 0 {
			what = envDiskImage
		}
		values = append(values, value{what, d.Path})
	}
	for _, fs := range c.Filesystems {
		values = append(values,
			value{envFS + " tag", fs.Tag},
			value{envFS + " socket", fs.Socket},
			value{envFS + " shared_dir", fs.SharedDir})
	}
	for _, v := range values {
		if strings.Contains(v.v, ",") {
			return fmt.Errorf("%s %q contains a comma, which cloud-hypervisor would read as the start of another option; rename the path", v.what, v.v)
		}
	}
	if strings.IndexFunc(c.MemAgentToken, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s contains whitespace, which the memory agent's signal format cannot carry: the token is one whitespace-separated field", envMemAgentToken)
	}
	return nil
}

func (c Config) checkPathsExist() error {
	paths := map[string]string{}
	for i, d := range c.Disks {
		paths[fmt.Sprintf("disk[%d] %s", i, d.Path)] = d.Path
	}
	if c.Kernel != "" {
		paths[envKernel] = c.Kernel
	}
	if c.Initramfs != "" {
		paths[envInitramfs] = c.Initramfs
	}
	if c.Firmware != "" {
		paths[envFirmware] = c.Firmware
	}
	for label, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s (%s): %w", label, path, err)
		}
	}
	return nil
}

func parseExtraDisk(spec string) (path string, readonly bool, err error) {
	parts := strings.SplitN(spec, ":", 2)
	path = parts[0]
	if path == "" {
		return "", false, fmt.Errorf("empty disk path in %q", spec)
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "ro":
			readonly = true
		case "rw":
			readonly = false
		default:
			return "", false, fmt.Errorf("unknown disk mode %q in %q, want \"ro\" or \"rw\"", parts[1], spec)
		}
	}
	return path, readonly, nil
}

func splitNonEmpty(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(s, sep) {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// CmdlineWith composes the kernel command line a caller asked for onto
// the one granule boots a guest with by itself.
//
// CHV_CMDLINE is the caller's half of the command line rather than the
// whole of it: docs/granulefile.md §5.5 calls a Granule's
// `kernel-cmdline` "extra kernel command line", and a `VM SET
// kernel-cmdline=quiet` records exactly "quiet" in the image's config,
// since the table there says the setting is recorded verbatim as its
// runtime variable. Read as a replacement, that machine booted with no
// root= and no console= at all -- a kernel with nothing to mount and
// nowhere to say so, which is a VM container that comes up and a guest
// that never answers.
//
// So what the caller names is added to granule's own line, and what they
// do not name is kept. A parameter they do name replaces granule's,
// which is what makes a full command line such as
// `console=ttyS0 root=/dev/vda ro` still mean exactly itself: it names
// every parameter granule sets, so nothing of granule's survives it.
// rw and ro count as one parameter, being one switch spelled two ways.
// (The deployment examples pass just `root=/dev/vda ro` and leave
// console= to granule, so the guest gets the console its architecture
// has.)
//
// Kept separate from the ip= and granule.routes= parameters the VM
// container appends at boot (see cmd/granule-shim): those are the
// sandbox's own address, knowable only once it exists, and they are
// appended to whatever this returns.
func CmdlineWith(base, extra string) string {
	extras := strings.Fields(extra)
	if len(extras) == 0 {
		return base
	}
	named := make(map[string]bool, len(extras))
	for _, param := range extras {
		named[cmdlineKey(param)] = true
	}
	var out []string
	for _, param := range strings.Fields(base) {
		if !named[cmdlineKey(param)] {
			out = append(out, param)
		}
	}
	return strings.Join(append(out, extras...), " ")
}

// cmdlineKey is the parameter a kernel command line token sets: the name
// before its "=", or the token itself for a bare switch. "rw" and "ro"
// share a key because they are the same switch.
func cmdlineKey(param string) string {
	if name, _, ok := strings.Cut(param, "="); ok {
		return name
	}
	if param == "rw" || param == "ro" {
		return "rw"
	}
	return param
}

func getEnvDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getEnvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", key, v)
	}
	return n, nil
}

func getEnvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	// on/off beside strconv's own spellings: a Granule writes its
	// switches that way -- docs/granulefile.md §5.5 gives
	// "memory-hotplug" the values on and off, and VM SET records the
	// value it was given as the runtime variable verbatim -- so the
	// machine a `VM SET memory-hotplug=on` produced arrives here with
	// "on" in CHV_MEMORY_HOTPLUG and used to refuse to boot.
	switch strings.ToLower(v) {
	case "on", "yes":
		return true, nil
	case "off", "no":
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", key, v)
	}
	return b, nil
}

func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, v)
	}
	return d, nil
}

// netsFromEnv turns CHV_NET into the one-element list BuildArgs expects,
// or no list at all when it is unset (a VM with no network device).
func netsFromEnv() []string {
	if v := os.Getenv(envNet); v != "" {
		return []string{v}
	}
	return nil
}

// diskModeFromEnv resolves CHV_DISK_MODE, falling back to the boolean it
// replaced.
//
// The fallback is not a straight translation, because the two settings
// were asked in different places and "writable" meant different things in
// each. CHV_DISK_READONLY is read here, inside the VM's own container,
// where false has always meant "write through to the disk image" -- so it
// maps to DiskModePersistent, and a deployment that sets it keeps exactly
// the behaviour it has today rather than silently acquiring a
// throw-away overlay. granule's own -disk-readonly=false meant
// something else again ("give this VM a private writable disk", which it
// implemented as a host-side overlay), so it maps to DiskModeOverlay --
// see pkg/cli's own translation.
//
// Setting both is an error rather than a precedence rule: they contradict
// each other often enough (mode=overlay with readonly=true) that guessing
// which the caller meant is worse than saying so.
func diskModeFromEnv() (string, error) {
	mode := os.Getenv(envDiskMode)
	rawReadonly, hasReadonly := os.LookupEnv(envDiskReadonly)

	if mode != "" && hasReadonly {
		return "", fmt.Errorf("%s and %s are mutually exclusive (%s=%q, %s=%q): %s replaces it",
			envDiskMode, envDiskReadonly, envDiskMode, mode, envDiskReadonly, rawReadonly, envDiskMode)
	}

	if mode == "" {
		if !hasReadonly {
			return DiskModeOverlay, nil
		}
		readonly, err := getEnvBool(envDiskReadonly, false)
		if err != nil {
			return "", err
		}
		if readonly {
			return DiskModeReadOnly, nil
		}
		return DiskModePersistent, nil
	}

	switch mode {
	case DiskModeOverlay, DiskModePersistent, DiskModeReadOnly:
		return mode, nil
	default:
		return "", fmt.Errorf("%s must be %q, %q or %q, got %q",
			envDiskMode, DiskModeOverlay, DiskModePersistent, DiskModeReadOnly, mode)
	}
}

// PrepareOverlay makes DiskModeOverlay real: it creates the guest's CoW overlay
// at OverlayPath.
// If the underlying disk is a flat file and the local filesystem supports reflink,
// it copies the flat file with reflink.
// Otherwise, it creates a new qcow2 layer pointing to the source image as its backing file.
// Repoints Disks[0] at OverlayPath. A no-op in the other two modes.
//
// Idempotent, and deliberately so in a specific way: an overlay that
// already exists is reused rather than recreated, so restarting a
// container keeps whatever the guest had written -- which is what happened
// when writes landed in the disk image itself, and the behaviour a caller
// restarting a VM expects. Discarding the overlay (removing the container)
// is what resets the guest.
//
// DiskSizeMB, when set, is applied here too -- to a fresh overlay by
// creating it at that size, and to one that already exists by growing it
// (see qcow2.Resize for qcow2 overlays or os.Truncate for raw overlays) --
// so it takes effect before the VM is started either way, and raising it on
// an already-running VM's next boot enlarges the disk it comes back with
// instead of being ignored.
//
// A source that is an extent directory (pkg/extent) is assembled into a
// flat raw disk at OverlayPath -- reflinked extent by extent where the
// filesystem allows, copied otherwise -- rather than overlaid: that is
// the only writable form of it cloud-hypervisor can attach, and it boots
// as image_type=raw with no backing file. The other two modes refuse an
// extent directory, since there is no single file to write through to or
// attach read-only.
func (c *Config) PrepareOverlay() error {
	if len(c.Disks) == 0 {
		return nil
	}
	useExtents := c.StorageBackend == StorageBackendExtents ||
		extents.IsExtentDir(c.Disks[0].Path) ||
		extents.IsExtentDir(c.OverlayPath)

	source := c.Disks[0].Path
	if c.DiskMode != DiskModeOverlay {
		if useExtents && c.Disks[0].VhostUserSocket == "" {
			c.Disks[0].VhostUserSocket = guestimage.VhostBlkSocket
		}
		if !useExtents && extent.IsExtentDir(source) {
			return fmt.Errorf("%s %s is an extent directory, which only %s=%s can boot: it is assembled into the guest's own disk at boot, and there is no single image for %s=%s to attach",
				envDiskImage, source, envDiskMode, DiskModeOverlay, envDiskMode, c.DiskMode)
		}
		return nil
	}
	wantBytes := int64(c.DiskSizeMB) * 1024 * 1024

	if useExtents {
		if c.OverlayPath == "" || c.OverlayPath == guestimage.Overlay {
			c.OverlayPath = guestimage.OverlayExtents
		}
		extBytes := uint64(c.ExtentSizeMB) * 1024 * 1024
		if err := extents.PrepareOverlay(c.OverlayPath, source, uint64(wantBytes), extBytes); err != nil {
			return fmt.Errorf("preparing extent overlay %s from %s: %w", c.OverlayPath, source, err)
		}
		vhostSock := c.Disks[0].VhostUserSocket
		if vhostSock == "" {
			vhostSock = guestimage.VhostBlkSocket
		}
		c.StorageBackend = StorageBackendExtents
		c.Disks[0] = Disk{
			Path:            c.OverlayPath,
			BasePath:        source,
			VhostUserSocket: vhostSock,
			ReadOnly:        false,
			NumQueues:       c.Disks[0].NumQueues,
			IOThreads:       c.Disks[0].IOThreads,
			IOEngine:        c.Disks[0].IOEngine,
			Serial:          c.Disks[0].Serial,
		}
		return nil
	}

	if _, err := os.Stat(c.OverlayPath); err == nil {
		if wantBytes > 0 {
			if qcow2.IsQcow2(c.OverlayPath) {
				if err := qcow2.Resize(c.OverlayPath, wantBytes); err != nil {
					return fmt.Errorf("resizing the existing overlay %s to %s=%d: %w", c.OverlayPath, envDiskSizeMB, c.DiskSizeMB, err)
				}
			} else {
				info, err := os.Stat(c.OverlayPath)
				if err != nil {
					return fmt.Errorf("statting the existing overlay %s: %w", c.OverlayPath, err)
				}
				currentBytes := info.Size()
				if wantBytes < currentBytes {
					return fmt.Errorf("resizing the existing overlay %s to %s=%d: %s is already %d bytes: shrinking a disk is not supported",
						c.OverlayPath, envDiskSizeMB, c.DiskSizeMB, c.OverlayPath, currentBytes)
				}
				if wantBytes > currentBytes {
					if err := os.Truncate(c.OverlayPath, wantBytes); err != nil {
						return fmt.Errorf("resizing the existing overlay %s to %s=%d: %w", c.OverlayPath, envDiskSizeMB, c.DiskSizeMB, err)
					}
				}
			}
		}
		c.Disks[0] = Disk{
			Path:      c.OverlayPath,
			ReadOnly:  false,
			NumQueues: c.Disks[0].NumQueues,
			IOThreads: c.Disks[0].IOThreads,
			IOEngine:  c.Disks[0].IOEngine,
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking for an existing overlay at %s: %w", c.OverlayPath, err)
	}

	if extent.IsExtentDir(source) {
		m, err := extent.ReadManifest(source)
		if err != nil {
			return fmt.Errorf("reading the extent disk %s: %w", source, err)
		}
		if wantBytes > 0 && wantBytes < m.VirtualSize {
			return fmt.Errorf("%s=%d is smaller than the source disk %s (%d bytes): the guest's filesystem would be truncated",
				envDiskSizeMB, c.DiskSizeMB, source, m.VirtualSize)
		}
		if _, err := extent.Assemble(source, c.OverlayPath, wantBytes); err != nil {
			return fmt.Errorf("assembling the extent disk %s into %s: %w", source, c.OverlayPath, err)
		}
		c.Disks[0] = Disk{
			Path:      c.OverlayPath,
			ReadOnly:  false,
			NumQueues: c.Disks[0].NumQueues,
			IOThreads: c.Disks[0].IOThreads,
			IOEngine:  c.Disks[0].IOEngine,
		}
		return nil
	}

	sourceBytes, err := qcow2.VirtualSize(source)
	if err != nil {
		return fmt.Errorf("detecting virtual size of source disk %s: %w", source, err)
	}
	size := sourceBytes

	// The overlay reads through to the source image for everything the
	// guest hasn't written, so presenting it as smaller than that image
	// would cut the guest's own filesystem off at the new end. Refuse
	// instead of quietly booting a truncated root disk.
	if wantBytes > 0 {
		if wantBytes < sourceBytes {
			return fmt.Errorf("%s=%d is smaller than the source disk %s (%d bytes): the guest's filesystem would be truncated",
				envDiskSizeMB, c.DiskSizeMB, source, sourceBytes)
		}
		size = wantBytes
	}
	if err := os.MkdirAll(filepath.Dir(c.OverlayPath), 0o755); err != nil {
		return fmt.Errorf("creating the overlay directory: %w", err)
	}

	isSourceFlat := !qcow2.IsQcow2(source)
	reflinkOk := qcow2.HasReflinkSupport(filepath.Dir(c.OverlayPath))

	if isSourceFlat && reflinkOk {
		if _, err := qcow2.CloneFileReflink(source, c.OverlayPath); err != nil {
			return fmt.Errorf("creating reflink overlay %s from %s: %w", c.OverlayPath, source, err)
		}
		if size > sourceBytes {
			if err := os.Truncate(c.OverlayPath, size); err != nil {
				return fmt.Errorf("expanding the raw overlay %s to %d bytes: %w", c.OverlayPath, size, err)
			}
		}
	} else {
		if err := qcow2.WriteOverlay(c.OverlayPath, source, size); err != nil {
			return fmt.Errorf("creating the qcow2 overlay %s backed by %s: %w", c.OverlayPath, source, err)
		}
	}

	c.Disks[0] = Disk{
		Path:      c.OverlayPath,
		ReadOnly:  false,
		NumQueues: c.Disks[0].NumQueues,
		IOThreads: c.Disks[0].IOThreads,
		IOEngine:  c.Disks[0].IOEngine,
	}
	return nil
}

func parseFilesystem(spec string) (Filesystem, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Filesystem{}, fmt.Errorf("empty filesystem specification")
	}
	fs := Filesystem{}
	if strings.Contains(spec, "=") {
		for _, part := range strings.Split(spec, ",") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "tag":
				fs.Tag = v
			case "socket":
				fs.Socket = v
			case "shared_dir", "dir", "path":
				fs.SharedDir = v
			case "destination", "dest", "target":
				fs.Destination = v
			case "readonly", "ro":
				b, err := strconv.ParseBool(v)
				if err != nil {
					if v == "on" {
						fs.ReadOnly = true
					} else if v == "off" {
						fs.ReadOnly = false
					} else {
						return Filesystem{}, fmt.Errorf("invalid readonly value %q", v)
					}
				} else {
					fs.ReadOnly = b
				}
			}
		}
	} else {
		parts := strings.Split(spec, ":")
		if len(parts) < 2 {
			return Filesystem{}, fmt.Errorf("invalid filesystem specification %q (expected tag:path or tag=...,shared_dir=...)", spec)
		}
		fs.Tag = parts[0]
		target := parts[1]
		if strings.HasSuffix(target, ".sock") || strings.HasSuffix(target, ".socket") {
			fs.Socket = target
		} else {
			fs.SharedDir = target
		}
		// tag:source[:destination][:ro|rw], told apart by the
		// destination being an absolute path and the mode not.
		rest := parts[2:]
		if len(rest) > 0 && strings.HasPrefix(rest[0], "/") {
			fs.Destination = rest[0]
			rest = rest[1:]
		}
		if len(rest) > 0 {
			switch rest[0] {
			case "ro":
				fs.ReadOnly = true
			case "rw":
				fs.ReadOnly = false
			default:
				return Filesystem{}, fmt.Errorf("invalid filesystem specification %q: %q is neither an absolute destination path nor ro or rw", spec, rest[0])
			}
			rest = rest[1:]
		}
		if len(rest) > 0 {
			return Filesystem{}, fmt.Errorf("invalid filesystem specification %q (expected tag:source[:destination][:ro|rw])", spec)
		}
	}
	if fs.Tag == "" {
		return Filesystem{}, fmt.Errorf("filesystem tag is required in %q", spec)
	}
	if fs.Socket == "" && fs.SharedDir == "" {
		return Filesystem{}, fmt.Errorf("filesystem %q requires either socket or shared_dir", fs.Tag)
	}
	if fs.Destination != "" && !strings.HasPrefix(fs.Destination, "/") {
		return Filesystem{}, fmt.Errorf("filesystem %q's destination %q is not an absolute path inside the container", fs.Tag, fs.Destination)
	}
	return fs, nil
}

// PrepareAuxiliaryDisks ensures that any auxiliary disks in Disks[1:] exist,
// creating sparse raw disk files (1GiB default) and their parent directories
// as needed before VM boot.
func (c *Config) PrepareAuxiliaryDisks() error {
	if len(c.Disks) <= 1 {
		return nil
	}
	for i := 1; i < len(c.Disks); i++ {
		d := c.Disks[i]
		if _, err := os.Stat(d.Path); err != nil {
			if os.IsNotExist(err) {
				if err := os.MkdirAll(filepath.Dir(d.Path), 0o755); err != nil {
					return fmt.Errorf("creating parent directory for auxiliary disk %s: %w", d.Path, err)
				}
				f, err := os.Create(d.Path)
				if err != nil {
					return fmt.Errorf("creating auxiliary disk %s: %w", d.Path, err)
				}
				const defaultAuxDiskSize = 1024 * 1024 * 1024
				if err := f.Truncate(defaultAuxDiskSize); err != nil {
					f.Close()
					return fmt.Errorf("sizing auxiliary disk %s: %w", d.Path, err)
				}
				if err := f.Close(); err != nil {
					return fmt.Errorf("closing auxiliary disk %s: %w", d.Path, err)
				}
			} else {
				return fmt.Errorf("checking auxiliary disk %s: %w", d.Path, err)
			}
		}
	}
	return nil
}

// PrepareContainerOverride writes ContainerOverride onto the disk that
// carries it to the guest and attaches that disk last, after the boot
// disk and every CHV_EXTRA_DISKS entry, so none of theirs move. A Config
// with no override is left alone.
//
// It runs on a restore too: the snapshot's disk list names the disk, so
// it has to be there, and what it carries then only matters to a
// granule-container started again after the restore.
func (c *Config) PrepareContainerOverride() error {
	if c.ContainerOverride == nil {
		return nil
	}
	path := c.ContainerOverrideDisk
	if path == "" {
		path = defaultContainerOverrideDisk
	}
	if err := containerspec.WriteOverrideDisk(path, *c.ContainerOverride); err != nil {
		return err
	}
	for _, d := range c.Disks {
		if d.Path == path {
			return nil
		}
	}
	c.Disks = append(c.Disks, Disk{Path: path, ReadOnly: true, Serial: containerspec.OverrideSerial})
	return nil
}

func parseStorageOptSize(s string) (int, error) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "size=") {
			val := strings.TrimPrefix(part, "size=")
			return parseSizeMBString(val)
		}
	}
	return 0, nil
}

func parseSizeMBString(val string) (int, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, nil
	}
	upper := strings.ToUpper(val)
	var multiplier int64 = 1

	if strings.HasSuffix(upper, "TIB") || strings.HasSuffix(upper, "TB") || strings.HasSuffix(upper, "T") {
		multiplier = 1024 * 1024
		val = strings.TrimRight(val, "tTbBiI ")
	} else if strings.HasSuffix(upper, "GIB") || strings.HasSuffix(upper, "GB") || strings.HasSuffix(upper, "G") {
		multiplier = 1024
		val = strings.TrimRight(val, "gGbBiI ")
	} else if strings.HasSuffix(upper, "MIB") || strings.HasSuffix(upper, "MB") || strings.HasSuffix(upper, "M") {
		multiplier = 1
		val = strings.TrimRight(val, "mMbBiI ")
	} else if strings.HasSuffix(upper, "KIB") || strings.HasSuffix(upper, "KB") || strings.HasSuffix(upper, "K") {
		val = strings.TrimRight(val, "kKbBiI ")
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing storage size %q: %w", val, err)
		}
		return int((n + 1023) / 1024), nil
	}

	n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing storage size %q: %w", val, err)
	}
	return int(n * multiplier), nil
}
