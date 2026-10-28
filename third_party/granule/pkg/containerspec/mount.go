package containerspec

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Mount is a virtio-fs share the host attached to the VM (pkg/config's
// CHV_FS and CHV_VOLUMES, cloud-hypervisor's --fs), mounted inside the
// container at Destination -- the guest half of a container runtime's
// bind mount or volume. granule-container mounts every one inside the
// wrapped filesystem before it starts the container's process, so that
// the process, and anything granule-agent's container exec later starts
// in the same chroot, sees it there: both run in the guest's one mount
// namespace.
type Mount struct {
	// Tag is the virtio-fs tag the host attached the share with, which
	// is the mount source the guest's virtiofs driver knows it by.
	Tag string `json:"tag"`

	// Destination is an absolute path inside the container. It is
	// resolved the way the chroot will resolve it (MkdirIn), and created
	// if the image does not have it, so a symlink or ".." in the image
	// cannot put the mount anywhere on the guest outside the container.
	Destination string `json:"destination"`

	// ReadOnly mounts the share read-only inside the guest, whatever the
	// host's side of it allows.
	ReadOnly bool `json:"readonly,omitempty"`
}

// MountFSType is the filesystem type a Mount is mounted with.
const MountFSType = "virtiofs"

// validateMounts rejects a mount with no tag or a relative destination,
// and two mounts on one destination, which a container runtime refuses
// too: the second would silently hide the first.
func validateMounts(mounts []Mount) error {
	seen := map[string]bool{}
	for _, m := range mounts {
		if m.Tag == "" {
			return fmt.Errorf("the mount on %q names no virtio-fs tag", m.Destination)
		}
		if !strings.HasPrefix(m.Destination, "/") {
			return fmt.Errorf("the mount destination %q for tag %q is not an absolute path inside the container", m.Destination, m.Tag)
		}
		dest := path.Clean(m.Destination)
		if dest == "/" {
			return fmt.Errorf("the mount for tag %q is on the container's root, which would replace the whole wrapped filesystem", m.Tag)
		}
		if seen[dest] {
			return fmt.Errorf("two mounts are on %s", dest)
		}
		seen[dest] = true
	}
	return nil
}

// MountOrder is mounts in the order they have to be mounted: a parent
// before anything inside it, so that a share at /data/cache lands inside
// the share at /data rather than underneath it, where nothing could see
// it. Mounts at the same depth keep the order they were given in.
func MountOrder(mounts []Mount) []Mount {
	ordered := append([]Mount{}, mounts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return depth(ordered[i].Destination) < depth(ordered[j].Destination)
	})
	return ordered
}

func depth(p string) int {
	p = path.Clean("/" + p)
	if p == "/" {
		return 0
	}
	return strings.Count(p, "/")
}

// MountTarget is where on the guest m is mounted for a container whose
// filesystem is rootfs: its destination resolved inside rootfs, and
// created as a directory if it is missing (MkdirIn). The answer is a
// guest path no symlink stands between, so mounting on it cannot escape
// the wrapped filesystem. A destination that resolves to the container's
// root -- by "..", or by a symlink to "/" -- is refused rather than
// mounted over the whole of it.
func MountTarget(rootfs string, m Mount, perm fs.FileMode) (string, error) {
	dir, _, err := MkdirIn(rootfs, m.Destination, perm)
	if err != nil {
		return "", fmt.Errorf("the mount point %s for tag %q: %w", m.Destination, m.Tag, err)
	}
	if dir == "/" {
		return "", fmt.Errorf("the mount point %s for tag %q resolves to the container's root", m.Destination, m.Tag)
	}
	return filepath.Join(rootfs, dir), nil
}
