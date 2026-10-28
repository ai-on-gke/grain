package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHost is a doctorHost whose /dev, /sys and /proc are files under a
// temporary directory and whose docker, kernel probes and device opens
// are stand-ins, so no check here ever touches the machine running it.
// It starts out as a host every check passes on; tests take things away.
type fakeHost struct {
	*doctorHost
	t       *testing.T
	openErr error
	// dockerOut maps the first docker argument ("version", "info") to
	// what it prints; dockerErr makes every docker call fail.
	dockerOut map[string]string
	dockerErr error
}

func newFakeHost(t *testing.T) *fakeHost {
	f := &fakeHost{t: t, dockerOut: map[string]string{
		"version": "24.0.7\n",
		"info":    "name=apparmor\nname=seccomp,profile=builtin\nname=cgroupns\n",
	}}
	f.doctorHost = &doctorHost{
		root:   t.TempDir(),
		goarch: "amd64",
		euid:   1000,
		open:   func(string) error { return f.openErr },
		docker: func(_ context.Context, args ...string) (string, error) {
			if f.dockerErr != nil {
				return "", f.dockerErr
			}
			return f.dockerOut[args[0]], nil
		},
		netProbe:     func() error { t.Fatal("netshim probe run without root"); return nil },
		ioUringProbe: func() error { return nil },
	}
	f.write("/dev/kvm", "")
	f.write("/dev/net/tun", "")
	f.write("/sys/module/kvm/parameters/halt_poll_ns", "200000")
	f.write("/sys/module/kvm_intel/parameters/nested", "Y")
	f.write("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu vme vmx sse\n")
	return f
}

func (f *fakeHost) write(p, content string) {
	f.t.Helper()
	full := filepath.Join(f.root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) remove(p string) {
	f.t.Helper()
	if err := os.RemoveAll(filepath.Join(f.root, p)); err != nil {
		f.t.Fatal(err)
	}
}

// check runs every check and returns the one named.
func (f *fakeHost) check(name string) checkResult {
	f.t.Helper()
	for _, r := range runDoctorChecks(context.Background(), f.doctorHost) {
		if r.name == name {
			return r
		}
	}
	f.t.Fatalf("no check named %q", name)
	return checkResult{}
}

func wantVerdict(t *testing.T, r checkResult, want verdict, detailHas ...string) {
	t.Helper()
	if r.verdict != want {
		t.Errorf("%s = %s (%s), want %s", r.name, r.verdict, r.detail, want)
	}
	for _, s := range detailHas {
		if !strings.Contains(r.detail+" "+r.fix, s) {
			t.Errorf("%s: %q / %q does not mention %q", r.name, r.detail, r.fix, s)
		}
	}
	if r.verdict != verdictPass && r.verdict != verdictSkip && r.fix == "" {
		t.Errorf("%s is %s with nothing said about what to do", r.name, r.verdict)
	}
}

func TestDoctor_HealthyHostPassesEverything(t *testing.T) {
	f := newFakeHost(t)
	for _, r := range runDoctorChecks(context.Background(), f.doctorHost) {
		want := verdictPass
		if r.name == "netshim" {
			want = verdictSkip
		}
		wantVerdict(t, r, want)
	}
	var out bytes.Buffer
	if err := doctor(context.Background(), f.doctorHost, nil, &out, &out); err != nil {
		t.Fatalf("doctor on a healthy host: %v\n%s", err, out.String())
	}
}

func TestDoctor_Arch(t *testing.T) {
	f := newFakeHost(t)
	f.goarch = "arm64"
	wantVerdict(t, f.check("arch"), verdictPass)
	f.goarch = "riscv64"
	wantVerdict(t, f.check("arch"), verdictWarn, "riscv64")
}

func TestDoctor_KVMMissing(t *testing.T) {
	f := newFakeHost(t)
	f.remove("/dev/kvm")
	wantVerdict(t, f.check("kvm"), verdictFail, "does not exist", "udev")

	f.remove("/sys/module")
	wantVerdict(t, f.check("kvm"), verdictFail, "modprobe kvm_intel")

	f.write("/proc/cpuinfo", "flags\t\t: fpu vme sse\n")
	wantVerdict(t, f.check("kvm"), verdictFail, "nested virtualization")
}

func TestDoctor_KVMNotOpenable(t *testing.T) {
	f := newFakeHost(t)
	f.openErr = &fs.PathError{Op: "open", Path: "/dev/kvm", Err: fs.ErrPermission}
	// A rootful daemon opens the device itself.
	wantVerdict(t, f.check("kvm"), verdictWarn, "permission denied", "docker runs as root", "group owning /dev/kvm")

	f.dockerOut["info"] = "name=seccomp,profile=builtin\nname=rootless\n"
	wantVerdict(t, f.check("kvm"), verdictFail, "rootless")

	// No daemon to vouch for it: this user's permissions are all there is.
	f.dockerErr = errors.New("Cannot connect to the Docker daemon")
	wantVerdict(t, f.check("kvm"), verdictFail, "permission denied")
}

func TestDoctor_KVMModule(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("kvm-module"), verdictPass, "kvm_intel")

	f.remove("/sys/module/kvm_intel")
	f.write("/sys/module/kvm_amd/parameters/nested", "1")
	wantVerdict(t, f.check("kvm-module"), verdictPass, "kvm_amd")

	f.remove("/sys/module/kvm_amd")
	wantVerdict(t, f.check("kvm-module"), verdictWarn, "neither kvm_intel nor kvm_amd")
	// arm64 has no vendor module.
	f.goarch = "arm64"
	wantVerdict(t, f.check("kvm-module"), verdictPass)

	f.remove("/sys/module/kvm")
	wantVerdict(t, f.check("kvm-module"), verdictFail, "CONFIG_KVM")
}

func TestDoctor_Nested(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("nested"), verdictPass, "enabled")

	f.write("/sys/module/kvm_intel/parameters/nested", "N")
	wantVerdict(t, f.check("nested"), verdictWarn, "disabled", "nested=1")

	f.remove("/sys/module/kvm_intel")
	f.write("/sys/module/kvm_amd/parameters/nested", "1")
	wantVerdict(t, f.check("nested"), verdictPass, "kvm_amd")

	f.remove("/sys/module/kvm_amd")
	wantVerdict(t, f.check("nested"), verdictSkip)
}

func TestDoctor_Tun(t *testing.T) {
	f := newFakeHost(t)
	f.remove("/dev/net/tun")
	wantVerdict(t, f.check("tun"), verdictFail, "modprobe tun")
}

func TestDoctor_VsockNeverFails(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("vsock"), verdictPass, "not needed")
	f.write("/dev/vhost-vsock", "")
	wantVerdict(t, f.check("vsock"), verdictPass, "present")
}

func TestDoctor_Docker(t *testing.T) {
	f := newFakeHost(t)
	t.Setenv("GRANULE_DOCKER", "podman")
	wantVerdict(t, f.check("docker"), verdictPass, "podman", "24.0.7")

	f.dockerOut["info"] = "name=rootless\n"
	wantVerdict(t, f.check("docker"), verdictWarn, "rootless", "--device /dev/kvm")

	f.dockerErr = errors.New("docker version: exit status 1: Cannot connect to the Docker daemon\nmore")
	r := f.check("docker")
	wantVerdict(t, r, verdictFail, "Cannot connect", "GRANULE_DOCKER")
	if strings.Contains(r.detail, "more") {
		t.Errorf("docker detail %q is more than the error's first line", r.detail)
	}
}

func TestDoctor_Netshim(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("netshim"), verdictSkip, "needs root")

	f.euid = 0
	probed := false
	f.netProbe = func() error { probed = true; return nil }
	wantVerdict(t, f.check("netshim"), verdictPass)
	if !probed {
		t.Error("netshim check passed as root without probing")
	}

	f.netProbe = func() error { return errors.New("adding an ingress qdisc: operation not supported") }
	wantVerdict(t, f.check("netshim"), verdictFail, "ingress qdisc", "CONFIG_NET_SCH_INGRESS", "act_mirred")
}

func TestDoctor_LSM(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("lsm"), verdictPass, "no SELinux or AppArmor")

	f.write("/sys/module/apparmor/parameters/enabled", "Y")
	wantVerdict(t, f.check("lsm"), verdictPass, "AppArmor")

	f.write("/sys/fs/selinux/enforce", "0")
	wantVerdict(t, f.check("lsm"), verdictPass, "SELinux permissive")

	f.write("/sys/fs/selinux/enforce", "1")
	wantVerdict(t, f.check("lsm"), verdictWarn, "SELinux enforcing", "--device /dev/kvm", "no-new-privileges", "SELinux support is off")
}

func TestDoctor_IOUring(t *testing.T) {
	f := newFakeHost(t)
	wantVerdict(t, f.check("io_uring"), verdictPass, "available")

	f.dockerOut["version"] = "25.0.3"
	wantVerdict(t, f.check("io_uring"), verdictWarn, "seccomp", "epoll")

	f.dockerOut["info"] = "name=seccomp,profile=unconfined\n"
	wantVerdict(t, f.check("io_uring"), verdictPass)

	f.ioUringProbe = func() error { return errors.New("function not implemented") }
	wantVerdict(t, f.check("io_uring"), verdictWarn, "not available", "epoll")

	f.write("/proc/sys/kernel/io_uring_disabled", "2")
	wantVerdict(t, f.check("io_uring"), verdictWarn, "io_uring_disabled=2")
}

func TestDoctor_ExitsNonZeroOnlyOnAFailure(t *testing.T) {
	f := newFakeHost(t)
	f.goarch = "riscv64" // a warning
	var out bytes.Buffer
	if err := doctor(context.Background(), f.doctorHost, nil, &out, &out); err != nil {
		t.Fatalf("doctor with only a warning: %v", err)
	}
	if !strings.Contains(out.String(), "WARN  arch") || !strings.Contains(out.String(), "-> ") {
		t.Errorf("report does not show the warning and its fix:\n%s", out.String())
	}

	f.remove("/dev/net/tun")
	out.Reset()
	err := doctor(context.Background(), f.doctorHost, nil, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "tun") {
		t.Fatalf("doctor without /dev/net/tun: err = %v, want a failure naming tun", err)
	}
}

func TestDoctor_OnlyJudgesTheNamedChecks(t *testing.T) {
	f := newFakeHost(t)
	f.dockerErr = errors.New("no daemon")
	var out bytes.Buffer
	if err := doctor(context.Background(), f.doctorHost, []string{"-only", "kvm,tun"}, &out, &out); err != nil {
		t.Fatalf("doctor -only kvm,tun with docker down: %v", err)
	}
	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Errorf("doctor -only kvm,tun printed %d lines, want 2:\n%s", got, out.String())
	}
	if err := doctor(context.Background(), f.doctorHost, []string{"-only", "docker"}, &out, &out); err == nil {
		t.Error("doctor -only docker with docker down succeeded")
	}
	if err := doctor(context.Background(), f.doctorHost, []string{"-only", "kvmm"}, &out, &out); err == nil || !strings.Contains(err.Error(), "kvmm") {
		t.Errorf("doctor -only kvmm: err = %v, want it to name the unknown check", err)
	}
}

func TestRun_DoctorHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"doctor", "-h"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("granule doctor -h exit code = %d, want 0\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-only") {
		t.Errorf("granule doctor -h does not show -only:\n%s", stderr.String())
	}
}
