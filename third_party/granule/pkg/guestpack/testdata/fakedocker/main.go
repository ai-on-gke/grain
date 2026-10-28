// Command fakedocker stands in for the docker CLI in tests of the
// guestpack package: it records every invocation (one line per call, to
// the file named by FAKEDOCKER_LOG) and keeps a copy of the Dockerfile
// the call named with -f, so the build pack generates can be asserted on
// after the temporary build context has been cleaned up -- and without a
// docker daemon, an image to pull, or a rootfs to pack.
//
//   - FAKEDOCKER_SAVE_DOCKERFILE, if set, is where the file named by the
//     recorded call's "--file" argument is copied to. Pack deletes its
//     build context when it returns, so a test that wants to read the
//     generated Dockerfile has to take the copy while the build is
//     notionally running, which is what this does.
//   - FAKEDOCKER_SIZE sets what `image inspect -f {{.Size}}` prints
//     (default "1000000"), so the size report can be exercised.
//   - FAKEDOCKER_EXPOSED is the ports an `image inspect` of the
//     ExposedPorts config answers with, space-separated and one per line
//     as the real one's template prints them. Empty means an image that
//     declares none, which is the ordinary case.
//   - FAKEDOCKER_FAIL_CONTAINS fails any call whose argv (joined with
//     spaces) contains it, writing a line to stderr that mentions it --
//     which is how a build failure's own message reaches the error
//     guestpack wraps.
//   - FAKEDOCKER_FAIL_MESSAGE is what such a call prints instead of that
//     line: it is how a test hands over docker's own wording for a
//     `create` that could not pull the image, which is what a pack reads
//     to tell a reference that would not resolve from a granule image
//     that answered and carried the wrong things.
//   - FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS does the same for a string in
//     the Dockerfile rather than in the argv, and fails the way buildkit
//     fails a COPY whose source is not there: naming the path. That is
//     the failure an old granule image with no guest-parts directory
//     produces, and all guestpack has to recognize it by.
//   - FAKEDOCKER_FAIL_WITH fails a `build` call by printing what it is
//     set to as the build's log and nothing else. It is how a test hands
//     over a whole tail of real buildkit output rather than the one
//     canned line above: what matters about a real tail is that it holds
//     the steps that succeeded too, and an explanation matched across
//     all of it blames one of those.
//   - FAKEDOCKER_ABSENT is a comma-separated list of "<ref>=<path>"
//     entries that `docker cp` reports as missing, the way docker
//     reports a path an image does not carry. Everything else is a
//     regular file. This is how a test says "this granule image carries
//     no kernel", which is what a --target runtime build is.
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
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(f, strings.Join(args, "\x1f"))
		f.Close()
	}

	var dockerfile string
	if path := flagValue(args, "--file"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		dockerfile = string(data)
		if dest := os.Getenv("FAKEDOCKER_SAVE_DOCKERFILE"); dest != "" {
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
	}

	if want := os.Getenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS"); want != "" && strings.Contains(dockerfile, want) {
		fmt.Fprintf(os.Stderr, "ERROR: failed to solve: failed to compute cache key: %q: not found\n", want)
		os.Exit(1)
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

	if len(args) >= 2 && args[0] == "create" {
		// The reference goes into the id so that `cp` below can answer
		// about the right image. Nothing is started: `docker create` on
		// the real one does not start anything either, which is the
		// property the reads are built on.
		fmt.Println("c-" + base64.RawURLEncoding.EncodeToString([]byte(args[1])))
		return
	}
	if len(args) >= 3 && args[0] == "cp" {
		copyOut(args[1])
		return
	}

	if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
		// Queries share this verb, and which one it is is decided by
		// the format, exactly as it is for the real docker.
		format := flagValue(args, "-f")
		if strings.Contains(format, "ExposedPorts") {
			for _, port := range strings.Fields(os.Getenv("FAKEDOCKER_EXPOSED")) {
				fmt.Println(port)
			}
			return
		}
		if strings.Contains(format, "Volumes") {
			for _, vol := range strings.Fields(os.Getenv("FAKEDOCKER_VOLUMES")) {
				fmt.Println(vol)
			}
			return
		}
		if strings.Contains(format, "Labels") {
			if strings.Contains(format, "dev.granule.guest.volumes") {
				fmt.Println(os.Getenv("FAKEDOCKER_VOLUMES_LABEL"))
				return
			}
		}
		if strings.Contains(format, ".Config") {
			cfg := os.Getenv("FAKEDOCKER_CONFIG")
			if cfg == "" {
				cfg = "{}"
			}
			fmt.Println(cfg)
			return
		}
		size := os.Getenv("FAKEDOCKER_SIZE")
		if size == "" {
			size = "1000000"
		}
		fmt.Println(size)
		return
	}
}

// copyOut answers "docker cp <id>:<path> -": a tar carrying one header
// for the path, or docker's own way of saying the image has not got it.
func copyOut(source string) {
	id, path, ok := strings.Cut(source, ":")
	if !ok {
		fmt.Fprintf(os.Stderr, "fakedocker: %q is not <container>:<path>\n", source)
		os.Exit(2)
	}
	ref, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "c-"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if absent(string(ref), path) {
		// Byte for byte the shape docker reports a path an image does
		// not carry in, which is all the caller has to recognize it by.
		fmt.Fprintf(os.Stderr, "Error: No such container:path: %s:%s\n", id, path)
		os.Exit(1)
	}

	tw := tar.NewWriter(os.Stdout)
	body := "fakedocker: the contents of " + path + "\n"
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: filepath.Base(path), Mode: 0o644, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := tw.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
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

// flagValue returns the argument following name, or "".
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
