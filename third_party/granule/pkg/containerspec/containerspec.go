// Package containerspec is the contract between `granule wrap`, which
// turns an ordinary container image into a granule image, and
// granule-container, the guest-side program that runs that container once
// the VM has booted.
//
// Wrap reads an image's OCI configuration on the machine doing the
// wrapping -- entrypoint, command, environment, working directory, user
// -- and writes it into the guest as JSON at ConfigPath, beside the
// image's filesystem at RootfsDir. Nothing of the image itself runs at
// wrap time (see pkg/guestwrap), so what crosses from build time to
// boot time is exactly this file, and it is read once by
// cmd/granule-container. What the image's process is can still be
// changed per VM without rewrapping: see Override.
//
// The two halves ship together in one granule image and move together, so
// this is not a versioned wire format: it is a struct both ends of one
// build agree on. It is a package of its own rather than a type inside
// guestwrap so that the guest-side binary does not link the docker-driving
// half of wrap in order to read a config file.
package containerspec

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// Dir is where a wrapped container lives inside the guest: the image's
	// own filesystem below it, and nothing granule's guest rootfs already
	// uses. It is under /var/lib rather than /opt or /run because it is
	// state packed into the disk rather than something a boot creates.
	Dir = "/var/lib/granule/container"

	// RootfsDir holds the wrapped image's filesystem, exactly as the image
	// carries it. granule-container chroots into this; the guest's own
	// filesystem around it stays what runs the init system, the agent and
	// the network configuration.
	RootfsDir = Dir + "/rootfs"

	// ConfigPath is the spec below, as JSON. In /etc beside the guest's
	// other granule configuration (/etc/granule/ready-file) because it is
	// configuration: an operator editing it by hand and rebooting the VM
	// is a supported way to change what the container runs.
	ConfigPath = "/etc/granule/container.json"

	// RunnerPath is granule-container inside the guest, next to the other
	// two guest-side binaries granule installs.
	RunnerPath = "/usr/local/bin/granule-container"

	// DefaultPath is the PATH a container gets when its image names none,
	// matching what a container runtime would have given it.
	DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// Spec is what a wrapped image's container is: the OCI configuration
// that decides which process runs, plus where wrap put the filesystem it
// runs in.
type Spec struct {
	// Name is the service name of the wrapped workload.
	Name string `json:"name,omitempty"`

	// Image is the reference that was wrapped. Provenance only -- nothing
	// reads it back -- but it is the first thing anyone opening this file
	// in a booted guest wants to know.
	Image string `json:"image,omitempty"`

	// Rootfs is the wrapped filesystem's absolute path in the guest,
	// normally RootfsDir.
	Rootfs string `json:"rootfs"`

	// Entrypoint and Cmd are the image's, with the same meaning a
	// container runtime gives them: the process is Entrypoint followed by
	// Cmd, and an override of Entrypoint on the wrap command line drops
	// the image's Cmd unless one was supplied too.
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`

	// Env is the full environment the process gets, as KEY=VALUE, already
	// merged: the image's own, then anything wrap was asked to add. It is
	// the whole environment rather than additions to the guest's, because
	// a container's environment is the image's and not the machine's.
	Env []string `json:"env,omitempty"`

	// WorkingDir is a directory inside the wrapped filesystem. Empty
	// means "/", as it does for a container.
	WorkingDir string `json:"working_dir,omitempty"`

	// Volumes are volume mount points declared for the container.
	Volumes []string `json:"volumes,omitempty"`

	// Mounts are the virtio-fs shares mounted inside the container
	// before its process starts (see Mount). A wrapped image carries
	// none of its own; they arrive with the VM's override.
	Mounts []Mount `json:"mounts,omitempty"`

	// User is the image's USER, in any of the forms it may take: empty
	// (root), "1000", "1000:1000", "nginx" or "nginx:www-data". Names are
	// resolved against the wrapped filesystem's own /etc/passwd and
	// /etc/group at boot rather than here, since that is the only place
	// they mean anything.
	User string `json:"user,omitempty"`

	// StopSignal is the image's STOPSIGNAL ("SIGQUIT", "3"), sent to the
	// container when the VM is asked to shut down. Empty means SIGTERM.
	StopSignal string `json:"stopSignal,omitempty"`

	// HaltOnExit powers the VM off when the container's process exits,
	// which is what makes a wrapped image behave like the container it
	// wraps: the machine exists to run that process, so a `docker run` of
	// the wrapped image ends when it would have ended. Turned off, the
	// guest keeps running with no container in it, which is what someone
	// debugging a crash loop wants.
	HaltOnExit bool `json:"haltOnExit"`

	// OpenStdin gives the process a stdin the host can write to, the way
	// `docker run -i` (OCI's process with stdin kept open) does: what the
	// host sends over the workload's stream connection reaches it, and the
	// host closing its end is end-of-file (see pkg/workloadio). Without it
	// the process reads /dev/null, as a detached container does.
	OpenStdin bool `json:"openStdin,omitempty"`

	// Tty runs the process on a pseudo-terminal, OCI's process.terminal
	// and `docker run -t`: its stdin, stdout and stderr are the terminal,
	// so its output is one stream (stdout) and the host can resize it.
	Tty bool `json:"tty,omitempty"`
}

// Argv is the command the container runs: entrypoint then command, the
// way a container runtime composes them.
func (s Spec) Argv() []string {
	argv := make([]string, 0, len(s.Entrypoint)+len(s.Cmd))
	argv = append(argv, s.Entrypoint...)
	return append(argv, s.Cmd...)
}

// StopSignalName is the signal the container is stopped with, named --
// the image's own STOPSIGNAL, or the SIGTERM it gets when the image says
// nothing. For messages; ParseSignal is what sends it.
func (s Spec) StopSignalName() string {
	if s.StopSignal == "" {
		return "SIGTERM"
	}
	return s.StopSignal
}

// Validate rejects a spec that names no process or no filesystem. Both
// failures are worth catching at wrap time, where the message can say
// which flag fixes it, rather than on a VM's console at boot.
func (s Spec) Validate() error {
	if s.Rootfs == "" {
		return fmt.Errorf("the spec names no rootfs")
	}
	if !strings.HasPrefix(s.Rootfs, "/") {
		return fmt.Errorf("the rootfs %q is not an absolute path inside the guest", s.Rootfs)
	}
	if len(s.Argv()) == 0 {
		return fmt.Errorf("the image has neither an entrypoint nor a command, so there is nothing to run: give one on the wrap command line")
	}
	if s.WorkingDir != "" && !strings.HasPrefix(s.WorkingDir, "/") {
		return fmt.Errorf("the working directory %q is not an absolute path inside the container", s.WorkingDir)
	}
	for _, e := range s.Env {
		if !strings.Contains(e, "=") {
			return fmt.Errorf("the environment entry %q is not KEY=VALUE", e)
		}
	}
	if _, err := ParseSignal(s.StopSignal); err != nil {
		return err
	}
	return validateMounts(s.Mounts)
}

// Marshal is the spec as it is written into the guest: indented, because
// the one thing anybody does with this file is read it on a console.
func (s Spec) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Load reads a spec written by wrap.
func Load(path string) (Spec, error) {
	var s Spec
	data, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("reading the container spec: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parsing the container spec at %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return s, fmt.Errorf("the container spec at %s is unusable: %w", path, err)
	}
	return s, nil
}

// LookupEnv is the value of one variable in the spec's environment. The
// last assignment wins, as it does for a process environment.
func (s Spec) LookupEnv(key string) (string, bool) {
	value, found := "", false
	for _, e := range s.Env {
		name, v, ok := strings.Cut(e, "=")
		if ok && name == key {
			value, found = v, true
		}
	}
	return value, found
}

// Path is the PATH the container's own environment gives it, or the
// default a container runtime would have supplied.
func (s Spec) Path() string {
	if p, ok := s.LookupEnv("PATH"); ok && p != "" {
		return p
	}
	return DefaultPath
}

// signalNames are the signals a STOPSIGNAL is realistically set to. Kept
// as a list here rather than resolved with a syscall table because this
// package is read by the wrapping side too, which may not be Linux, and
// because an image naming something exotic is better refused at wrap
// time than translated into a guess.
var signalNames = map[string]int{
	"SIGHUP": 1, "SIGINT": 2, "SIGQUIT": 3, "SIGILL": 4, "SIGABRT": 6,
	"SIGKILL": 9, "SIGUSR1": 10, "SIGSEGV": 11, "SIGUSR2": 12,
	"SIGPIPE": 13, "SIGALRM": 14, "SIGTERM": 15, "SIGCONT": 18,
	"SIGSTOP": 19, "SIGTSTP": 20, "SIGWINCH": 28,
}

// ParseSignal turns a STOPSIGNAL into its number. Empty means SIGTERM,
// which is what a container gets when its image says nothing.
func ParseSignal(name string) (int, error) {
	if name == "" {
		return int(signalNames["SIGTERM"]), nil
	}
	upper := strings.ToUpper(strings.TrimSpace(name))
	if n, ok := signalNames[upper]; ok {
		return n, nil
	}
	if n, ok := signalNames["SIG"+upper]; ok {
		return n, nil
	}
	if n, err := strconv.Atoi(upper); err == nil && n > 0 && n < 64 {
		return n, nil
	}
	return 0, fmt.Errorf("unknown stop signal %q: expected a name like SIGTERM or a number", name)
}
