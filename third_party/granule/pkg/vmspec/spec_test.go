package vmspec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/netshim"
)

func baseSpec() VMSpec {
	s := Defaults()
	s.Name = "web"
	s.DiskImage = "/images/disk.img"
	return s
}

func TestValidate_WritableDiskNeedsNoHostPathOrImagesPrefix(t *testing.T) {
	// Both of these used to be errors, and stopped being ones when the
	// overlay moved inside the VM's container: there is no host-side
	// overlay to place, so no -disk-hostpath to require, and no backing
	// file to resolve out here, so no reason the source has to sit under
	// the shared /images mount.
	s := baseSpec()
	s.DiskReadOnly = false
	s.DiskHostPath = ""
	s.DiskImage = "/var/lib/granule/guest/disk.img"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if s.DiskImage != "/var/lib/granule/guest/disk.img" {
		t.Errorf("DiskImage = %q, want it passed through unchanged", s.DiskImage)
	}
}

func TestDiskModeOrDerived(t *testing.T) {
	// -disk-readonly=false has always meant "a private writable disk for
	// this VM", which the overlay mode is; mapping it to persistent
	// instead would silently point every existing caller's guest at the
	// shared image and let two VMs write to one file.
	cases := []struct {
		name string
		mut  func(s *VMSpec)
		want string
	}{
		{"explicit mode wins", func(s *VMSpec) { s.DiskMode = config.DiskModePersistent; s.DiskReadOnly = true }, config.DiskModePersistent},
		{"readonly true", func(s *VMSpec) { s.DiskReadOnly = true }, config.DiskModeReadOnly},
		{"readonly false", func(s *VMSpec) { s.DiskReadOnly = false }, config.DiskModeOverlay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSpec()
			s.DiskMode = ""
			tc.mut(&s)
			if got := s.DiskModeOrDerived(); got != tc.want {
				t.Errorf("DiskModeOrDerived() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidate_RejectsAnUnknownDiskMode(t *testing.T) {
	s := baseSpec()
	s.DiskMode = "writable"
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil, want an error naming the three valid modes")
	}
}

func TestValidate_RequiresCore(t *testing.T) {
	cases := []struct {
		name string
		mut  func(s *VMSpec)
	}{
		{"name", func(s *VMSpec) { s.Name = "" }},
		{"name too long for tap device", func(s *VMSpec) { s.Name = "way-too-long-a-name" }},
		{"cpus", func(s *VMSpec) { s.CPUs = 0 }},
		{"memory", func(s *VMSpec) { s.MemoryMB = 1 }},
		{"kernel+firmware", func(s *VMSpec) { s.Kernel = "/images/vmlinux"; s.Firmware = "/images/CLOUDHV.fd" }},
		{"bad shutdown timeout", func(s *VMSpec) { s.ShutdownTimeout = "banana" }},
		{"bad control cidr", func(s *VMSpec) { s.ControlCIDR = "not-a-cidr" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSpec()
			tc.mut(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("Validate() = nil, want an error")
			}
		})
	}
}

func TestValidate_NoDiskMeansTheImagesOwnGuest(t *testing.T) {
	// An empty DiskImage is not a missing field: it means the guest
	// baked into the granule image, which is what a VM booted from a
	// customized granule image runs. There is no host path to name and no
	// shared backing file to overlay, so the writable-disk rules below
	// must not apply to it either.
	s := baseSpec()
	s.DiskImage = ""
	s.DiskReadOnly = false
	s.DiskHostPath = ""
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a spec with no disk of its own", err)
	}
	if s.DiskImage != "" {
		t.Errorf("DiskImage = %q, want it left empty so granule run's own default applies", s.DiskImage)
	}
}

func TestValidate_TapNameLengthBoundary(t *testing.T) {
	// "tap-" (4 chars) + name must fit in 15 chars (IFNAMSIZ-1), so an
	// 11-character name is the longest that still fits.
	s := baseSpec()
	s.Name = "twelvecharzz"[:11]
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for an 11-character name", err)
	}
	s.Name = "twelvecharzz"[:12]
	if err := s.Validate(); err == nil {
		t.Errorf("Validate() = nil, want an error for a 12-character name")
	}
}

func TestValidate_RejectsANameThatIsNotADNS1123Label(t *testing.T) {
	// A name becomes a tap device name, a docker container name and a
	// file name (see nameRE). Docker happens to fail on something like
	// "../evil" today, on the container name -- but what it says never
	// mentions the name, and the check is docker's rather than granule's.
	// This one is granule's.
	for _, name := range []string{
		"",
		"-web",
		"web-",
		"Web",
		"web.1",
		"../evil",
		"web/1",
		"web_1",
	} {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			s.Name = name
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for name %q", name)
			}
			// The empty name keeps its own "name is required"; every
			// other shape should be told what a name has to look like.
			if name != "" && !strings.Contains(err.Error(), "DNS-1123 label") {
				t.Errorf("Validate() error = %v, want it to say the name must be a DNS-1123 label", err)
			}
		})
	}
}

// TestValidateName_IsTheSameCheckValidateMakes: the name check is
// exported so the subcommands that never build a spec ("vm delete", "vm
// status", "vm exec", "vm wait") can make it too, and the point of
// exporting it rather than repeating it is that all of them refuse the
// same names in the same words.
func TestValidateName_IsTheSameCheckValidateMakes(t *testing.T) {
	for _, name := range []string{"", "-web", "Web", "../evil", "web/1", "web_1"} {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err == nil {
				t.Fatalf("ValidateName(%q) = nil, want an error", name)
			}
			s := baseSpec()
			s.Name = name
			specErr := s.Validate()
			if specErr == nil {
				t.Fatalf("Validate() = nil for name %q, want the same error", name)
			}
			if specErr.Error() != err.Error() {
				t.Errorf("Validate() error = %v, ValidateName() error = %v, want the same message", specErr, err)
			}
		})
	}
	for _, name := range []string{"web", "worker", "gb-a1b2c3", "web1"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) error = %v, want nil", name, err)
			}
		})
	}
}

func TestValidate_AcceptsTheNamesTheREADMEUses(t *testing.T) {
	// The names the README's and CI's flows create VMs under, plus the
	// one guestbuild generates for its throwaway build VM (buildName's
	// "gb-" + six lower-case alphanumerics).
	for _, name := range []string{"web", "worker", "oneoff", "oneoff2", "verify", "gb-a1b2c3"} {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			s.Name = name
			if err := s.Validate(); err != nil {
				t.Errorf("Validate() error = %v, want nil for name %q", err, name)
			}
		})
	}
}

func TestValidate_TooLongAValidLabelStillGetsTheTapLengthMessage(t *testing.T) {
	// The label check comes first, but it must not swallow a name that
	// is a perfectly good label and simply too long for an interface
	// name: that error names the tap device and the 15-character limit,
	// which is far more use than "must be a DNS-1123 label" would be.
	s := baseSpec()
	s.Name = "way-too-long-a-name"
	err := s.Validate()
	if err == nil {
		t.Fatalf("Validate() = nil, want an error for %q", s.Name)
	}
	if !strings.Contains(err.Error(), "tap-"+s.Name) {
		t.Errorf("Validate() error = %v, want it to name the over-length tap device", err)
	}
}

func TestValidate_Minimal(t *testing.T) {
	s := baseSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// Kernel is left unset, but so is Firmware: this still means direct
	// kernel boot, via granule run's own baked-in CHV_KERNEL default (see
	// pkg/config's defaultKernel) -- so Cmdline is still
	// auto-derived here, the same as if Kernel had been given explicitly.
	//
	// "rw", because the default disk mode is the overlay: the root a
	// stock guest boots is its own writable copy.
	want := "console=" + config.GuestConsole() + " root=/dev/vda rw noresume quiet loglevel=3"
	if s.Cmdline != want {
		t.Errorf("Cmdline = %q, want %q", s.Cmdline, want)
	}
	if !s.CmdlineAuto {
		t.Errorf("CmdlineAuto = false, want true")
	}
}

func TestValidate_FirmwareSkipsAutoCmdline(t *testing.T) {
	s := baseSpec()
	s.Firmware = "/images/firmware.fd"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if s.Cmdline != "" {
		t.Errorf("Cmdline = %q, want empty (firmware boot, not direct kernel boot)", s.Cmdline)
	}
	if s.CmdlineAuto {
		t.Errorf("CmdlineAuto = true, want false for firmware boot")
	}
}

func TestValidate_AutoCmdline(t *testing.T) {
	s := baseSpec()
	s.Kernel = "/images/vmlinux"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	want := "console=" + config.GuestConsole() + " root=/dev/vda rw noresume quiet loglevel=3"
	if s.Cmdline != want {
		t.Errorf("Cmdline = %q, want %q", s.Cmdline, want)
	}
	if !s.CmdlineAuto {
		t.Errorf("CmdlineAuto = false, want true")
	}
}

func TestValidate_ExplicitCmdlineNotOverridden(t *testing.T) {
	s := baseSpec()
	s.Kernel = "/images/vmlinux"
	s.Cmdline = "console=ttyS0 root=/dev/vda ro custom"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if s.Cmdline != "console=ttyS0 root=/dev/vda ro custom" {
		t.Errorf("Cmdline was overwritten: %q", s.Cmdline)
	}
	if s.CmdlineAuto {
		t.Errorf("CmdlineAuto = true, want false for an explicit cmdline")
	}
}

func TestValidate_AutoCmdlineUsesROInReadOnlyMode(t *testing.T) {
	// The mirror of TestValidate_AutoCmdline, which gets "rw" from the
	// default overlay mode: read-only is now the mode that has to be
	// asked for, and asking for it is what still mounts the root "ro".
	s := baseSpec()
	s.Kernel = "/images/vmlinux"
	s.DiskMode = config.DiskModeReadOnly
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := s.Cmdline; !strings.Contains(got, "root=/dev/vda ro") {
		t.Errorf("Cmdline = %q, want it to contain %q", got, "root=/dev/vda ro")
	}
}

func TestValidate_AutoCmdlineNeverWaitsForAResumeDevice(t *testing.T) {
	// A granule microVM is booted fresh from a packed image every time
	// and never hibernates: it has no swap and no hibernation image, so
	// there is never a resume to do. Without noresume, a Debian initramfs
	// waits ~105s for a resume device before giving up.
	for _, mode := range []string{config.DiskModeOverlay, config.DiskModeReadOnly} {
		t.Run(mode, func(t *testing.T) {
			s := baseSpec()
			s.DiskMode = mode
			if err := s.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !strings.Contains(s.Cmdline, "noresume") {
				t.Errorf("Cmdline = %q, want it to contain noresume", s.Cmdline)
			}
		})
	}
}

// TestDefaults_BootableDiskMode pins the default a VM gets when nothing
// is passed: the overlay, the only mode a stock guest finishes booting
// from. It defaulted to read-only until this was fixed, so every docker
// caller had to pass -disk-mode overlay to get a VM that came up at all.
func TestDefaults_BootableDiskMode(t *testing.T) {
	if got, want := Defaults().DiskModeOrDerived(), config.DiskModeOverlay; got != want {
		t.Errorf("Defaults() disk mode = %q, want %q", got, want)
	}
}

// TestResolveSettings is §5.5's ordering, which is the whole point of the
// method: what the caller asked for, then what the machine asks for, then
// granule's own defaults.
func TestResolveSettings(t *testing.T) {
	// A machine built with "VM SET cpus=2 cpus-max=4 memory-mb=1024
	// memory-max-mb=2048 disk-mode=persistent", as the build records it.
	machine := []string{
		"PATH=/usr/bin:/bin",
		"CHV_CPUS=2",
		"CHV_CPUS_MAX=4",
		"CHV_MEMORY_MB=1024",
		"CHV_MEMORY_MAX_MB=2048",
		"CHV_DISK_MODE=persistent",
	}
	cases := []struct {
		name  string
		spec  VMSpec
		env   []string
		want  VMSpec
		about string
	}{
		{
			name:  "the machine's own",
			env:   machine,
			want:  VMSpec{CPUs: 2, CPUsMax: 4, MemoryMB: 1024, MemoryMaxMB: 2048, DiskMode: config.DiskModePersistent},
			about: "a caller who asked for nothing gets the machine as it was built",
		},
		{
			name:  "the caller overrides",
			spec:  VMSpec{CPUs: 4, MemoryMB: 2048, DiskMode: config.DiskModeOverlay},
			env:   machine,
			want:  VMSpec{CPUs: 4, CPUsMax: 4, MemoryMB: 2048, MemoryMaxMB: 2048, DiskMode: config.DiskModeOverlay},
			about: "the runtime environment overrides the image's defaults, and only the settings it names",
		},
		{
			name:  "an image that records nothing",
			env:   []string{"PATH=/usr/bin:/bin", "CHV_DISK_MODE="},
			want:  VMSpec{CPUs: DefaultCPUs, MemoryMB: DefaultMemoryMB},
			about: "granule's own defaults, and no hotplug headroom; an ENV with no value is not a setting",
		},
		{
			name:  "nothing to read at all",
			want:  VMSpec{CPUs: DefaultCPUs, MemoryMB: DefaultMemoryMB},
			about: "an image whose config could not be read is still booted",
		},
		{
			name:  "values granule cannot read",
			env:   []string{"CHV_CPUS=lots", "CHV_MEMORY_MB=0", "CHV_MEMORY_MAX_MB=-1"},
			want:  VMSpec{CPUs: DefaultCPUs, MemoryMB: DefaultMemoryMB},
			about: "somebody else's image, so an unreadable setting is left to the VM container to report on",
		},
		{
			name: "the rest of §5.5's table",
			env: []string{
				"CHV_CMDLINE=quiet",
				"CHV_SHUTDOWN_TIMEOUT=45s",
				"CHV_DISK_SIZE_MB=2048",
				"CHV_EXTENT_SIZE_MB=128",
				"CHV_DISK_NUM_QUEUES=4",
				"CHV_DISK_IO_ENGINE=io_uring",
				"CHV_DISK_IOTHREADS=on",
			},
			want: VMSpec{
				CPUs: DefaultCPUs, MemoryMB: DefaultMemoryMB,
				Cmdline: "quiet", ShutdownTimeout: "45s",
				DiskSizeMB: 2048, ExtentSizeMB: 128, DiskNumQueues: 4,
				DiskIOEngine: "io_uring", DiskIOThreads: true,
			},
			about: "every setting granule passes to the VM container explicitly, not just the sizing",
		},
		{
			name:  "the vcpus spelling",
			env:   []string{"CHV_VCPUS=3"},
			want:  VMSpec{CPUs: 3, MemoryMB: DefaultMemoryMB},
			about: "§5.5 gives both spellings, and a machine recording only vcpus still has a vCPU count",
		},
		{
			name:  "cpus beats vcpus",
			env:   []string{"CHV_VCPUS=3", "CHV_CPUS=5"},
			want:  VMSpec{CPUs: 5, MemoryMB: DefaultMemoryMB},
			about: "read the way the VM container reads it, whichever order they appear in",
		},
		{
			name: "settings granule cannot make sense of",
			env: []string{
				"CHV_DISK_MODE=writable",
				"CHV_DISK_IO_ENGINE=telepathy",
				"CHV_SHUTDOWN_TIMEOUT=soon",
				"CHV_DISK_IOTHREADS=maybe",
			},
			want:  VMSpec{CPUs: DefaultCPUs, MemoryMB: DefaultMemoryMB},
			about: "somebody else's image: left out rather than guessed at, and never turned into a spec Validate refuses",
		},
		{
			name:  "a ceiling the caller booted past",
			spec:  VMSpec{CPUs: 8, MemoryMB: 4096},
			env:   machine,
			want:  VMSpec{CPUs: 8, MemoryMB: 4096, DiskMode: config.DiskModePersistent},
			about: "the machine's ceilings were drawn around its own boot size, and the caller's wins",
		},
		{
			name:  "a ceiling the caller contradicted themselves",
			spec:  VMSpec{CPUs: 8, CPUsMax: 4},
			env:   nil,
			want:  VMSpec{CPUs: 8, CPUsMax: 4, MemoryMB: DefaultMemoryMB},
			about: "their own contradiction, left for Validate to report rather than silently dropped",
		},
		{
			name:  "a ceiling below granule's default boot size",
			env:   []string{"CHV_MEMORY_MAX_MB=1024"},
			want:  VMSpec{CPUs: DefaultCPUs, MemoryMB: 1024, MemoryMaxMB: 1024},
			about: "a machine that named only a ceiling is not asking to boot above it",
		},
		{
			name:  "the deprecated -disk-readonly still decides the disk",
			spec:  VMSpec{DiskReadOnly: true},
			env:   machine,
			want:  VMSpec{DiskReadOnly: true, CPUs: 2, CPUsMax: 4, MemoryMB: 1024, MemoryMaxMB: 2048},
			about: "it names no mode of its own, so an empty DiskMode here is not an absence of opinion",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec
			got.ResolveSettings(tc.env)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResolveSettings() = %+v, want %+v (%s)", got, tc.want, tc.about)
			}
		})
	}
}

// TestResolveSettings_LeavesASpecValid guards the invariant Validate relies
// on: whatever the image says or doesn't, a resolved spec has a real vCPU
// count and memory size, because the VM container's own defaults are
// smaller than granule's and a gap passed on to it would shrink the guest
// rather than fail.
func TestResolveSettings_LeavesASpecValid(t *testing.T) {
	s := baseSpec()
	s.CPUs, s.MemoryMB = 0, 0
	if err := s.Validate(); err == nil {
		t.Error("Validate() on an unresolved spec = nil error, want it refused rather than passed on")
	}
	s.CPUs, s.MemoryMB = 0, 0
	s.ResolveSettings(nil)
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() after ResolveSettings() error = %v, want nil", err)
	}
}

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := baseSpec()

	if _, err := Load(dir, s.Name); err == nil {
		t.Fatalf("Load() before Save() = nil error, want not-found")
	}

	if err := Save(dir, s); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(dir, s.Name)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("Load() = %+v, want %+v", got, s)
	}

	if err := Delete(dir, s.Name); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := Load(dir, s.Name); err == nil {
		t.Fatalf("Load() after Delete() = nil error, want not-found")
	}
	// Deleting something already gone is not an error.
	if err := Delete(dir, s.Name); err != nil {
		t.Errorf("Delete() of already-deleted VM error = %v, want nil", err)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()

	specs, err := List(dir)
	if err != nil {
		t.Fatalf("List() on missing dir error = %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("List() on missing dir = %v, want empty", specs)
	}

	web := baseSpec()
	worker := baseSpec()
	worker.Name = "worker"
	if err := Save(dir, web); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, worker); err != nil {
		t.Fatal(err)
	}

	specs, err = List(dir)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(specs) != 2 || specs[0].Name != "web" || specs[1].Name != "worker" {
		t.Errorf("List() = %+v, want [web, worker] sorted by name", specs)
	}
}

// TestValidate_NeedsNoAddressOrPort covers what the spec deliberately no
// longer carries: the container runtime assigns the address the guest
// takes over, and ports are published on the sandbox itself.
func TestValidate_NeedsNoAddressOrPort(t *testing.T) {
	s := baseSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// The address is only knowable once the sandbox exists, so the
	// derived cmdline must leave ip= out for the VM container to append.
	if strings.Contains(s.Cmdline, "ip=") {
		t.Errorf("Cmdline = %q, want no ip= parameter", s.Cmdline)
	}
	if !s.CmdlineAuto {
		t.Errorf("CmdlineAuto = false, want the derived cmdline to be marked automatic")
	}
}

func TestValidate_RejectsNameTooLongForControlTap(t *testing.T) {
	s := baseSpec()
	s.Name = "twelvechars1"
	if err := s.Validate(); err == nil {
		t.Errorf("Validate() = nil, want an error: %q would exceed the 15-character interface name limit", "ctl-"+s.Name)
	}
}

// A relative ready file is checked inside the guest, in a session that
// starts in the account's home directory -- so it would name a different
// file per account and the VM would simply never report ready. Refused
// at "vm create" time, where the flag can be named, rather than 3
// minutes later as a wait that timed out.
func TestValidate_RejectsARelativeReadyFile(t *testing.T) {
	s := baseSpec()
	s.ReadyFile = "run/kontur/ready"
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want a relative ready file refused")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("Validate() = %v, want it to say the path must be absolute", err)
	}
}

func TestValidate_ReadyFileIsOptional(t *testing.T) {
	s := baseSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if s.ReadyFile != "" {
		t.Errorf("ReadyFile = %q, want a VM with no declaration configured to have none", s.ReadyFile)
	}
}

func TestValidate_DiskSizeNeedsOverlayMode(t *testing.T) {
	// The overlay is the only disk granule creates for a VM. In the other
	// two modes the guest boots the image itself, which is shared with
	// every other VM using it and never resized -- so a disk size asked
	// for there is a mistake worth reporting at "vm create" time rather
	// than a container crash loop later.
	for _, mode := range []string{config.DiskModePersistent, config.DiskModeReadOnly} {
		t.Run(mode, func(t *testing.T) {
			s := baseSpec()
			s.DiskReadOnly = false
			s.DiskMode = mode
			s.DiskSizeMB = 8192
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for a disk size in %q mode", mode)
			}
			if !strings.Contains(err.Error(), config.DiskModeOverlay) {
				t.Errorf("Validate() error = %v, want it to name %q as the mode that can be sized", err, config.DiskModeOverlay)
			}
		})
	}

	s := baseSpec()
	s.DiskMode = config.DiskModeOverlay
	s.DiskSizeMB = 8192
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want a disk size accepted in %q mode", err, config.DiskModeOverlay)
	}

	s = baseSpec()
	s.DiskMode = config.DiskModeOverlay
	s.DiskSizeMB = -1
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil for a negative disk size, want an error")
	}
}

func TestValidate_ExtentSizeMB(t *testing.T) {
	for _, mode := range []string{config.DiskModePersistent, config.DiskModeReadOnly} {
		t.Run(mode, func(t *testing.T) {
			s := baseSpec()
			s.DiskReadOnly = false
			s.DiskMode = mode
			s.ExtentSizeMB = 128
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for extent size in %q mode", mode)
			}
			if !strings.Contains(err.Error(), config.DiskModeOverlay) {
				t.Errorf("Validate() error = %v, want it to name %q as the mode that can have extents", err, config.DiskModeOverlay)
			}
		})
	}

	s := baseSpec()
	s.DiskMode = config.DiskModeOverlay
	s.ExtentSizeMB = 128
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want extent size accepted in %q mode", err, config.DiskModeOverlay)
	}

	s = baseSpec()
	s.DiskMode = config.DiskModeOverlay
	s.ExtentSizeMB = -1
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil for a negative extent size, want an error")
	}
}

// The nameservers are validated on this side even though the ip=
// parameter carrying them is assembled inside the VM container. An
// address rejected there would fail a VM that "vm create" had already
// reported as started, which is a much worse place to learn about a typo.
func TestValidate_RejectsNameserversTheGuestCannotBeGiven(t *testing.T) {
	for _, dns := range []string{"nameserver", "8.8.8.8,1.1.1.1,9.9.9.9", "fd00::1,fd00::2,fd00::3,8.8.8.8"} {
		s := baseSpec()
		s.DNS = dns
		if err := s.Validate(); err == nil {
			t.Errorf("Validate() with dns %q = nil, want an error", dns)
		}
	}

	// IPv6 is carried on granule.dns=, for a v6-only guest or beside
	// IPv4 ones on a dual-stack one.
	for _, dns := range []string{"2001:4860:4860::8888", "fd00:10:96::a,10.96.0.10"} {
		s := baseSpec()
		s.DNS = dns
		if err := s.Validate(); err != nil {
			t.Errorf("Validate() with dns %q = %v, want it accepted", dns, err)
		}
	}

	// Empty is not a mistake: it is how a caller keeps whatever resolver
	// the guest image ships with.
	s := baseSpec()
	s.DNS = ""
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() with no dns = %v, want it accepted", err)
	}

	// "auto" is resolved inside the VM container, against its own
	// resolv.conf, and is the default.
	s = baseSpec()
	s.DNS = netshim.AutoDNS
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() with dns auto = %v, want it accepted", err)
	}
	if d := Defaults().DNS; d != netshim.AutoDNS {
		t.Errorf("Defaults().DNS = %q, want %q", d, netshim.AutoDNS)
	}
}

// memAgentSpec is a spec the mem agent could actually work on: a ceiling
// above the boot size for it to grow into, and the control link the
// guest's signals arrive over.
func memAgentSpec() VMSpec {
	s := baseSpec()
	s.MemAgent = true
	s.MemoryMB = 512
	s.MemoryMaxMB = 1024
	return s
}

func TestValidate_MemAgent(t *testing.T) {
	s := memAgentSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a mem-agent VM with headroom accepted", err)
	}

	// The tuning is optional: unset means the VM container's own
	// defaults, which are what the guest image is built to signal.
	s = memAgentSpec()
	s.MemAgentAddr = "169.254.100.1:30090"
	s.MemAgentStepMB = 128
	s.MemAgentCooldown = "45s"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want the tuning accepted", err)
	}
}

// docker run takes the image positionally after granule's own flags, so
// an image that looks like a flag would become one.
func TestValidate_KonturImageThatLooksLikeAFlag(t *testing.T) {
	s := Defaults()
	s.Name = "vm1"
	s.KonturImage = "--privileged"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "kontur image") {
		t.Fatalf("Validate() error = %v, want a refusal naming the kontur image", err)
	}
	s.KonturImage = "registry.example/granule:v1"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want an ordinary image reference accepted", err)
	}
}

// The token is one whitespace-separated field of a signal line, so a
// token with whitespace in it could never match.
func TestValidate_MemAgentTokenWithWhitespace(t *testing.T) {
	s := memAgentSpec()
	s.MemAgentToken = "abc init=/bin/sh"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "mem-agent-token") {
		t.Fatalf("Validate() error = %v, want a refusal naming mem-agent-token", err)
	}
	s = memAgentSpec()
	s.MemAgentToken = "0123abcd"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a plain token accepted", err)
	}
}

// The window virtio-mem grows into is sized once, at boot, from the gap
// between the boot size and the ceiling. Without a gap the agent comes
// up, is signalled and can never grow anything -- and at granule's own
// -memory-mb default the VM container's default ceiling is exactly the
// boot size, so this is the combination a caller who passes -mem-agent
// and nothing else would get.
func TestValidate_MemAgentNeedsAMemoryCeiling(t *testing.T) {
	for _, max := range []int{0, 512} {
		s := memAgentSpec()
		s.MemoryMaxMB = max
		err := s.Validate()
		if err == nil {
			t.Fatalf("Validate() with memory-max-mb %d = nil, want it refused", max)
		}
		if !strings.Contains(err.Error(), "memory-max-mb") {
			t.Errorf("Validate() error = %v, want it to name memory-max-mb", err)
		}
	}
}

// The guest dials the listener over netshim's control link; with the link
// off there is no route into the VM container's namespace at all, so the
// default address points at nothing. A caller who names an address of
// their own is taken at their word.
func TestValidate_MemAgentNeedsTheControlLink(t *testing.T) {
	s := memAgentSpec()
	s.ControlCIDR = ""
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() with no control link = nil, want the mem agent refused")
	}
	if !strings.Contains(err.Error(), "control") {
		t.Errorf("Validate() error = %v, want it to name the control link", err)
	}

	s.MemAgentAddr = "10.0.0.1:30090"
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want an explicit address accepted with no control link", err)
	}
}

// Malformed tuning is caught here rather than in the VM container, where
// it would surface as a crash loop after "vm create" had already reported
// the VM started.
func TestValidate_RejectsMalformedMemAgentTuning(t *testing.T) {
	cases := []struct {
		name string
		mut  func(s *VMSpec)
		want string
	}{
		{"address without a port", func(s *VMSpec) { s.MemAgentAddr = "169.254.100.1" }, "mem-agent-addr"},
		{"unparseable cooldown", func(s *VMSpec) { s.MemAgentCooldown = "30" }, "mem-agent-cooldown"},
		{"negative cooldown", func(s *VMSpec) { s.MemAgentCooldown = "-30s" }, "mem-agent-cooldown"},
		{"negative step", func(s *VMSpec) { s.MemAgentStepMB = -1 }, "mem-agent-step-mb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := memAgentSpec()
			tc.mut(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want %s refused", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() error = %v, want it to name %s", err, tc.want)
			}

			// The same value is refused with the agent off, since a
			// value that cannot be parsed is a mistake either way.
			s.MemAgent = false
			s.MemoryMaxMB = 0
			if err := s.Validate(); err == nil {
				t.Errorf("Validate() with the agent off = nil, want %s refused there too", tc.want)
			}
		})
	}

	// Turning the agent off must not drag the rest of Validate down with
	// it: the tuning from the previous create stays in the saved spec,
	// and "granule vm update -mem-agent=false" has to be able to save
	// that.
	s := memAgentSpec()
	s.MemAgent = false
	s.MemoryMaxMB = 0
	s.MemAgentStepMB = 128
	s.MemAgentCooldown = "45s"
	s.MemAgentAddr = "169.254.100.1:30090"
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want the tuning kept harmlessly with the agent off", err)
	}
}

func TestValidate_DiskTuningParameters(t *testing.T) {
	s := baseSpec()
	s.DiskNumQueues = 4
	s.DiskIOThreads = true
	s.DiskIOEngine = "io_uring"
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want valid tuning accepted", err)
	}

	// Negative num queues
	s = baseSpec()
	s.DiskNumQueues = -1
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil for negative DiskNumQueues, want error")
	}

	// Invalid IO engine
	s = baseSpec()
	s.DiskIOEngine = "invalid"
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil for invalid DiskIOEngine, want error")
	}

	// Controlled environment variables in Env
	for _, envKey := range []string{"CHV_DISK_NUM_QUEUES=4", "CHV_DISK_IOTHREADS=true", "CHV_DISK_IO_ENGINE=io_uring", "CHV_VCPUS=2"} {
		s = baseSpec()
		s.Env = []string{envKey}
		if err := s.Validate(); err == nil {
			t.Errorf("Validate() = nil for controlled env %q, want error", envKey)
		}
	}
}

// A saved spec carries the VM's Env and memory-agent token, so it must
// not be readable by other accounts -- including when an older granule
// already left the file behind world-readable.
func TestSave_IsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, VMSpec{Name: "web", Env: []string{"TOKEN=secret"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("saved spec mode = %04o, want 0600", got)
	}
	got, err := Load(dir, "web")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Env) != 1 || got.Env[0] != "TOKEN=secret" {
		t.Errorf("Load().Env = %v, want [TOKEN=secret]", got.Env)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("state directory holds %d entries after Save, want just the spec", len(entries))
	}
}

func TestValidate_IPv6(t *testing.T) {
	for _, v := range []string{"", "auto", "off"} {
		s := baseSpec()
		s.IPv6 = v
		if err := s.Validate(); err != nil {
			t.Errorf("IPv6 %q: Validate() error = %v", v, err)
		}
	}
	s := baseSpec()
	s.IPv6 = "on"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "ipv6") {
		t.Errorf("IPv6 \"on\": Validate() error = %v, want one naming ipv6", err)
	}
	s = baseSpec()
	s.Env = []string{"NETSHIM_IPV6=off"}
	if err := s.Validate(); err == nil {
		t.Errorf("-e NETSHIM_IPV6: Validate() error = nil, want the flag required instead")
	}
	if d := Defaults().IPv6; d != netshim.IPv6Auto {
		t.Errorf("Defaults().IPv6 = %q, want %q", d, netshim.IPv6Auto)
	}
}

func TestMemoryLimitAndOOMScoreAdj(t *testing.T) {
	if got := Defaults().EffectiveOOMScoreAdj(); got != DefaultOOMScoreAdj {
		t.Errorf("Defaults().EffectiveOOMScoreAdj() = %d, want %d", got, DefaultOOMScoreAdj)
	}
	if got := (VMSpec{}).EffectiveOOMScoreAdj(); got != DefaultOOMScoreAdj {
		t.Errorf("zero VMSpec.EffectiveOOMScoreAdj() = %d, want %d", got, DefaultOOMScoreAdj)
	}
	if got := (VMSpec{OOMScoreAdj: 700}).EffectiveOOMScoreAdj(); got != 700 {
		t.Errorf("explicit VMSpec.EffectiveOOMScoreAdj() = %d, want 700", got)
	}

	if got := (VMSpec{MemoryMB: 8192}).ContainerMemoryLimitMB(); got != 8192+VMMMemoryOverheadMB {
		t.Errorf("ContainerMemoryLimitMB() = %d, want %d", got, 8192+VMMMemoryOverheadMB)
	}
	if got := (VMSpec{MemoryMB: 2048, MemoryMaxMB: 4096}).ContainerMemoryLimitMB(); got != 4096+VMMMemoryOverheadMB {
		t.Errorf("ContainerMemoryLimitMB() with hotplug ceiling = %d, want %d", got, 4096+VMMMemoryOverheadMB)
	}
	if got := (VMSpec{MemoryMB: 8192, MemoryLimitMB: 9000}).ContainerMemoryLimitMB(); got != 9000 {
		t.Errorf("ContainerMemoryLimitMB() with explicit limit = %d, want 9000", got)
	}

	s := baseSpec()
	s.MemoryLimitMB = -1
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "memory-limit-mb") {
		t.Errorf("negative MemoryLimitMB: Validate() = %v, want memory-limit-mb error", err)
	}
	s = baseSpec()
	s.MemoryMB = 2048
	s.MemoryLimitMB = 1024
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "memory-limit-mb") {
		t.Errorf("MemoryLimitMB < MemoryMB: Validate() = %v, want memory-limit-mb error", err)
	}
	s = baseSpec()
	s.OOMScoreAdj = 1001
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "oom-score-adj") {
		t.Errorf("OOMScoreAdj > 1000: Validate() = %v, want oom-score-adj error", err)
	}
}
