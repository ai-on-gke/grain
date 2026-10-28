package containerspec

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LookPath resolves the program a container is asked to run, inside the
// wrapped filesystem, and returns the path it will have *after* the
// chroot -- which is the only path an exec that follows can use.
//
// A name with a slash in it is already that path. Anything else is
// searched along the container's own PATH, because a container's
// commands are its image's and the guest's /usr/bin is not on the
// container's PATH at all.
//
// Both granule-container (the workload) and granule-agent (an exec into
// the workload's root) ask this, so the two cannot disagree about which
// binary a name means.
func LookPath(rootfs, name, pathEnv string) (string, error) {
	if strings.Contains(name, "/") {
		if err := ExecutableIn(rootfs, name); err != nil {
			return "", fmt.Errorf("the container's command %s: %w", name, err)
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if err := ExecutableIn(rootfs, candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("the container's command %q is not on its own PATH (%s) inside the wrapped image", name, pathEnv)
}

// ExecutableIn is os/exec's own question, asked about a path in the
// wrapped filesystem: is this a file something can be exec'd from.
func ExecutableIn(rootfs, path string) error {
	resolved, err := ResolveIn(rootfs, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(rootfs, resolved))
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fs.ErrPermission
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// Environ is the environment a process in the container gets: the
// image's own, plus a HOME for the user it runs as and a PATH when the
// image did not set them. A container runtime supplies the same, and a
// shell started in a wrapped image with no HOME writes its history to
// the root of the filesystem.
func (s Spec) Environ(cred Credential) []string {
	env := append([]string{}, s.Env...)
	if _, ok := s.LookupEnv("HOME"); !ok {
		home := cred.Home
		if home == "" {
			home = "/"
		}
		env = append(env, "HOME="+home)
	}
	if _, ok := s.LookupEnv("PATH"); !ok {
		env = append(env, "PATH="+s.Path())
	}
	return env
}
