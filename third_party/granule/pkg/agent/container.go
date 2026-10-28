package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/execwire"
)

// containerConfig is the spec a container exec enters: the one `granule
// wrap` wrote and granule-container runs the workload from. A variable so
// tests can point it at a spec of their own.
var containerConfig = containerspec.ConfigPath

// containerEffective is where granule-container leaves the spec it
// actually ran -- containerConfig with the VM's own override merged in
// (see containerspec.Override) -- which a container exec prefers, so it
// enters the container that is running rather than the one the image was
// wrapped with. containerSysBlock and containerDev are where the
// override device is looked for when that file is not there yet, which
// is only in the moment between boot and granule-container's start.
// Variables, like containerConfig, so tests can point them elsewhere.
var (
	containerEffective = containerspec.EffectiveConfigPath
	containerSysBlock  = "/sys/block"
	containerDev       = "/dev"
)

// containerShell is what a container exec runs a Line through, and what
// an interactive one starts. A path inside the wrapped image: the
// guest's own shell is not on the far side of the chroot, and the
// image's login shell is not something a container runtime consults.
const containerShell = "/bin/sh"

// containerLaunch resolves a Container request against the wrapped
// container's own spec, the way a container runtime resolves an exec
// against the container's config:
//
//   - the root is the wrapped filesystem, entered with a chroot: the same
//     directory granule-container pivots the workload into, with the
//     mounts granule-container made there before it did (what the
//     workload mounts in its own mount namespace is not seen here);
//   - the user is the request's, else the container's USER, and either
//     is looked up in the wrapped image's /etc/passwd -- the guest's
//     accounts mean nothing inside it;
//   - the working directory is the request's, else the container's
//     WORKDIR, else "/", and is a path inside the wrapped filesystem;
//   - the environment is the container's (containerspec.Spec.Environ),
//     with the request's TERM and Env overlaid on it.
//
// Nothing here creates anything in the wrapped filesystem. The workload
// already did what a container start does (its WORKDIR, its volumes, the
// pseudo-filesystems); an exec that finds those missing reports it
// rather than repairing the container from the side.
func containerLaunch(config string, req execwire.Request) (launch, error) {
	if len(req.Argv) > 0 && req.Line != "" {
		return launch{}, errors.New("a request sets either argv or line, not both")
	}
	spec, err := containerspec.LoadEffective(config, containerEffective, containerSysBlock, containerDev)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return launch{}, fmt.Errorf("this guest runs no wrapped container (there is no %s), so there is nothing to exec into", config)
		}
		return launch{}, err
	}
	rootfs := spec.Rootfs
	if rootfs == "" {
		rootfs = containerspec.RootfsDir
	}
	if fi, err := os.Stat(rootfs); err != nil || !fi.IsDir() {
		return launch{}, fmt.Errorf("the wrapped container's filesystem is not at %s", rootfs)
	}

	user := req.User
	if user == "" {
		user = spec.User
	}
	cred, err := containerspec.ResolveUser(rootfs, user)
	if err != nil {
		return launch{}, err
	}
	who := user
	if who == "" {
		who = "root"
	}

	env := spec.Environ(cred)
	if req.TTY || req.Term != "" {
		// A container runtime gives a terminal session a TERM of its own
		// and leaves one without a terminal alone; the image's ENV is
		// still the base either way.
		env = setEnv(env, "TERM", "TERM="+terminalType(req))
	}
	for _, e := range req.Env {
		key, _, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			return launch{}, fmt.Errorf("environment entry %q is not in KEY=value form", e)
		}
		env = setEnv(env, key, e)
	}
	pathEnv := containerspec.DefaultPath
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			pathEnv = v
		}
	}

	var argv []string
	switch {
	case len(req.Argv) > 0:
		program, err := containerspec.LookPath(rootfs, req.Argv[0], pathEnv)
		if err != nil {
			return launch{}, err
		}
		argv = append([]string{program}, req.Argv[1:]...)
	default:
		if err := containerspec.ExecutableIn(rootfs, containerShell); err != nil {
			return launch{}, fmt.Errorf("the wrapped image has no %s to run a command line or an interactive shell with (%v); send the command as argv instead", containerShell, err)
		}
		argv = []string{containerShell}
		if strings.TrimSpace(req.Line) != "" {
			argv = append(argv, "-c", req.Line)
		}
	}
	if !path.IsAbs(argv[0]) {
		// A PATH entry of "" or "." resolves to a relative name, which
		// would be exec'd relative to the working directory rather than
		// found where LookPath found it.
		argv[0] = path.Join(dirOr(req.Dir, spec.WorkingDir), argv[0])
	}

	dir := dirOr(req.Dir, spec.WorkingDir)
	if !path.IsAbs(dir) {
		return launch{}, fmt.Errorf("working directory %q must be an absolute path", dir)
	}
	resolved, err := containerspec.ResolveIn(rootfs, dir)
	if err != nil {
		return launch{}, fmt.Errorf("working directory %s in the container: %w", dir, err)
	}
	if fi, err := os.Stat(filepath.Join(rootfs, resolved)); err != nil {
		return launch{}, fmt.Errorf("working directory %s in the container: %w", dir, err)
	} else if !fi.IsDir() {
		return launch{}, fmt.Errorf("working directory %s in the container is not a directory", dir)
	}

	var credential *syscall.Credential
	self := os.Getuid() != 0 && cred.UID == uint32(os.Getuid()) && cred.GID == uint32(os.Getgid())
	if cred.Set && !self {
		// An agent that is not root already is the only account it
		// could run as (which is only ever true in tests), the same
		// case account.self covers in the guest.
		//
		// No supplementary groups, deliberately: see ResolveUser. An
		// empty list rather than none, so the agent's own (root's) are
		// dropped rather than inherited.
		credential = &syscall.Credential{Uid: cred.UID, Gid: cred.GID, Groups: []uint32{}}
	}
	l := launch{Argv: argv, Dir: dir, Env: env, Credential: credential, Chroot: rootfs, Who: who}
	if rootfs == "/" {
		// A spec whose container is the whole guest, which
		// granule-container runs unchrooted too.
		l.Chroot = ""
	}
	return l, nil
}

// dirOr is the request's working directory when it names one, the
// container's WORKDIR when that does, and the root otherwise.
func dirOr(requested, workdir string) string {
	switch {
	case requested != "":
		return requested
	case workdir != "":
		return workdir
	}
	return "/"
}
