package guestdockerfile

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateRefuses is the subset, stated as a test: one case per
// instruction granule does not implement, each asserting that the message
// names the file, the line and the instruction. Those three are the
// whole contract of a refusal -- a build that says "unsupported
// instruction" and stops is one the reader has to bisect their own file
// against.
func TestValidateRefuses(t *testing.T) {
	// Every case's refused instruction is on line 3, under two lines
	// that are always fine, so the line assertion means something.
	const preamble = "# a comment\nRUN echo hello\n"

	for _, tc := range []struct {
		name string
		// instruction is the line under test, and also what its own
		// refusal has to name.
		instruction string
		line        string
		// wants are phrases the reason has to carry: what to write
		// instead, or what granule does instead.
		wants []string
	}{{
		// FROM itself is supported; a FROM that names nothing is not an
		// image granule could not use but a line docker cannot read.
		name:        "FROM with no image",
		instruction: "FROM",
		line:        "FROM --platform=linux/amd64",
		wants:       []string{"names no image", "last FROM"},
	}, {
		name:        "VOLUME",
		instruction: "VOLUME",
		line:        "VOLUME /var/lib/myapp",
		wants:       []string{"packed disk image"},
	}, {
		name:        "HEALTHCHECK",
		instruction: "HEALTHCHECK",
		line:        "HEALTHCHECK CMD curl -f http://localhost/ || exit 1",
		wants:       []string{"granule vm status"},
	}, {
		name:        "STOPSIGNAL",
		instruction: "STOPSIGNAL",
		line:        "STOPSIGNAL SIGTERM",
		wants:       []string{"ACPI power button"},
	}, {
		name:        "ONBUILD",
		instruction: "ONBUILD",
		line:        "ONBUILD RUN echo later",
		wants:       []string{"defers instructions"},
	}, {
		name:        "MAINTAINER",
		instruction: "MAINTAINER",
		line:        "MAINTAINER somebody <somebody@example.com>",
		wants:       []string{"LABEL org.opencontainers.image.authors"},
	}, {
		name:        "ADD of a URL",
		instruction: "ADD",
		line:        "ADD https://example.com/app.tar.gz /srv/",
		wants:       []string{"https://example.com/app.tar.gz", "COPY it out of the build context", "checksum"},
	}, {
		name:        "ADD of a git remote",
		instruction: "ADD",
		line:        "ADD git@github.com:someone/repo.git /src",
		wants:       []string{"git@github.com:someone/repo.git"},
	}, {
		name:        "ADD of a URL among local paths",
		instruction: "ADD",
		line:        "ADD ./local.txt https://example.com/app.tgz /srv/",
		wants:       []string{"https://example.com/app.tgz"},
	}, {
		// A name no builder has and an instruction granule has not got to
		// are different answers and read differently (granulefile §10.3);
		// this one is the first.
		name:        "an instruction nobody has",
		instruction: "INSTALL",
		line:        "INSTALL nginx",
		wants:       []string{"not an instruction granule knows"},
	}, {
		// A VM instruction is the second: the language has it, this
		// command does not build it.
		name:        "a VM instruction, which this command does not build",
		instruction: "VM PACK",
		line:        "VM PACK",
		wants:       []string{"unimplemented", "docs/granulefile.md"},
	}, {
		// The lower-case spelling docker accepts is the same
		// instruction, and has to be refused as the same one.
		name:        "a refused instruction in lower case",
		instruction: "VOLUME",
		line:        "volume /var/lib/myapp",
		wants:       []string{"packed disk image"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate("guest/Dockerfile", preamble+tc.line+"\n")
			if err == nil {
				t.Fatalf("Validate accepted %q", tc.line)
			}
			var verr *Error
			if !errors.As(err, &verr) {
				t.Fatalf("Validate returned %T (%v), want *Error", err, err)
			}
			if len(verr.Violations) != 1 {
				t.Fatalf("refused %d instructions, want 1: %v", len(verr.Violations), err)
			}
			v := verr.Violations[0]
			if v.Instruction != tc.instruction {
				t.Errorf("named instruction %q, want %q", v.Instruction, tc.instruction)
			}
			if v.Line != 3 {
				t.Errorf("named line %d, want 3", v.Line)
			}
			msg := err.Error()
			for _, want := range []string{"guest/Dockerfile:3:", tc.instruction} {
				if !strings.Contains(msg, want) {
					t.Errorf("message does not carry %q:\n%s", want, msg)
				}
			}
			for _, want := range tc.wants {
				if !strings.Contains(v.Reason, want) {
					t.Errorf("reason does not carry %q:\n%s", want, v.Reason)
				}
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []Instruction
	}{{
		name: "the supported instructions",
		src: `ARG VERSION=1.2.3
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y nginx
COPY nginx.service /etc/systemd/system/nginx.service
RUN systemctl enable nginx
WORKDIR /srv
USER nginx
LABEL org.opencontainers.image.title="my guest"
ENTRYPOINT ["/usr/bin/myapp"]
CMD ["--serve"]
`,
		want: []Instruction{
			{Name: "ARG", Args: "VERSION=1.2.3", Line: 1},
			{Name: "ENV", Args: "DEBIAN_FRONTEND=noninteractive", Line: 2},
			{Name: "RUN", Args: "apt-get update && apt-get install -y nginx", Line: 3},
			{Name: "COPY", Args: "nginx.service /etc/systemd/system/nginx.service", Line: 4},
			{Name: "RUN", Args: "systemctl enable nginx", Line: 5},
			{Name: "WORKDIR", Args: "/srv", Line: 6},
			{Name: "USER", Args: "nginx", Line: 7},
			{Name: "LABEL", Args: `org.opencontainers.image.title="my guest"`, Line: 8},
			{Name: "ENTRYPOINT", Args: `["/usr/bin/myapp"]`, Line: 9},
			{Name: "CMD", Args: `["--serve"]`, Line: 10},
		},
	}, {
		// ADD of a local path is COPY with tar extraction, which is a
		// thing people write on purpose.
		name: "ADD of local paths",
		src:  "ADD --chown=root:root ./rootfs.tar.gz /\n",
		want: []Instruction{{Name: "ADD", Args: "--chown=root:root ./rootfs.tar.gz /", Line: 1}},
	}, {
		// COPY --from naming an image (rather than a stage, which a
		// single-stage file has none of) is how a build gets an artifact
		// out of a builder image without running any of it.
		name: "COPY --from an image",
		src:  "COPY --from=ghcr.io/someone/builder:1 /out/app /usr/local/bin/app\n",
		want: []Instruction{{Name: "COPY", Args: "--from=ghcr.io/someone/builder:1 /out/app /usr/local/bin/app", Line: 1}},
	}, {
		// SHELL is build-time and entirely docker's: the pipefail form
		// below is the reason to have it, since without it a failure in
		// the middle of a RUN's pipeline is ignored.
		name: "SHELL",
		src:  "SHELL [\"/bin/bash\", \"-euo\", \"pipefail\", \"-c\"]\nRUN curl -fsSL https://example.com/x | tar -xz -C /opt\n",
		want: []Instruction{
			{Name: "SHELL", Args: `["/bin/bash", "-euo", "pipefail", "-c"]`, Line: 1},
			{Name: "RUN", Args: "curl -fsSL https://example.com/x | tar -xz -C /opt", Line: 2},
		},
	}, {
		// EXPOSE is accepted and recorded rather than honoured: it is
		// the image's declaration of what it wants reachable, and
		// something else grants it.
		name: "EXPOSE",
		src:  "EXPOSE 8080\nEXPOSE 53/udp\n",
		want: []Instruction{
			{Name: "EXPOSE", Args: "8080", Line: 1},
			{Name: "EXPOSE", Args: "53/udp", Line: 2},
		},
	}, {
		name: "a base of the file's own",
		src:  "FROM ghcr.io/example/my-guest-rootfs:v3\nRUN apt-get update\n",
		want: []Instruction{
			{Name: "FROM", Args: "ghcr.io/example/my-guest-rootfs:v3", Line: 1},
			{Name: "RUN", Args: "apt-get update", Line: 2},
		},
	}, {
		// The complete form of a rebase, written as a Dockerfile: take
		// this image's payload, discard granule's half, re-lay the
		// current one. Nothing to do to it is the whole instruction.
		name: "nothing but a base",
		src:  "FROM ghcr.io/example/my-guest:v1\n",
		want: []Instruction{{Name: "FROM", Args: "ghcr.io/example/my-guest:v1", Line: 1}},
	}, {
		// Multi-stage: only the final stage is the guest's base, so a
		// builder stage may be anything at all -- which is the point of
		// allowing it.
		name: "multi-stage with a builder that is not a guest at all",
		src: `FROM golang:1.23 AS builder
RUN go build -o /out/app ./cmd/app

FROM ghcr.io/example/my-guest-rootfs:v3
COPY --from=builder /out/app /usr/bin/app
`,
		want: []Instruction{
			{Name: "FROM", Args: "golang:1.23 AS builder", Line: 1},
			{Name: "RUN", Args: "go build -o /out/app ./cmd/app", Line: 2},
			{Name: "FROM", Args: "ghcr.io/example/my-guest-rootfs:v3", Line: 4},
			{Name: "COPY", Args: "--from=builder /out/app /usr/bin/app", Line: 5},
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Validate("Dockerfile", tc.src)
			if err != nil {
				t.Fatalf("Validate refused a supported file: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d instructions, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("instruction %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestValidateReportsEveryViolation: a consumer porting a container
// Dockerfile trips several of these at once, and one refusal per build
// turns one edit into three builds.
func TestValidateReportsEveryViolation(t *testing.T) {
	src := `FROM ubuntu:24.04
RUN apt-get install -y nginx
VOLUME /var/lib/nginx
STOPSIGNAL SIGTERM
HEALTHCHECK CMD curl -f http://localhost/ || exit 1
`
	err := errorFrom(t, src)
	if got, want := len(err.Violations), 3; got != want {
		t.Fatalf("refused %d instructions, want %d: %v", got, want, err)
	}
	for i, want := range []Violation{{Line: 3, Instruction: "VOLUME"}, {Line: 4, Instruction: "STOPSIGNAL"}, {Line: 5, Instruction: "HEALTHCHECK"}} {
		if got := err.Violations[i]; got.Line != want.Line || got.Instruction != want.Instruction {
			t.Errorf("violation %d: got %s at line %d, want %s at line %d", i, got.Instruction, got.Line, want.Instruction, want.Line)
		}
	}
	// And the whole message says what may be used, since a reader who
	// has just had four lines refused is asking exactly that.
	for _, want := range []string{"RUN, COPY", "deploy/guest-image/README.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not carry %q:\n%s", want, err)
		}
	}
}

// TestValidateParserDirectives: a "# syntax=" line is a comment to this
// package's parser and a frontend swap to docker, and it is not even
// that once granule has prepended its own header -- so it is named rather
// than silently ignored.
func TestValidateParserDirectives(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{{
		name: "syntax",
		src:  "# syntax=docker/dockerfile:1\nRUN echo hi\n",
		want: "# syntax=",
	}, {
		name: "escape",
		src:  "# escape=`\nRUN echo hi\n",
		want: "# escape=",
	}, {
		name: "check",
		src:  "# check=error=true\nRUN echo hi\n",
		want: "# check=",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			err := errorFrom(t, tc.src)
			if len(err.Violations) != 1 || err.Violations[0].Instruction != tc.want {
				t.Fatalf("got %v, want one %s violation", err.Violations, tc.want)
			}
			if err.Violations[0].Line != 1 {
				t.Errorf("named line %d, want 1", err.Violations[0].Line)
			}
		})
	}

	// Below the first non-comment line it is an ordinary comment to
	// docker too, and refusing it would be an error about nothing.
	if _, err := Validate("Dockerfile", "RUN echo hi\n# syntax=docker/dockerfile:1\n"); err != nil {
		t.Errorf("refused a syntax comment docker would ignore as well: %v", err)
	}
}

// TestBaseIsTheLastFrom: which FROM decides what the guest's filesystem
// is made of, since that is the one granule has a rule about and the
// earlier ones are build stages that may name anything.
func TestBaseIsTheLastFrom(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		want  string
		stage string
		line  int
		ok    bool
	}{{
		name: "no base of its own",
		src:  "RUN echo hi\n",
	}, {
		name: "one",
		src:  "FROM my-rootfs:v1\nRUN echo hi\n",
		want: "my-rootfs:v1",
		line: 1,
		ok:   true,
	}, {
		name:  "the last of several",
		src:   "FROM golang:1.23 AS builder\nRUN echo hi\nFROM my-rootfs:v1 AS guest\nCOPY --from=builder /out /out\n",
		want:  "my-rootfs:v1",
		stage: "guest",
		line:  3,
		ok:    true,
	}, {
		name: "with a platform flag on it",
		src:  "FROM --platform=linux/amd64 my-rootfs:v1\n",
		want: "my-rootfs:v1",
		line: 1,
		ok:   true,
	}, {
		// Lower case is the same instruction to docker, so it has to be
		// the same base here.
		name:  "in lower case",
		src:   "from my-rootfs:v1 as guest\n",
		want:  "my-rootfs:v1",
		stage: "guest",
		line:  1,
		ok:    true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			instrs, err := Validate("Dockerfile", tc.src)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			got, ok := Base(instrs)
			if ok != tc.ok {
				t.Fatalf("Base() ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Image != tc.want || got.Line != tc.line || got.Stage != tc.stage {
				t.Errorf("Base() = %+v, want image %q stage %q on line %d", got, tc.want, tc.stage, tc.line)
			}
		})
	}
}

func TestValidateEmpty(t *testing.T) {
	for _, src := range []string{"", "\n\n", "# nothing but a comment\n"} {
		if _, err := Validate("Dockerfile", src); err == nil || !strings.Contains(err.Error(), "no instructions") {
			t.Errorf("Validate(%q) = %v, want a complaint about an empty file", src, err)
		}
	}
}

func errorFrom(t *testing.T, src string) *Error {
	t.Helper()
	_, err := Validate("Dockerfile", src)
	var verr *Error
	if !errors.As(err, &verr) {
		t.Fatalf("Validate returned %T (%v), want *Error", err, err)
	}
	return verr
}
