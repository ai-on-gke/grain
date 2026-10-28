package guestdockerfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-guestdockerfile")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "./testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

// build runs one Build against the fake docker and returns the argv it
// was invoked with and the Dockerfile it was handed on stdin.
func build(t *testing.T, opts Options) (argv []string, dockerfile string, stdout string, err error) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "docker.args")
	t.Setenv("FAKEDOCKER_LOG", logPath)

	var out bytes.Buffer
	t.Setenv(dockercli.EnvBinary, fakedockerPath)
	opts.Stdout = &out
	opts.Stderr = &out
	err = Build(context.Background(), opts)

	if data, rerr := os.ReadFile(logPath); rerr == nil {
		argv = strings.Split(string(data), "\x1f")
	}
	if data, rerr := os.ReadFile(logPath + ".stdin"); rerr == nil {
		dockerfile = string(data)
	}
	return argv, dockerfile, out.String(), err
}

const simple = `ARG NGINX_CONF=default
RUN apt-get update && apt-get install -y nginx
COPY ${NGINX_CONF} /etc/nginx/nginx.conf
RUN systemctl enable nginx
`

// TestBuildDrivesDocker: one `docker build`, the file on its stdin, and
// the consumer's own instructions in it unchanged. Everything granule
// does here is in those two facts.
func TestBuildDrivesDocker(t *testing.T) {
	argv, dockerfile, stdout, err := build(t, Options{
		File:           "guest/Dockerfile",
		Source:         simple,
		Context:        "guest",
		Tag:            "my-guest:dev",
		BuildArgs:      []string{"NGINX_CONF=prod.conf"},
		ExtraBuildArgs: []string{"--no-cache"},
	})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}

	want := []string{"build", "-f", "-", "-t", "my-guest:dev", "--build-arg", "NGINX_CONF=prod.conf", "--no-cache", "guest"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("docker argv:\n got %v\nwant %v", argv, want)
	}

	// The base is granule's, first, and the caller never named one.
	if !strings.Contains(dockerfile, "\nFROM "+DefaultBase+"\n") {
		t.Errorf("the built file does not carry granule's own FROM:\n%s", dockerfile)
	}
	if !strings.HasSuffix(dockerfile, simple) {
		t.Errorf("the consumer's instructions are not in the built file verbatim:\n%s", dockerfile)
	}
	// Docker's line numbers are the consumer's plus the header, and a
	// build says so rather than leaving it to be worked out.
	if n := HeaderLines(Options{File: "guest/Dockerfile", Source: simple}); !strings.Contains(dockerfile[:len(dockerfile)-len(simple)], "guest/Dockerfile") ||
		!strings.Contains(stdout, "first "+strconv.Itoa(n)+" lines") {
		t.Errorf("a build should say how far docker's line numbers are off (header %d lines):\n%s", n, stdout)
	}
}

func TestBuildUsesTheBaseItIsGiven(t *testing.T) {
	_, dockerfile, _, err := build(t, Options{
		File:    "Dockerfile",
		Source:  simple,
		Context: ".",
		From:    "ghcr.io/someone/granule-guest-rootfs:2026-09",
		Tag:     "my-guest:dev",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(dockerfile, "FROM ghcr.io/someone/granule-guest-rootfs:2026-09\n") {
		t.Errorf("-from was not honoured:\n%s", dockerfile)
	}
}

// TestBuildUsesTheFilesOwnBase: a file that names a FROM is built on
// that, with no FROM of granule's prepended -- the base supplies the
// filesystem payload, and granule's own half is re-laid at pack time
// rather than inherited from it.
func TestBuildUsesTheFilesOwnBase(t *testing.T) {
	src := "FROM ghcr.io/example/my-guest-rootfs:v3\n" + simple
	_, dockerfile, stdout, err := build(t, Options{
		File:    "Dockerfile",
		Source:  src,
		Context: ".",
		Tag:     "my-guest:dev",
	})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}
	if strings.Contains(dockerfile, DefaultBase) {
		t.Errorf("granule supplied a base for a file that named its own:\n%s", dockerfile)
	}
	if n := strings.Count(dockerfile, "\nFROM "); n != 1 {
		t.Errorf("the built file has %d FROM instructions, want the file's own only:\n%s", n, dockerfile)
	}
	if !strings.HasSuffix(dockerfile, src) {
		t.Errorf("the consumer's instructions are not in the built file verbatim:\n%s", dockerfile)
	}
	if !strings.Contains(stdout, "ghcr.io/example/my-guest-rootfs:v3 (named by the file)") {
		t.Errorf("the build does not say whose base it is standing on:\n%s", stdout)
	}
	// And the line-number offset still holds for a header of this
	// second shape, which is a different number of lines.
	if !strings.Contains(stdout, "first "+strconv.Itoa(HeaderLines(Options{File: "Dockerfile", Source: src}))+" lines") {
		t.Errorf("the header offset reported does not match this file's header:\n%s", stdout)
	}
}

// A build stage may be anything at all -- that is what multi-stage is
// for -- because nothing of it reaches the guest but what a COPY takes
// out of it. Only the last FROM is the base.
func TestBuildAcceptsMultiStage(t *testing.T) {
	src := `FROM golang:1.23 AS builder
RUN go build -o /out/app ./cmd/app

FROM ghcr.io/example/my-guest-rootfs:v3
COPY --from=builder /out/app /usr/bin/app
`
	_, dockerfile, stdout, err := build(t, Options{File: "Dockerfile", Source: src, Context: ".", Tag: "my-guest:dev"})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}
	if !strings.HasSuffix(dockerfile, src) {
		t.Errorf("the stages are not in the built file verbatim:\n%s", dockerfile)
	}
	if !strings.Contains(stdout, "ghcr.io/example/my-guest-rootfs:v3 (named by the file)") {
		t.Errorf("the base reported is not the last stage's:\n%s", stdout)
	}
}

// TestRebaseIsAFromOnlyDockerfile is the equivalence this front end
// buys: a file whose only instruction is a FROM of a guest image is the
// complete form of a rebase -- the image's payload as it stands, with
// granule's own half discarded and re-laid from the current build at pack
// time. docs/guest-images.md states it; this is it as a build.
//
// What the *packing* then does with that file is asserted in
// pkg/guestpack (TestPackGuestRelaysGranuleSHalfOverAnOlderGuest):
// here the claim is only that such a file builds, unchanged, with
// nothing of granule's own inserted above it.
func TestRebaseIsAFromOnlyDockerfile(t *testing.T) {
	const src = "FROM ghcr.io/example/my-guest:v1\n"
	_, dockerfile, stdout, err := build(t, Options{File: "Dockerfile", Source: src, Context: ".", Tag: "my-guest:v2"})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}
	if !strings.HasSuffix(dockerfile, src) {
		t.Errorf("the built file is not the consumer's base and nothing else:\n%s", dockerfile)
	}
	if n := strings.Count(dockerfile, "\nFROM "); n != 1 {
		t.Errorf("the built file names %d bases, want 1:\n%s", n, dockerfile)
	}
	// And the build says what it is doing, since "a Dockerfile with one
	// line in it" is otherwise indistinguishable from a mistake.
	if !strings.Contains(stdout, "repack of ghcr.io/example/my-guest:v1") {
		t.Errorf("the build does not say that a base and nothing else is a repack:\n%s", stdout)
	}
}

// Two bases is a build where one of them is not happening, so it is an
// error rather than a silent precedence rule.
func TestBuildRefusesTwoBases(t *testing.T) {
	_, _, _, err := build(t, Options{
		File:    "guest/Dockerfile",
		Source:  "FROM ghcr.io/example/my-guest-rootfs:v3\nRUN echo hi\n",
		Context: "guest",
		From:    "granule-guest-rootfs:latest",
		Tag:     "my-guest:dev",
	})
	if err == nil {
		t.Fatal("Build accepted a file that names its own base under -from")
	}
	for _, want := range []string{"guest/Dockerfile:1:", "ghcr.io/example/my-guest-rootfs:v3", "granule-guest-rootfs:latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q: %v", want, err)
		}
	}
}

// SHELL is docker's own and granule does nothing with it but let it
// past: the assertion is that it reaches the build docker actually
// runs, in the position the consumer put it, since an instruction that
// validated and then vanished would be the failure this package exists
// to avoid.
func TestBuildPassesShellToDocker(t *testing.T) {
	src := "SHELL [\"/bin/bash\", \"-euo\", \"pipefail\", \"-c\"]\nRUN curl -fsS https://example.com/x | tar -xz -C /opt\n"
	_, dockerfile, stdout, err := build(t, Options{File: "Dockerfile", Source: src, Context: ".", Tag: "my-guest:dev"})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}
	if !strings.HasSuffix(dockerfile, src) {
		t.Errorf("SHELL did not reach docker unchanged, above the RUN it governs:\n%s", dockerfile)
	}
}

// EXPOSE builds, and the build says what it does and does not mean --
// the declaration is recorded and nothing is opened by it.
func TestBuildNotesWhatExposeMeans(t *testing.T) {
	_, dockerfile, stdout, err := build(t, Options{
		File:    "Dockerfile",
		Source:  "RUN apt-get install -y nginx\nEXPOSE 8080\n",
		Context: ".",
		Tag:     "my-guest:dev",
	})
	if err != nil {
		t.Fatalf("Build: %v (output %q)", err, stdout)
	}
	if !strings.Contains(dockerfile, "EXPOSE 8080") {
		t.Errorf("EXPOSE did not reach docker:\n%s", dockerfile)
	}
	for _, want := range []string{"opens nothing", "OCI config"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the build does not say what EXPOSE means here (%q):\n%s", want, stdout)
		}
	}
}

// A file granule refuses is refused before docker is invoked at all: the
// point of validating is to fail in a second rather than after a
// four-minute build of something that was never going to boot.
func TestBuildRefusesBeforeBuilding(t *testing.T) {
	argv, dockerfile, _, err := build(t, Options{
		File:    "Dockerfile",
		Source:  "FROM ubuntu\nVOLUME /var/lib/data\n",
		Context: ".",
		Tag:     "my-guest:dev",
	})
	var verr *Error
	if !errors.As(err, &verr) {
		t.Fatalf("Build returned %T (%v), want *Error", err, err)
	}
	if argv != nil || dockerfile != "" {
		t.Errorf("docker was invoked anyway: %v", argv)
	}
}

// A docker build that fails fails the guest build, with docker's own
// output already on the caller's stderr.
func TestBuildReportsDockerFailure(t *testing.T) {
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "my-guest:dev")
	_, _, stdout, err := build(t, Options{
		File:    "Dockerfile",
		Source:  simple,
		Context: ".",
		Tag:     "my-guest:dev",
	})
	if err == nil || !strings.Contains(err.Error(), "docker build") {
		t.Fatalf("Build = %v, want a docker build failure", err)
	}
	if strings.Contains(stdout, "built my-guest:dev") {
		t.Errorf("a failed build claimed success:\n%s", stdout)
	}
}

func TestBuildRequires(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"no tag", Options{File: "Dockerfile", Source: simple, Context: "."}, "tag is required"},
		{"no file", Options{Source: simple, Context: ".", Tag: "t"}, "Dockerfile is required"},
		{"no context", Options{File: "Dockerfile", Source: simple, Tag: "t"}, "context is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Build(context.Background(), tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Build = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestDockerfilePrints is the -print-dockerfile path: what a caller is
// shown is exactly what docker would be handed.
func TestDockerfilePrints(t *testing.T) {
	opts := Options{File: "Dockerfile", Source: simple, Context: ".", Tag: "t"}
	text, err := Dockerfile(opts)
	if err != nil {
		t.Fatalf("Dockerfile: %v", err)
	}
	if got := strings.Count(text[:strings.Index(text, "\nFROM ")+1], "\n") + 1; got != HeaderLines(opts) {
		t.Errorf("FROM is on line %d of a %d-line header", got, HeaderLines(opts))
	}
	if !strings.HasSuffix(text, simple) {
		t.Errorf("printed file does not end with the consumer's own:\n%s", text)
	}
}
