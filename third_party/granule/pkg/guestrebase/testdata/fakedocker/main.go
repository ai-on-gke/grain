// Command fakedocker stands in for the docker CLI in tests of the
// guestrebase package: it records every invocation (one line per call,
// to the file named by FAKEDOCKER_LOG), keeps a copy of the Dockerfile
// the call named with --file, and answers the handful of questions a
// rebase asks an image -- so the whole sequence can be asserted without
// a docker daemon, a guest image to rebase, or a disk to copy.
//
// The interesting half is `create`/`cp`: a rebase looks at what an image
// carries by making a container from it and copying paths out, and this
// models that by encoding the image reference into the container id it
// hands back, so a later `cp` can answer per image.
//
//   - FAKEDOCKER_SAVE_DOCKERFILE, if set, is where the file named by
//     "--file" is copied to. A rebase deletes its build context when it
//     returns, so a test that wants the generated build has to take the
//     copy while the build is notionally running.
//   - FAKEDOCKER_ABSENT is a comma-separated list of "<ref>=<path>"
//     entries that `docker cp` reports as missing, the way docker
//     reports a path an image does not carry. Everything else is a
//     regular file. This is how a test says "this guest brings no kernel
//     of its own", which is the ordinary case.
//   - FAKEDOCKER_LABELS is a comma-separated list of "<name>=<value>"
//     answered by `image inspect -f {{index .Config.Labels "<name>"}}`.
//     A name that is not listed answers "<no value>", which is what
//     docker prints for a label an image has not got.
//   - FAKEDOCKER_PLACED, if set, is a directory the tar of a
//     `docker cp - <container>:<dir>` is unpacked into, so a test can
//     read what a build wrote beside the guest's disk before committing
//     it.
//   - FAKEDOCKER_EXPOSED is the ports an `image inspect` of the
//     ExposedPorts config answers with, space-separated. Empty means an
//     image that declares none, which is the ordinary case.
//   - FAKEDOCKER_PARTS names a directory `docker cp <id>:<path> <dir>`
//     copies from, standing in for the guest-parts directory a granule
//     image carries.
//   - FAKEDOCKER_FAIL_CONTAINS fails any call whose argv (joined with
//     spaces) contains it.
//   - FAKEDOCKER_FAIL_MESSAGE is what such a call prints to stderr
//     instead of a note from the fake: it is how a test hands over
//     docker's own wording for a `create` that could not pull the image,
//     which is what a rebase reads to tell a reference that would not
//     resolve from an image that would not answer.
//   - FAKEDOCKER_FAIL_WITH fails a `build` call by printing what it is
//     set to as the build's log and nothing else. It is how a test hands
//     over a whole tail of real buildkit output: what matters about a
//     real tail is that it holds the steps that succeeded too, and an
//     explanation matched across all of it blames one of those.
package main

import (
	"archive/tar"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	args := os.Args[1:]

	if logPath := os.Getenv("FAKEDOCKER_LOG"); logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			die(err)
		}
		fmt.Fprintln(f, strings.ReplaceAll(strings.Join(args, "\x1f"), "\n", "\x1e"))
		f.Close()
	}

	if path := flagValue(args, "--file"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			die(err)
		}
		if dest := os.Getenv("FAKEDOCKER_SAVE_DOCKERFILE"); dest != "" {
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				die(err)
			}
		}
	}

	if log := os.Getenv("FAKEDOCKER_FAIL_WITH"); log != "" && len(args) > 0 && args[0] == "build" {
		fmt.Fprintln(os.Stderr, log)
		os.Exit(1)
	}

	if want := os.Getenv("FAKEDOCKER_FAIL_CONTAINS"); want != "" && strings.Contains(strings.Join(args, " "), want) {
		if msg := os.Getenv("FAKEDOCKER_FAIL_MESSAGE"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		} else {
			fmt.Fprintf(os.Stderr, "fakedocker: failing on request: %s\n", want)
		}
		os.Exit(1)
	}

	switch {
	case len(args) >= 2 && args[0] == "create":
		// The reference goes into the id so that `cp` below can answer
		// about the right image.
		fmt.Println("c-" + base64.RawURLEncoding.EncodeToString([]byte(args[1])))
	case len(args) >= 3 && args[0] == "cp" && args[1] == "-":
		copyIn(args[2])
	case len(args) >= 3 && args[0] == "cp":
		copyOut(args[1], args[2])
	case len(args) >= 4 && args[0] == "image" && args[1] == "inspect" && args[2] == "-f":
		inspect(args[3])
	case len(args) >= 3 && args[0] == "inspect" && args[1] == "-f":
		// The two questions pkg/guestbuild asks while the boot form
		// is running: a container that is up, and one that powered off
		// cleanly.
		switch args[2] {
		case "{{.State.Running}}":
			fmt.Println("true")
		case "{{.State.ExitCode}}":
			fmt.Println("0")
		}
	}
}

// copyIn answers "docker cp - <container>:<dir>", the direction
// pkg/guestbuild writes the guest contract sidecar in before it
// commits. The tar on stdin is unpacked under FAKEDOCKER_PLACED, if a
// test asked for somewhere to look at it.
func copyIn(dest string) {
	if _, _, ok := strings.Cut(dest, ":"); !ok {
		die(fmt.Errorf("fakedocker: %q is not <container>:<path>", dest))
	}
	into := os.Getenv("FAKEDOCKER_PLACED")
	r := tar.NewReader(os.Stdin)
	for {
		h, err := r.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			die(err)
		}
		if into == "" {
			continue
		}
		body, err := io.ReadAll(r)
		if err != nil {
			die(err)
		}
		if err := os.WriteFile(filepath.Join(into, filepath.Base(h.Name)), body, 0o644); err != nil {
			die(err)
		}
	}
}

// copyOut answers "docker cp <id>:<path> <dest>", where dest is either
// "-" (a tar on stdout) or a directory on this machine.
func copyOut(source, dest string) {
	id, path, ok := strings.Cut(source, ":")
	if !ok {
		die(fmt.Errorf("fakedocker: %q is not <container>:<path>", source))
	}
	ref, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "c-"))
	if err != nil {
		die(err)
	}
	if absent(string(ref), path) {
		// Byte for byte the shape docker reports a path an image does
		// not carry in, which is all a rebase has to recognize it by.
		fmt.Fprintf(os.Stderr, "Error: No such container:path: %s:%s\n", id, path)
		os.Exit(1)
	}

	if dest != "-" {
		parts := os.Getenv("FAKEDOCKER_PARTS")
		if parts == "" {
			die(fmt.Errorf("fakedocker: no FAKEDOCKER_PARTS to copy %s from", path))
		}
		if err := copyTree(parts, dest); err != nil {
			die(err)
		}
		return
	}

	tw := tar.NewWriter(os.Stdout)
	body := "fakedocker: the contents of " + path + "\n"
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     filepath.Base(path),
		Mode:     0o644,
		Size:     int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		die(err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		die(err)
	}
	if err := tw.Close(); err != nil {
		die(err)
	}
}

// absent reports whether FAKEDOCKER_ABSENT lists this image's path.
func absent(ref, path string) bool {
	for _, entry := range strings.Split(os.Getenv("FAKEDOCKER_ABSENT"), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if ok && name == ref && value == path {
			return true
		}
	}
	return false
}

// inspect answers the -f templates a rebase uses: a label lookup, and
// what the image declares with EXPOSE.
func inspect(format string) {
	if strings.Contains(format, "ExposedPorts") {
		for _, port := range strings.Fields(os.Getenv("FAKEDOCKER_EXPOSED")) {
			fmt.Println(port)
		}
		return
	}
	name := format
	if i := strings.Index(format, `"`); i >= 0 {
		name = format[i+1:]
		if j := strings.Index(name, `"`); j >= 0 {
			name = name[:j]
		}
	}
	for _, entry := range strings.Split(os.Getenv("FAKEDOCKER_LABELS"), ",") {
		label, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if ok && label == name {
			fmt.Println(value)
			return
		}
	}
	fmt.Println("<no value>")
}

// copyTree copies a directory tree, which is the part of `docker cp`
// into a directory that a test cares about.
func copyTree(src, dest string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// flagValue returns the argument following name, or "".
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
