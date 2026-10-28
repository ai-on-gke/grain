// Command fakedocker stands in for the docker CLI in tests of the
// dockervm package: it records every invocation it's given (one line per
// call, to the file named by FAKEDOCKER_LOG) so tests can assert on the
// exact sequence and arguments of "docker" commands dockervm issues,
// without needing a real docker daemon.
//
//   - FAKEDOCKER_MISSING names a comma-separated set of container names
//     that "stop"/"rm"/"inspect" should fail against with the same "No
//     such container"/"No such object" messages the real docker CLI uses,
//     so dockervm's not-found handling can be exercised too.
//   - FAKEDOCKER_FAIL_CONTAINS, if set, fails any call whose argv (joined
//     with spaces) contains it, so callers' error handling can be
//     exercised without needing a specific real failure mode.
//   - FAKEDOCKER_RUNNING sets what `inspect -f {{.State.Running}}`
//     prints (default "true").
//   - FAKEDOCKER_EXEC_EXIT sets the status an "exec" call exits with,
//     standing in for the guest command's own (default "0").
//   - FAKEDOCKER_PROBE_FAIL makes the readiness probe ("granule ready",
//     see dockervm.readyProbeArgs) fail that many times before it
//     succeeds, which is how a guest that is still booting looks; -1
//     fails every one, for a guest that never comes up. It counts the
//     "exec" lines already in FAKEDOCKER_LOG, so it needs one to be set.
//     The probe is answered separately from FAKEDOCKER_EXEC_EXIT so a
//     test can have a guest that is reachable and a command that exits
//     non-zero.
//   - FAKEDOCKER_NO_READY_MODE makes the probe fail the way an image
//     predating "granule ready" does, with cmd/granule-shim's own "unknown
//     mode" line, which is what sends dockervm to its fallback probe.
//   - FAKEDOCKER_NO_EXEC_MODE does the same for "granule exec", so a
//     container with neither mode -- one there is no probe left for --
//     can be exercised too.
//   - FAKEDOCKER_EXPOSED_PORTS is what `image inspect -f
//     {{...ExposedPorts...}}` prints, space-separated, standing in for
//     what the image a VM runs declares with EXPOSE.
//   - FAKEDOCKER_PORTS is a comma-separated set of lines `port` prints,
//     standing in for the host ports docker assigned a published port. A
//     container named by FAKEDOCKER_MISSING fails `port` too, the way a
//     real one does, since a VM's published ports are asked for long
//     after its create as well (see dockervm.Published).
//   - FAKEDOCKER_IMAGE_ENV is a comma-separated "KEY=value" list that
//     `image inspect -f {{range .Config.Env}}...` prints, standing in for
//     the settings a machine image records for itself with ENV -- where a
//     Granule's "VM SET" writes them, and what granule reads to boot a
//     machine the way it asks to be booted.
//   - FAKEDOCKER_CONTAINER_ENV is a comma-separated "KEY=value" list
//     that `inspect -f {{range .Config.Env}}...` prints, standing in for
//     the environment a VM container was created with. The fallback
//     probe reads GRANULE_READY_FILE from it.
//   - FAKEDOCKER_PROBE_UNDECLARED makes the probe fail the way a VM with
//     a -ready-file whose guest has not created that file does: the
//     guest answered, so "granule ready" exits guestexec.ExitNotDeclared
//     (2) rather than 1. It is what tells dockervm that the boot is done
//     and the workload is what is outstanding. The fallback probe's own
//     "test -e" answers it the way that guest would too, with a 1.
//
// An "exec" call copies its stdin to stdout, so tests can assert that a
// session's stdin actually reaches the guest command.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	args := os.Args[1:]

	logPath := os.Getenv("FAKEDOCKER_LOG")
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(f, strings.Join(args, "\x1f"))
		f.Close()
	}

	if len(args) == 0 {
		os.Exit(0)
	}

	if fail := os.Getenv("FAKEDOCKER_FAIL_CONTAINS"); fail != "" && strings.Contains(strings.Join(args, " "), fail) {
		fmt.Fprintf(os.Stderr, "fakedocker: forced failure (matched %q)\n", fail)
		os.Exit(1)
	}

	switch args[0] {
	case "stop", "rm":
		if missing(args[len(args)-1]) {
			fmt.Fprintf(os.Stderr, "Error response from daemon: No such container: %s\n", args[len(args)-1])
			os.Exit(1)
		}
	case "inspect":
		name := args[len(args)-1]
		if missing(name) {
			fmt.Fprintf(os.Stderr, "Error response from daemon: No such object: %s\n", name)
			os.Exit(1)
		}
		if len(args) >= 3 && args[1] == "-f" && args[2] == "{{.State.Running}}" {
			fmt.Println(envOr("FAKEDOCKER_RUNNING", "true"))
			os.Exit(0)
		}
		if len(args) >= 3 && args[1] == "-f" && strings.Contains(args[2], ".Config.Env") {
			for _, e := range strings.Split(os.Getenv("FAKEDOCKER_CONTAINER_ENV"), ",") {
				if e != "" {
					fmt.Println(e)
				}
			}
			os.Exit(0)
		}
	case "image":
		// `image inspect -f {{...ExposedPorts...}}`: what the image a VM
		// is about to run declares with EXPOSE, which is what
		// -publish-declared publishes. FAKEDOCKER_EXPOSED_PORTS is
		// space-separated, and empty stands for an image that declares
		// nothing.
		if len(args) >= 2 && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "ExposedPorts") {
			fmt.Println(os.Getenv("FAKEDOCKER_EXPOSED_PORTS"))
			os.Exit(0)
		}
		// `image inspect -f {{range .Config.Env}}...`: what the image
		// records with ENV, which for a machine is where its own settings
		// live (a Granule's "VM SET", docs/granulefile.md §5.5).
		// FAKEDOCKER_IMAGE_ENV is a comma-separated "KEY=value" list, and
		// empty stands for an image that records nothing of its own.
		if len(args) >= 2 && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), ".Config.Env") {
			for _, e := range strings.Split(os.Getenv("FAKEDOCKER_IMAGE_ENV"), ",") {
				if e != "" {
					fmt.Println(e)
				}
			}
			os.Exit(0)
		}
	case "port":
		// The host side of what was published, which real docker only
		// knows once the container is up. FAKEDOCKER_PORTS is a
		// comma-separated set of "docker port" lines; empty stands for a
		// docker with nothing to report.
		if missing(args[len(args)-1]) {
			fmt.Fprintf(os.Stderr, "Error response from daemon: No such container: %s\n", args[len(args)-1])
			os.Exit(1)
		}
		for _, line := range strings.Split(os.Getenv("FAKEDOCKER_PORTS"), ",") {
			if line != "" {
				fmt.Println(line)
			}
		}
		os.Exit(0)
	case "exec":
		io.Copy(os.Stdout, os.Stdin)
		if isReadinessProbe(args) {
			if os.Getenv("FAKEDOCKER_NO_READY_MODE") != "" {
				fmt.Fprintln(os.Stderr, `granule: unknown mode "ready" (want "run", "netshim", "exec", "cp", "resize", "ready" or "sleep")`)
				os.Exit(1)
			}
			if os.Getenv("FAKEDOCKER_PROBE_UNDECLARED") != "" {
				fmt.Fprintln(os.Stderr, "granule: ready: the guest has not declared itself ready: granule-agent answered, but /run/granule/ready does not exist inside the guest yet (GRANULE_READY_FILE)")
				os.Exit(2)
			}
			n, _ := strconv.Atoi(os.Getenv("FAKEDOCKER_PROBE_FAIL"))
			if n < 0 || (n > 0 && execCalls(logPath, "") <= n) {
				fmt.Fprintln(os.Stderr, "fakedocker: guest not reachable yet")
				os.Exit(1)
			}
			os.Exit(0)
		}
		if isFallbackProbe(args) {
			if os.Getenv("FAKEDOCKER_NO_EXEC_MODE") != "" {
				fmt.Fprintln(os.Stderr, `granule: unknown mode "exec" (want "run", "netshim" or "sleep")`)
				os.Exit(1)
			}
			// The declaration half, which an old image is asked as an
			// ordinary "test -e" rather than through a mode of its own.
			if args[len(args)-2] == "-e" {
				if os.Getenv("FAKEDOCKER_PROBE_UNDECLARED") != "" {
					os.Exit(1)
				}
				os.Exit(0)
			}
			n, _ := strconv.Atoi(os.Getenv("FAKEDOCKER_PROBE_FAIL"))
			if n < 0 || (n > 0 && execCalls(logPath, "true") <= n) {
				fmt.Fprintln(os.Stderr, "fakedocker: guest not reachable yet")
				os.Exit(1)
			}
			os.Exit(0)
		}
		code, _ := strconv.Atoi(os.Getenv("FAKEDOCKER_EXEC_EXIT"))
		os.Exit(code)
	}

	fmt.Println("fakedocker0123456789")
	os.Exit(0)
}

// isReadinessProbe reports whether this "exec" is dockervm's readiness
// probe rather than a guest command a caller asked for: the probe is the
// only thing that runs granule's "ready" mode.
func isReadinessProbe(args []string) bool {
	for i, a := range args {
		if a == "granule" && i+1 < len(args) && args[i+1] == "ready" {
			return true
		}
	}
	return false
}

// isFallbackProbe reports whether this "exec" is dockervm's fallback
// readiness probe -- the "granule exec" it falls back to for an image
// with no "ready" mode -- rather than a guest command a caller asked
// for. The two run the same binary, so what tells them apart is the
// flag: dockervm.Exec always passes -i to proxy stdin, and a probe never
// does.
func isFallbackProbe(args []string) bool {
	if len(args) < 5 || args[1] == "-i" {
		return false
	}
	for i, a := range args {
		if a == "granule" && i+1 < len(args) && args[i+1] == "exec" {
			return true
		}
	}
	return false
}

// missing reports whether name is one of FAKEDOCKER_MISSING's containers.
func missing(name string) bool {
	for _, m := range strings.Split(os.Getenv("FAKEDOCKER_MISSING"), ",") {
		if m != "" && m == name {
			return true
		}
	}
	return false
}

// execCalls counts the "exec" invocations recorded in the log so far,
// this one included. A non-empty suffix counts only the calls whose last
// argument is it, which is how the fallback probe's own retries are
// counted apart from the "granule ready" attempt that precedes each one.
func execCalls(logPath, suffix string) int {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "exec\x1f") {
			continue
		}
		if suffix != "" && !strings.HasSuffix(line, "\x1f"+suffix) {
			continue
		}
		n++
	}
	return n
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
