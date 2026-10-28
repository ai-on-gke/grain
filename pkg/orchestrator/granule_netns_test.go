package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pidInContainer is what stands between a pid docker reports and the
// ports firewalled for it: a pid this process reads as somebody else's --
// its own pid namespace, or a reused number -- would firewall the wrong
// namespace's ports and leave the VM's open.
func TestPidInContainerReadsDockersCgroupPaths(t *testing.T) {
	const id = "4f1c0de5a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7"
	for _, tc := range []struct {
		name, cgroup string
		want         bool
	}{
		{"cgroup v2, systemd driver", "0::/system.slice/docker-" + id + ".scope\n", true},
		{"cgroup v2, cgroupfs driver", "0::/docker/" + id + "\n", true},
		{"cgroup v1", "12:pids:/docker/" + id + "\n1:name=systemd:/docker/" + id + "\n", true},
		{"its own cgroup namespace", "0::/\n", false},
		{"another container", "0::/system.slice/docker-" + strings.Repeat("e", 64) + ".scope\n", false},
		{"a prefix of the id is not the id", "0::/docker/" + id[:12] + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			prev := procRoot
			procRoot = root
			t.Cleanup(func() { procRoot = prev })
			if err := os.MkdirAll(filepath.Join(root, "42"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "42", "cgroup"), []byte(tc.cgroup), 0o644); err != nil {
				t.Fatal(err)
			}
			err := pidInContainer(42, id)
			if got := err == nil; got != tc.want {
				t.Errorf("pidInContainer = %v, want in-container %v", err, tc.want)
			}
		})
	}
	prev := procRoot
	procRoot = t.TempDir()
	defer func() { procRoot = prev }()
	if err := pidInContainer(42, id); err == nil {
		t.Error("a pid with no /proc entry was taken to be in the container")
	}
}

// The kernel-side lookup against a real container: the bridge port it
// finds is the host end of the container's own veth, which is what
// guestLinksScript finds when docker exec reaches the container (an
// ordinary image) rather than a guest (a granule VM, where only this
// lookup can answer). Skips without a docker it can start a container on.
func TestContainerNetnsPortsFindsTheContainersOwnVeth(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("network namespaces are Linux only")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image := ""
	for _, candidate := range []string{"busybox:latest", "debian:bookworm-slim", "alpine:latest"} {
		if exec.CommandContext(ctx, "docker", "image", "inspect", candidate).Run() == nil {
			image = candidate
			break
		}
	}
	if image == "" {
		t.Skip("no small image with sh and cat present locally (busybox, debian:bookworm-slim, alpine)")
	}
	name := "grain-netns-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--name", name, image, "sleep", "300").CombinedOutput(); err != nil {
		t.Skipf("cannot start a container: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	ports, err := containerNetnsPorts(ctx, name)
	if err != nil {
		t.Fatalf("containerNetnsPorts: %v", err)
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", guestLinksScript).Output()
	if err != nil {
		t.Fatalf("docker exec: %v", err)
	}
	links, err := parseGuestLinks(string(out))
	if err != nil {
		t.Fatal(err)
	}
	want, err := hostPortsFor(hostSysfsNet, links)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || strings.Join(ports, ",") != strings.Join(want, ",") {
		t.Errorf("containerNetnsPorts = %q, the container's own view finds %q", ports, want)
	}
}

// writeFakeGranuleVMDocker puts a docker on PATH shaped like a granule VM
// container: inspect reports one network and the container's id and
// pid, and exec does not run in the container's namespaces at all --
// granule-shim forwards it into the guest, so guestLinksScript lists the
// guest's virtio NICs and loopback, none of them a veth. Every argv is
// appended to the returned log.
func writeFakeGranuleVMDocker(t *testing.T, id string, pid int) string {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "docker-argv.log")
	script := `#!/bin/sh
echo "$*" >> '` + argvLog + `'
case "$1 $3" in
"inspect {{len .NetworkSettings.Networks}}") echo 1 ;;
"inspect {{.Id}} {{.State.Pid}}") echo '` + id + ` ` + strconv.Itoa(pid) + `' ;;
exec*) printf 'eth0 2 2\neth1 3 3\nlo 1 1\n' ;;
*) echo "unexpected docker $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog
}

// inspectGuestPorts against a granule VM container, whose docker exec
// lands in the guest: the bridge port has to come from the kernel's view
// of the container's namespace, and when that cannot be had the guest's
// answer -- no veth -- has to refuse the VM rather than firewall nothing.
func TestInspectGuestPortsWhenDockerExecLandsInTheGuest(t *testing.T) {
	const id = "4f1c0de5a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7"
	const pid = 4242
	ctx := context.Background()

	// The host has the VM container's veth on a bridge, at an ifindex
	// nothing in the guest names.
	sysfs := t.TempDir()
	for name, attrs := range map[string]map[string]string{
		"docker0":     {"ifindex": "4", "iflink": "4"},
		"veth1d9c4eb": {"ifindex": "81", "iflink": "80", "brport/": ""},
	} {
		for file, value := range attrs {
			path := filepath.Join(sysfs, name, file)
			if strings.HasSuffix(file, "/") {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	prevSysfs, prevProc, prevNetns := hostSysfsNet, procRoot, hostNetnsPorts
	t.Cleanup(func() { hostSysfsNet, procRoot, hostNetnsPorts = prevSysfs, prevProc, prevNetns })
	hostSysfsNet = sysfs
	writeCgroup := func(cgroup string) {
		procRoot = t.TempDir()
		if err := os.MkdirAll(filepath.Join(procRoot, strconv.Itoa(pid)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"), []byte(cgroup), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the kernel names the port", func(t *testing.T) {
		argvLog := writeFakeGranuleVMDocker(t, id, pid)
		writeCgroup("0::/system.slice/docker-" + id + ".scope\n")
		hostNetnsPorts = func(p int) ([]string, error) {
			if p != pid {
				return nil, fmt.Errorf("asked about pid %d, want %d", p, pid)
			}
			return []string{"veth1d9c4eb"}, nil
		}
		ports, err := inspectGuestPorts(ctx, "granule-vm-g-none-1")
		if err != nil {
			t.Fatalf("inspectGuestPorts: %v", err)
		}
		if strings.Join(ports, ",") != "veth1d9c4eb" {
			t.Errorf("inspectGuestPorts = %q, want the container's veth", ports)
		}
		if argv, _ := os.ReadFile(argvLog); strings.Contains(string(argv), "exec") {
			t.Errorf("the guest was asked for the container's interfaces, though the kernel had answered:\n%s", argv)
		}
	})

	for _, tc := range []struct {
		name, cgroup string
		netns        func(int) ([]string, error)
	}{
		{"the daemon cannot see the container's pid", "0::/\n",
			func(int) ([]string, error) {
				t.Error("asked the kernel about a pid not in the container")
				return nil, nil
			}},
		{"the kernel cannot name the namespace", "0::/docker/" + id + "\n",
			func(int) ([]string, error) {
				return nil, errors.New("pid 4242's network namespace has no id in this one")
			}},
		{"the namespace has no bridge port", "0::/docker/" + id + "\n",
			func(int) ([]string, error) { return nil, nil }},
	} {
		t.Run(tc.name+": refused, not left open", func(t *testing.T) {
			writeFakeGranuleVMDocker(t, id, pid)
			writeCgroup(tc.cgroup)
			hostNetnsPorts = tc.netns
			ports, err := inspectGuestPorts(ctx, "granule-vm-g-none-1")
			if err == nil {
				t.Fatalf("inspectGuestPorts = %q from the guest's virtio NICs, want an error", ports)
			}
			if !strings.Contains(err.Error(), "only 0 host bridge port(s)") || !strings.Contains(err.Error(), "from the host:") {
				t.Errorf("error = %v, want it to say no port was found and why the host could not answer", err)
			}
		})
	}
}
