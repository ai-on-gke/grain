//go:build linux

package orchestrator

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// netnsBridgePorts is every host interface enslaved to a bridge whose
// link peer lives in the network namespace of process pid: the host end of
// each veth that namespace was given. It asks the kernel rather than
// anything inside the namespace, which is the point of it -- a granule VM
// container's `docker exec` lands in the guest (its sh is granule-shim's
// exec), so a script run that way lists the guest's virtio interfaces and
// never the container's veths, and what it prints is the guest's to
// choose besides.
//
// The namespace is named by its nsid as seen from this process's own
// network namespace: RTM_GETNSID with NETNSA_PID, which needs no
// privilege, against the IFLA_LINK_NETNSID a link dump reports for each
// veth whose peer is elsewhere. The dump comes first because the kernel
// assigns a peer namespace its id when it first reports one.
//
// A pid this process cannot see (a daemon in a container of its own pid
// namespace) is an error, not an empty answer, and so is a namespace the
// kernel has no id for.
func netnsBridgePorts(pid int) ([]string, error) {
	links, err := netlinkLinks()
	if err != nil {
		return nil, err
	}
	nsid, err := netnsIDOfPID(pid)
	if err != nil {
		return nil, err
	}
	var ports []string
	for _, l := range links {
		if !l.hasNSID || l.nsid != nsid || l.master == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(hostSysfsNet, l.name, "brport")); err != nil {
			continue
		}
		if !portName.MatchString(l.name) {
			return nil, fmt.Errorf("host bridge port %q is not a name this firewall can write a rule for", l.name)
		}
		ports = append(ports, l.name)
	}
	return ports, nil
}

// netlinkLink is what netnsBridgePorts needs of one host interface.
type netlinkLink struct {
	name    string
	master  uint32
	nsid    int32
	hasNSID bool
}

func netlinkLinks() ([]netlinkLink, error) {
	tab, err := syscall.NetlinkRIB(unix.RTM_GETLINK, unix.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("listing the host's links: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(tab)
	if err != nil {
		return nil, fmt.Errorf("listing the host's links: %w", err)
	}
	var links []netlinkLink
	for i := range msgs {
		if msgs[i].Header.Type != unix.RTM_NEWLINK {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&msgs[i])
		if err != nil {
			return nil, fmt.Errorf("listing the host's links: %w", err)
		}
		var l netlinkLink
		for _, a := range attrs {
			switch a.Attr.Type {
			case unix.IFLA_IFNAME:
				l.name = strings.TrimRight(string(a.Value), "\x00")
			case unix.IFLA_MASTER:
				if len(a.Value) >= 4 {
					l.master = binary.NativeEndian.Uint32(a.Value)
				}
			case unix.IFLA_LINK_NETNSID:
				if len(a.Value) >= 4 {
					l.nsid, l.hasNSID = int32(binary.NativeEndian.Uint32(a.Value)), true
				}
			}
		}
		links = append(links, l)
	}
	return links, nil
}

// netnsIDOfPID is the nsid this process's network namespace knows pid's
// network namespace by.
func netnsIDOfPID(pid int) (int32, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return 0, fmt.Errorf("opening a netlink socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("binding a netlink socket: %w", err)
	}
	// nlmsghdr, then struct rtgenmsg padded to four bytes, then one
	// NETNSA_PID attribute.
	const reqLen = unix.SizeofNlMsghdr + 4 + 8
	req := make([]byte, reqLen)
	ne := binary.NativeEndian
	ne.PutUint32(req[0:], reqLen)
	ne.PutUint16(req[4:], unix.RTM_GETNSID)
	ne.PutUint16(req[6:], unix.NLM_F_REQUEST)
	ne.PutUint32(req[8:], 1)
	req[unix.SizeofNlMsghdr] = unix.AF_UNSPEC
	ne.PutUint16(req[unix.SizeofNlMsghdr+4:], 8)
	ne.PutUint16(req[unix.SizeofNlMsghdr+6:], unix.NETNSA_PID)
	ne.PutUint32(req[unix.SizeofNlMsghdr+8:], uint32(pid))
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("asking for the network namespace id of pid %d: %w", pid, err)
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return 0, fmt.Errorf("asking for the network namespace id of pid %d: %w", pid, err)
	}
	msgs, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return 0, fmt.Errorf("asking for the network namespace id of pid %d: %w", pid, err)
	}
	for _, m := range msgs {
		switch m.Header.Type {
		case unix.NLMSG_ERROR:
			if len(m.Data) >= 4 {
				if errno := int32(ne.Uint32(m.Data)); errno != 0 {
					return 0, fmt.Errorf("asking for the network namespace id of pid %d: %w", pid, syscall.Errno(-errno))
				}
			}
		case unix.RTM_NEWNSID:
			for data := m.Data[min(4, len(m.Data)):]; len(data) >= 4; {
				alen := int(ne.Uint16(data))
				if alen < 4 || alen > len(data) {
					break
				}
				if ne.Uint16(data[2:]) == unix.NETNSA_NSID && alen >= 8 {
					nsid := int32(ne.Uint32(data[4:]))
					if nsid < 0 {
						return 0, fmt.Errorf("pid %d's network namespace has no id in this one", pid)
					}
					return nsid, nil
				}
				data = data[min((alen+3)&^3, len(data)):]
			}
		}
	}
	return 0, fmt.Errorf("no network namespace id in the kernel's answer for pid %d", pid)
}
