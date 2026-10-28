package hypervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// KVMDevice is the device cloud-hypervisor opens on a KVM host, and
// mshvDevice the one it falls back to on a Microsoft Hypervisor (MSHV)
// host instead.
const (
	KVMDevice  = "/dev/kvm"
	mshvDevice = "/dev/mshv"
)

// CheckDevice reports, before anything boots, whether this process can
// open the hypervisor device cloud-hypervisor is about to need -- and if
// not, why, in terms an operator can act on. cloud-hypervisor's own
// report of the same failure is "CreateHypervisor(VmCreate(Permission
// denied (os error 13)))", which names neither the device nor the
// account that was refused it.
//
// It opens the device rather than reading its owner and mode: what
// decides access is the kernel's check against this process's
// credentials, supplementary groups included, and in a user namespace
// the owner and group a stat reports are often the overflow IDs (65534)
// of a host account the namespace does not map, which say nothing about
// whether the open will succeed. A rootless container reaching /dev/kvm
// through the host's kvm group, kept as a supplementary group it cannot
// see the name of, is the case this has to get right -- see
// docs/rootless.md.
func CheckDevice() error {
	return checkDevice(KVMDevice, mshvDevice)
}

func checkDevice(kvm, mshv string) error {
	f, err := os.OpenFile(kvm, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err == nil {
		return f.Close()
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, merr := os.Stat(mshv); merr == nil {
			return nil
		}
		return fmt.Errorf("%s does not exist in this container, and cloud-hypervisor needs it: pass the device in (docker run --device %s, a Kubernetes device plugin, or a bind mount of the host's %s under crun)", kvm, kvm, kvm)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w; %s", err, deviceAccessHint(kvm))
	}
	return err
}

// deviceAccessHint says who was refused path and what usually fixes it.
func deviceAccessHint(path string) string {
	var who strings.Builder
	fmt.Fprintf(&who, "this process runs as uid %d gid %d", os.Geteuid(), os.Getegid())
	if groups, err := os.Getgroups(); err == nil {
		fmt.Fprintf(&who, " with supplementary groups [%s]", joinInts(groups))
	}
	if info, err := os.Stat(path); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintf(&who, ", and the device is uid %d gid %d mode %#o as this container sees it", st.Uid, st.Gid, info.Mode().Perm())
		}
	}
	return who.String() + ". KVM is reached through the device's group (the host's kvm group): the process needs that group " +
		"among its supplementary groups, not ownership of the device and not mode 0666. In a user namespace the host's kvm group " +
		"shows up as 65534 when the namespace does not map it, and survives into the container only if the runtime keeps the " +
		"caller's groups -- crun's run.oci.keep_original_groups=1 annotation, podman's --group-add keep-groups -- or maps it and " +
		"adds it (docker --group-add <kvm gid>). See docs/rootless.md"
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, " ")
}
