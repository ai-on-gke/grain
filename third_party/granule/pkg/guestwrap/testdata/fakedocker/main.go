// Command fakedocker stands in for the docker CLI in tests of the
// guestwrap package: it answers the two questions wrap asks an image
// (`image inspect` for its configuration and for its size), records every
// invocation, and keeps a copy of the build context wrap generated, so
// the build can be asserted on after the temporary directory is gone --
// and without a docker daemon, a registry or an image to wrap.
//
//   - FAKEDOCKER_LOG is where one line per call is written, arguments
//     separated by \x1f.
//   - FAKEDOCKER_SAVE_CONTEXT, if set, is a directory the files of the
//     build context named by "--file" are copied into. Wrap deletes that
//     context when it returns, so a test that wants to read the generated
//     Dockerfile and container spec has to take the copy while the build
//     is notionally running, which is what this does.
//   - FAKEDOCKER_CONFIG is the JSON `image inspect -f {{json .Config}}`
//     prints: the wrapped image's own OCI configuration.
//   - FAKEDOCKER_SIZE sets what `image inspect -f {{.Size}}` prints.
//   - FAKEDOCKER_MISSING is an image reference that is not on this
//     machine yet: inspecting it fails until a `docker pull` of it has
//     been recorded, which is how the pull path is exercised.
//   - FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS fails the build when the
//     generated Dockerfile contains it, the way buildkit fails a COPY
//     whose source is not there: naming the path.
//   - FAKEDOCKER_FAIL_WITH fails a `build` call by printing what it is
//     set to as the build's log and nothing else. It is how a test hands
//     over a whole tail of real buildkit output rather than the one
//     canned line above: what matters about a real tail is that it holds
//     the steps that succeeded too, and an explanation matched across
//     all of it blames one of those.
//   - FAKEDOCKER_FAIL_CONTAINS fails any call whose argv (joined with
//     spaces) contains it, and FAKEDOCKER_FAIL_MESSAGE is what such a
//     call prints to stderr. Together they hand over docker's own
//     wording for a command that never got the image at all -- a
//     `create` whose pull was denied -- which wrap reads before it
//     generates a build and which no build log can stand in for.
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

	// Before the calls below are answered rather than after: the point of
	// this one is to fail a `create` or a `pull`, which the handlers
	// further down would otherwise have answered successfully.
	if want := os.Getenv("FAKEDOCKER_FAIL_CONTAINS"); want != "" && strings.Contains(strings.Join(args, " "), want) {
		if msg := os.Getenv("FAKEDOCKER_FAIL_MESSAGE"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		} else {
			fmt.Fprintf(os.Stderr, "fakedocker: failing on request: %s\n", want)
		}
		os.Exit(1)
	}

	missing := os.Getenv("FAKEDOCKER_MISSING")
	pulled := filepath.Join(os.TempDir(), "fakedocker-pulled-"+strings.NewReplacer("/", "_", ":", "_").Replace(missing))

	if len(args) >= 2 && args[0] == "pull" {
		if args[1] == missing {
			os.WriteFile(pulled, nil, 0o644)
		}
		return
	}

	if len(args) >= 2 && args[0] == "create" {
		// The reference goes into the id so that `cp` below can answer
		// about the right image. Nothing is started, exactly as the real
		// `docker create` starts nothing.
		fmt.Println("c-" + base64.RawURLEncoding.EncodeToString([]byte(args[1])))
		return
	}
	if len(args) >= 3 && args[0] == "cp" {
		copyOut(args[1])
		return
	}

	if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
		ref := args[len(args)-1]
		if missing != "" && ref == missing {
			if _, err := os.Stat(pulled); err != nil {
				fmt.Fprintf(os.Stderr, "Error response from daemon: No such image: %s\n", ref)
				os.Exit(1)
			}
		}
		if format := flagValue(args, "-f"); strings.Contains(format, "Config") {
			config := os.Getenv("FAKEDOCKER_CONFIG")
			if config == "" {
				config = `{"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin"]}`
			}
			fmt.Println(config)
			return
		}
		size := os.Getenv("FAKEDOCKER_SIZE")
		if size == "" {
			size = "1000000"
		}
		fmt.Println(size)
		return
	}

	var dockerfile string
	if path := flagValue(args, "--file"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		dockerfile = string(data)
		if dest := os.Getenv("FAKEDOCKER_SAVE_CONTEXT"); dest != "" {
			if err := saveContext(filepath.Dir(path), dest); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
	}

	if log := os.Getenv("FAKEDOCKER_FAIL_WITH"); log != "" && len(args) > 0 && args[0] == "build" {
		fmt.Fprintln(os.Stderr, log)
		os.Exit(1)
	}

	if want := os.Getenv("FAKEDOCKER_FAIL_DOCKERFILE_CONTAINS"); want != "" && strings.Contains(dockerfile, want) {
		fmt.Fprintf(os.Stderr, "ERROR: failed to solve: failed to compute cache key: %q: not found\n", want)
		os.Exit(1)
	}
}

// saveContext copies the build context somewhere the test can read it
// after wrap has cleaned up.
func saveContext(from, to string) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
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
	for _, entry := range strings.Split(os.Getenv("FAKEDOCKER_ABSENT"), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if ok && name == string(ref) && value == path {
			// Byte for byte the shape docker reports a path an image
			// does not carry in.
			fmt.Fprintf(os.Stderr, "Error: No such container:path: %s:%s\n", id, path)
			os.Exit(1)
		}
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
