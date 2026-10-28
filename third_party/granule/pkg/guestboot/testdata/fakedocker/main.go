// Command fakedocker stands in for the docker CLI in tests of the
// packages that boot a guest -- guestbuild and guestverify, over
// guestboot's loop. It records every invocation (one line per call, to
// the file named by FAKEDOCKER_LOG) and answers the `docker inspect`
// queries those packages make, so the whole boot/provision/commit and
// boot/check/verdict sequences can be asserted without a docker daemon,
// a KVM host or a real VM.
//
//   - FAKEDOCKER_RUNNING sets what `inspect -f {{.State.Running}}`
//     prints (default "true").
//   - FAKEDOCKER_EXIT sets what `inspect -f {{.State.ExitCode}}` prints
//     (default "0"); "137" is how a guest that had to be SIGKILLed looks.
//   - FAKEDOCKER_FAIL_CONTAINS fails any call whose argv (joined with
//     spaces) contains it, so guestbuild's error paths can be exercised.
//   - FAKEDOCKER_HANG_CONTAINS hangs any call whose argv contains it
//     until the caller's own context kills it, which is what a guest
//     that never finishes booting looks like from out here.
//   - FAKEDOCKER_NO_SYSTEMD answers `systemctl` the way a guest that has
//     none does: exit 127 from the shell that could not find it. The
//     alpine variant runs OpenRC, so this is a guest granule publishes,
//     not a hypothetical.
//   - FAKEDOCKER_IMAGE_SALT changes what `image inspect -f {{.Id}}`
//     answers for a given reference, which is how a test moves a tag:
//     the same name resolving to different bytes.
//   - FAKEDOCKER_IMAGE_ENV is a comma-separated list of "KEY=value"
//     entries an `image inspect` of any image reports as its ENV, which
//     is where a machine records the settings its own VM SET wrote.
//   - FAKEDOCKER_MISSING_GUEST_FILES is a comma-separated list of paths
//     `docker cp` should report as absent, for an image whose guest half
//     brings no kernel (the ordinary case) or none at all. An entry of
//     the form "<image>=<path>" is absent from that image alone; a bare
//     path is absent from all of them. Everything else `docker cp`s as a
//     one-file tar, which is how the caller tells "present" from an
//     error.
//   - FAKEDOCKER_SAVE_STDIN, if set, is where the stdin of a
//     `granule cp` call is written. A staged tree reaches the guest as a
//     tar on that stream rather than as an argument, so it is the only
//     part of a build the recorded argv does not show.
//
// A script the guest is asked to run is interpreted just far enough for
// a test to say what the guest did with it, since a fake guest has no
// other way to have an opinion:
//
//	#fake-out: what the check printed
//	#fake-exit: 3
//
// Anything else in a script runs and succeeds.
//
// Beyond recording, it models just enough of a booting guest to make the
// race that used to break `guest build` reproducible in a unit test:
//
//   - Files. A staging command ("... > PATH && chmod 0755 PATH") creates
//     one, "rm -f PATH" removes it, and running a path that does not
//     exist fails the way the guest's own shell does -- exit 127 and
//     "No such file or directory", which is the error the real failure
//     arrived as.
//   - A boot that clears /tmp. Until the guest reports its boot
//     finished, anything written under /tmp is discarded: on a real
//     guest `systemd-tmpfiles --create --remove --boot` empties it a
//     second or two in, which is *after* granule-agent starts answering,
//     so a file staged there before the boot settles may or may not
//     still be there a moment later. Modelling that as "certainly gone"
//     rather than "gone if the timing is unlucky" is what makes the test
//     deterministic; a build that stages outside /tmp, or waits for the
//     boot before staging, is unaffected either way.
//   - Booting finishes when the guest is asked to wait for it
//     (`systemctl is-system-running --wait`), which is the only signal
//     it gets out here.
package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]

	logged := append([]string(nil), args...)
	if len(args) > 0 && args[0] == "build" {
		// The Dockerfile a build was handed is in a temporary directory
		// its caller deletes, so the only chance a test has to see what
		// was in it is here. Both spellings of the flag, because the
		// generated builds in this tree say "--file" and a hand-written
		// docker command says "-f".
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--file" || args[i] == "-f" {
				if data, err := os.ReadFile(args[i+1]); err == nil {
					logged = append(logged, string(data))
				}
			}
		}
	}

	logPath := os.Getenv("FAKEDOCKER_LOG")
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		// Newlines inside an argument -- the scrub script is one --
		// would otherwise split a single call across log lines.
		fmt.Fprintln(f, strings.ReplaceAll(strings.Join(logged, "\x1f"), "\n", "\x1e"))
		f.Close()
	}

	if len(args) == 0 {
		return
	}

	joined := strings.Join(args, " ")
	if want := os.Getenv("FAKEDOCKER_FAIL_CONTAINS"); want != "" && strings.Contains(joined, want) {
		fmt.Fprintf(os.Stderr, "fakedocker: failing on request: %s\n", want)
		os.Exit(1)
	}
	if want := os.Getenv("FAKEDOCKER_HANG_CONTAINS"); want != "" && strings.Contains(joined, want) {
		// The caller bounds this with a context; sleeping past any
		// timeout a test would set is the whole behaviour.
		time.Sleep(time.Hour)
	}

	if len(args) >= 4 && args[0] == "image" && args[1] == "inspect" && args[2] == "-f" {
		// A content digest derived from the reference, so that two
		// images differ and the same one is stable across calls --
		// which is what a verdict cache is keyed on.
		if args[3] == "{{.Id}}" {
			sum := sha256.Sum256([]byte(args[len(args)-1] + os.Getenv("FAKEDOCKER_IMAGE_SALT")))
			fmt.Printf("sha256:%x\n", sum)
		}
		// The ENV an image records, which is where a machine's own
		// sizing lives. An image that was given none answers nothing,
		// as one built before VM SET existed does.
		if strings.Contains(args[3], ".Config.Env") {
			for _, entry := range strings.Split(os.Getenv("FAKEDOCKER_IMAGE_ENV"), ",") {
				if entry = strings.TrimSpace(entry); entry != "" {
					fmt.Println(entry)
				}
			}
		}
		return
	}

	// A console log, so that a caller reporting one has something to
	// report: an error that says it is quoting the guest's console and
	// then quotes nothing is worse than one that does not try.
	if args[0] == "logs" {
		fmt.Println("[    0.000000] Linux version 6.1.0-fake (fakedocker)")
		fmt.Println("[    1.816000] granule-agent.service: started")
		return
	}

	if args[0] == "create" {
		// The reference goes into the container id so that the `cp` probe
		// below can answer per image: the guest image being rebased and
		// the granule image it is rebased onto are asked the same
		// questions and need not give the same answers.
		ref := ""
		if len(args) >= 2 {
			ref = args[1]
		}
		fmt.Println("fakecid-" + base64.RawURLEncoding.EncodeToString([]byte(ref)))
		return
	}

	// `docker cp <cid>:<path> -` is a presence probe: docker's own "no
	// such container:path" for a path the image does not carry, and a tar
	// of the file for one it does. The tar matters because the caller
	// reads one header out of it to decide -- an empty stdout and exit 0
	// reads as a broken copy rather than as "present".
	if args[0] == "cp" && len(args) >= 2 {
		id, path, _ := strings.Cut(args[1], ":")
		if missingGuestFile(id, path) {
			fmt.Fprintf(os.Stderr, "Error: No such container:path: %s:%s\n", id, path)
			os.Exit(1)
		}
		if len(args) >= 3 && args[2] == "-" {
			writeTar(path)
		}
		return
	}

	if len(args) >= 3 && args[0] == "inspect" && args[1] == "-f" {
		switch args[2] {
		case "{{.State.Running}}":
			fmt.Println(envOr("FAKEDOCKER_RUNNING", "true"))
		case "{{.State.ExitCode}}":
			fmt.Println(envOr("FAKEDOCKER_EXIT", "0"))
		}
		return
	}

	if isGuestCopy(args) {
		if dest := os.Getenv("FAKEDOCKER_SAVE_STDIN"); dest != "" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		return
	}

	if cmd, ok := guestCommand(args); ok && logPath != "" {
		os.Exit(runInGuest(logPath+".guest", cmd))
	}
}

// missingGuestFile reports whether FAKEDOCKER_MISSING_GUEST_FILES says
// this container's image has no such path. An entry is a bare path,
// which answers for every image, or "<ref>=<path>" for one image only --
// which is how a test gives a granule image a bundled guest kernel the
// image being verified has not got.
func missingGuestFile(id, path string) bool {
	ref, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "fakecid-"))
	if err != nil {
		ref = nil
	}
	for _, entry := range strings.Split(os.Getenv("FAKEDOCKER_MISSING_GUEST_FILES"), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if want, missing, ok := strings.Cut(entry, "="); ok {
			if want == string(ref) && missing == path {
				return true
			}
			continue
		}
		if entry == path {
			return true
		}
	}
	return false
}

// writeTar answers a `docker cp ... -` with the one-file archive docker
// would write. Errors are ignored on purpose: the caller cancels the
// copy as soon as it has read the first header, and a write to a pipe
// nobody is reading any more is that cancellation rather than a fault.
func writeTar(path string) {
	tw := tar.NewWriter(os.Stdout)
	body := "fakedocker: the contents of " + path + "\n"
	_ = tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     filepath.Base(path),
		Mode:     0o644,
		Size:     int64(len(body)),
	})
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
}

// isGuestCopy recognizes a staged tree on its way into the guest:
// "docker exec -i <container> granule cp -tar - <dir>", whose payload is
// on stdin rather than in the argv.
func isGuestCopy(args []string) bool {
	return len(args) >= 5 && args[0] == "exec" && args[1] == "-i" && args[3] == "granule" && args[4] == "cp"
}

// guestCommand pulls the guest's own argv out of a
// "docker exec <container> granule exec -- ..." invocation.
func guestCommand(args []string) ([]string, bool) {
	if len(args) < 6 || args[0] != "exec" || args[2] != "granule" || args[3] != "exec" || args[4] != "--" {
		return nil, false
	}
	return args[5:], true
}

// staged matches the command guestScript installs a script with, and
// removed the one guestbuild deletes it with.
var (
	staged  = regexp.MustCompile(`printf %s (\S+) \| base64 -d > '([^']+)' && chmod`)
	removed = regexp.MustCompile(`^rm -f -- '([^']+)'$`)

	// What a script may tell the fake guest to do with it.
	fakeOut  = regexp.MustCompile(`(?m)^#fake-out: ?(.*)$`)
	fakeExit = regexp.MustCompile(`(?m)^#fake-exit: ?(\d+)$`)
)

// runInGuest answers one guest command against the modelled guest in
// statePath, and returns the exit status the guest would have given.
func runInGuest(statePath string, cmd []string) int {
	g := loadGuest(statePath)
	defer func() { g.save(statePath) }()

	switch {
	case cmd[0] == "systemctl" && len(cmd) > 1 && cmd[1] == "is-system-running":
		if os.Getenv("FAKEDOCKER_NO_SYSTEMD") != "" {
			fmt.Fprintln(os.Stderr, "/bin/bash: line 1: systemctl: command not found")
			return 127
		}
		g.finishBoot()
		fmt.Println("running")
		return 0

	case cmd[0] == "sh" && len(cmd) == 3 && cmd[1] == "-c":
		script := strings.TrimSpace(cmd[2])
		if m := staged.FindStringSubmatch(script); m != nil {
			text, err := base64.StdEncoding.DecodeString(m[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "base64: invalid input\n")
				return 1
			}
			g.write(m[2], string(text))
		}
		if m := removed.FindStringSubmatch(script); m != nil {
			delete(g.files, m[1])
		}
		return 0

	case strings.HasPrefix(cmd[0], "/"):
		script, ok := g.files[cmd[0]]
		if !ok {
			// Byte for byte the shape of the failure this models: the
			// guest's shell cannot find the script the build just wrote.
			fmt.Fprintf(os.Stderr, "/bin/bash: line 1: %s: No such file or directory\n", cmd[0])
			return 127
		}
		for _, m := range fakeOut.FindAllStringSubmatch(script, -1) {
			fmt.Println(m[1])
		}
		if m := fakeExit.FindStringSubmatch(script); m != nil {
			code, _ := strconv.Atoi(m[1])
			return code
		}
		return 0
	}
	return 0
}

// guest is the modelled guest: what it has on disk, and whether its boot
// has finished.
type guest struct {
	booted bool
	// files maps a path in the guest to the text written there, since a
	// script's own content is the only thing a fake guest can have an
	// opinion about.
	files map[string]string
}

func loadGuest(path string) *guest {
	g := &guest{files: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return g
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "booted":
			g.booted = true
		case strings.HasPrefix(line, "f "):
			rest := strings.TrimPrefix(line, "f ")
			path, encoded, _ := strings.Cut(rest, " ")
			text, _ := base64.StdEncoding.DecodeString(encoded)
			g.files[path] = string(text)
		}
	}
	return g
}

func (g *guest) save(path string) {
	var b strings.Builder
	if g.booted {
		b.WriteString("booted\n")
	}
	for f, text := range g.files {
		fmt.Fprintf(&b, "f %s %s\n", f, base64.StdEncoding.EncodeToString([]byte(text)))
	}
	// Best effort: a fake docker that cannot persist its guest fails the
	// test that depends on it, which is the report we want anyway.
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

// write creates a file, unless it is one the boot is about to delete.
func (g *guest) write(path, text string) {
	if !g.booted && underTmp(path) {
		return
	}
	g.files[path] = text
}

// finishBoot is the boot-time clean landing: whatever was written to
// /tmp before the guest settled is gone.
func (g *guest) finishBoot() {
	g.booted = true
	for f := range g.files {
		if underTmp(f) {
			delete(g.files, f)
		}
	}
}

func underTmp(path string) bool {
	return filepath.Dir(path) == "/tmp" || strings.HasPrefix(path, "/tmp/")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
