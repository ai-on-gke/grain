package containerspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A wrapped image's spec is fixed when it is wrapped, but one image is
// often run as many containers, each with a process of its own -- the
// way a containerd container spec, or a `docker run` command line, gives
// the same image a different entrypoint, command, environment, user or
// working directory every time. An Override is that per-container half:
// supplied to the VM's container on the host at start time
// (CHV_CONTAINER_OVERRIDE, read by pkg/config) and merged over the baked
// spec in the guest by granule-container, without rebuilding the image.
//
// # How it reaches the guest
//
// The host writes it onto a small read-only virtio-blk device of its own
// (WriteOverrideDisk), attached after every other disk with OverrideSerial
// as its serial, and the guest finds it by that serial
// (FindOverrideDevice). A disk rather than the alternatives because:
//
//   - the kernel command line is a few kilobytes at most and is readable
//     by every process in the guest, and an environment is easily larger;
//   - virtio-fs needs a virtiofsd running beside the VM for one file;
//   - a vsock fetch, either way round, races the boot: granule-container
//     starts as soon as the network is up, and would have to wait for a
//     host that may never call, or the host would need a listener of its
//     own that a restored VM's snapshot knows nothing about.
//
// A disk is there before the guest's first instruction, needs no daemon
// on either side, and is part of the VM's configuration -- so a
// snapshotted VM's restore replays it like any other disk. It is attached
// last so that the device names CHV_EXTRA_DISKS has always produced
// (vdb, vdc, ...) do not move.
//
// The device's contents are OverrideMagic, a newline, the override as
// JSON, and zero padding to a whole number of sectors. The magic is what
// makes a device the override rather than a disk that happens to share
// its serial, and lets a guest whose kernel exposes no serial in sysfs
// find it anyway.
type Override struct {
	// Entrypoint replaces the image's ENTRYPOINT when it is not nil, and
	// an empty (non-nil) one clears it, as `docker run --entrypoint ""`
	// does. Giving an Entrypoint drops the image's Cmd too unless Cmd is
	// given as well, which is how a container runtime composes them: the
	// image's command was written as arguments to the image's entrypoint,
	// not to somebody else's.
	Entrypoint []string `json:"entrypoint"`

	// Cmd replaces the image's CMD when it is not nil.
	Cmd []string `json:"cmd"`

	// Env is KEY=VALUE entries set over the image's environment: one
	// naming a variable the image sets replaces it, and the rest are
	// added, as `docker run -e` does. An override cannot unset a variable
	// the image sets; it can set it empty.
	Env []string `json:"env,omitempty"`

	// User replaces the image's USER when it is not empty, in any of the
	// forms Spec.User accepts.
	User string `json:"user,omitempty"`

	// WorkingDir replaces the image's WORKDIR when it is not empty.
	WorkingDir string `json:"working_dir,omitempty"`

	// Stdin, when not nil, sets Spec.OpenStdin: whether this container's
	// process gets a stdin the host can attach to.
	Stdin *bool `json:"stdin,omitempty"`

	// Tty, when not nil, sets Spec.Tty: whether this container's process
	// runs on a terminal.
	Tty *bool `json:"tty,omitempty"`

	// Mounts are virtio-fs shares to mount inside the container, added
	// to any the spec already has (Spec.Mounts). pkg/config fills them
	// in from the destinations given in CHV_FS and CHV_VOLUMES.
	Mounts []Mount `json:"mounts,omitempty"`
}

const (
	// OverrideSerial is the virtio-blk serial the override device carries
	// (at most 20 bytes, virtio-blk's limit).
	OverrideSerial = "granule-ctr-override"

	// OverrideMagic is the first line of the override device.
	OverrideMagic = "granule-container-override/v1"

	// EffectiveConfigPath is where granule-container writes the spec it
	// actually runs -- the baked one with any override merged in -- for
	// granule-agent's container exec to enter the same container with.
	// Under /run because it is per boot: an override is the VM's, and the
	// next boot may carry another or none.
	EffectiveConfigPath = "/run/granule/container.json"

	// maxOverrideSize bounds what is read from a device that claims to be
	// the override: far more than any process's arguments and
	// environment, and far less than a disk a guest might be handed by
	// mistake.
	maxOverrideSize = 1 << 20

	sectorSize = 512
)

// IsZero reports whether the override changes nothing.
func (o Override) IsZero() bool {
	return o.Entrypoint == nil && o.Cmd == nil && len(o.Env) == 0 && o.User == "" && o.WorkingDir == "" &&
		o.Stdin == nil && o.Tty == nil && len(o.Mounts) == 0
}

// Validate rejects an override that could only produce an unusable spec,
// so that the host refuses it at start rather than the guest at boot.
func (o Override) Validate() error {
	if o.WorkingDir != "" && !strings.HasPrefix(o.WorkingDir, "/") {
		return fmt.Errorf("the working directory %q is not an absolute path inside the container", o.WorkingDir)
	}
	for _, e := range o.Env {
		if key, _, ok := strings.Cut(e, "="); !ok || key == "" {
			return fmt.Errorf("the environment entry %q is not KEY=VALUE", e)
		}
	}
	return validateMounts(o.Mounts)
}

// ParseOverride reads an override as JSON, refusing fields it does not
// know rather than ignoring them: a misspelt "workdir" silently running
// the image's own would be the worse answer.
func ParseOverride(data []byte) (Override, error) {
	var o Override
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return Override{}, fmt.Errorf("parsing the container override: %w", err)
	}
	if dec.More() {
		return Override{}, errors.New("parsing the container override: trailing data after the JSON object")
	}
	if err := o.Validate(); err != nil {
		return Override{}, fmt.Errorf("the container override is unusable: %w", err)
	}
	return o, nil
}

// Apply is s with o merged over it.
func (s Spec) Apply(o Override) Spec {
	switch {
	case o.Entrypoint != nil:
		s.Entrypoint = append([]string{}, o.Entrypoint...)
		s.Cmd = nil
		if o.Cmd != nil {
			s.Cmd = append([]string{}, o.Cmd...)
		}
	case o.Cmd != nil:
		s.Cmd = append([]string{}, o.Cmd...)
	}
	if len(o.Env) > 0 {
		env := append([]string{}, s.Env...)
		for _, e := range o.Env {
			env = setEnvEntry(env, e)
		}
		s.Env = env
	}
	if o.User != "" {
		s.User = o.User
	}
	if o.WorkingDir != "" {
		s.WorkingDir = o.WorkingDir
	}
	if o.Stdin != nil {
		s.OpenStdin = *o.Stdin
	}
	if o.Tty != nil {
		s.Tty = *o.Tty
	}
	if len(o.Mounts) > 0 {
		s.Mounts = append(append([]Mount{}, s.Mounts...), o.Mounts...)
	}
	return s
}

// setEnvEntry replaces every assignment of entry's key in env with entry
// -- the first in place, the rest dropped -- or appends it.
func setEnvEntry(env []string, entry string) []string {
	key, _, _ := strings.Cut(entry, "=")
	out := env[:0]
	replaced := false
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == key {
			if !replaced {
				out = append(out, entry)
				replaced = true
			}
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, entry)
	}
	return out
}

// EncodeOverrideDisk is the override device's contents for o.
func EncodeOverrideDisk(o Override) ([]byte, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, len(OverrideMagic)+1+len(data)+sectorSize)
	buf = append(buf, OverrideMagic...)
	buf = append(buf, '\n')
	buf = append(buf, data...)
	if len(buf) > maxOverrideSize {
		return nil, fmt.Errorf("the container override is %d bytes, more than the %d a guest will read", len(buf), maxOverrideSize)
	}
	// A block device is a whole number of sectors; cloud-hypervisor
	// rounds a raw image's size down, which would cut the JSON short.
	if pad := len(buf) % sectorSize; pad != 0 {
		buf = append(buf, make([]byte, sectorSize-pad)...)
	}
	return buf, nil
}

// WriteOverrideDisk writes o's device image to path, replacing whatever
// an earlier start left there.
func WriteOverrideDisk(path string, o Override) error {
	data, err := EncodeOverrideDisk(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating the directory for the container override disk: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing the container override disk: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing the container override disk: %w", err)
	}
	return nil
}

// DecodeOverrideDisk reads an override device's contents from r. ok is
// false, with no error, when r does not start with OverrideMagic -- a
// device that is not the override.
func DecodeOverrideDisk(r io.Reader) (o Override, ok bool, err error) {
	data, err := io.ReadAll(io.LimitReader(r, maxOverrideSize))
	if err != nil {
		return Override{}, false, err
	}
	rest, found := bytes.CutPrefix(data, []byte(OverrideMagic+"\n"))
	if !found {
		return Override{}, false, nil
	}
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[:i]
	}
	o, err = ParseOverride(rest)
	if err != nil {
		return Override{}, true, err
	}
	return o, true, nil
}

// FindOverrideDevice looks through the guest's virtio block devices --
// sysBlock is /sys/block and dev is /dev, parameters so tests can supply
// their own -- for the override device, and returns what it carries.
// found is false when the VM was started with no override, which is the
// ordinary case.
//
// A device whose sysfs serial names something else is skipped without
// being opened; one with no readable serial is identified by its magic
// alone.
func FindOverrideDevice(sysBlock, dev string) (o Override, found bool, err error) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Override{}, false, nil
		}
		return Override{}, false, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vd") {
			names = append(names, e.Name())
		}
	}
	// The override is attached last, so the search starts from the end.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		if serial, err := os.ReadFile(filepath.Join(sysBlock, name, "serial")); err == nil {
			if strings.TrimRight(string(serial), "\x00\n ") != OverrideSerial {
				continue
			}
		}
		f, err := os.Open(filepath.Join(dev, name))
		if err != nil {
			continue
		}
		o, ok, err := DecodeOverrideDisk(f)
		f.Close()
		if ok {
			if err != nil {
				return Override{}, true, fmt.Errorf("the container override on /dev/%s: %w", name, err)
			}
			return o, true, nil
		}
	}
	return Override{}, false, nil
}

// LoadEffective is the spec the container runs: the one at effective,
// which granule-container writes once it has merged the VM's override,
// when it is there, and otherwise the baked spec at config with the
// override device's merged over it, found the same way granule-container
// finds it (sysBlock and dev as for FindOverrideDevice).
func LoadEffective(config, effective, sysBlock, dev string) (Spec, error) {
	if effective != "" {
		if _, err := os.Stat(effective); err == nil {
			return Load(effective)
		}
	}
	spec, err := Load(config)
	if err != nil {
		return spec, err
	}
	o, found, err := FindOverrideDevice(sysBlock, dev)
	if err != nil {
		return spec, err
	}
	if !found {
		return spec, nil
	}
	merged := spec.Apply(o)
	if err := merged.Validate(); err != nil {
		return spec, fmt.Errorf("the container spec at %s with this VM's override is unusable: %w", config, err)
	}
	return merged, nil
}
