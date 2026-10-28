package granulefile

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// want is one violation a file has to be refused with: where, what, the
// rule it cites, and phrases the reason has to carry. Those four are the
// whole contract of a refusal (§10.2) -- a validator that says
// "unsupported instruction" and stops is one the reader has to bisect
// their own file against.
type want struct {
	line        int
	instruction string
	rule        string
	phrases     []string
}

// TestValidateRefuses is §4.5's rule list and §6/§7's tables, stated as
// a test: one case per rule, each asserting what the file is refused
// with and where.
func TestValidateRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		opts Options
		want []want
	}{{
		name: "K1: VM without a verb",
		src:  "FROM alpine:latest\nVM\n",
		want: []want{{2, "VM", "K1", []string{"takes a verb", "FROM, WRAP, PACK, UNPACK, SET, SNAPSHOT"}}},
	}, {
		name: "K1: a verb nobody has",
		src:  "FROM alpine:latest\nVM FLY\n",
		want: []want{{2, "VM FLY", "K1", []string{`"FLY" is not a VM verb`, "closed set"}}},
	}, {
		name: "K2: a stage converts once",
		src:  "FROM alpine:latest\nVM PACK\nVM WRAP\n",
		want: []want{{3, "VM WRAP", "K2", []string{"already converted at line 2", "start a new stage"}}},
	}, {
		name: "K3: nothing to convert below VM FROM",
		src:  "VM FROM ghcr.io/example/vm:1\nVM PACK\n",
		want: []want{{2, "VM PACK", "K3", []string{"already a machine", "nothing to convert"}}},
	}, {
		// A header converter starts a machine too, so a converter under
		// one has nothing to convert either (§4.2's table).
		name: "K3: nor below a VM PACK FROM header",
		src:  "FROM alpine:latest AS rootfs\nRUN apk add --no-cache redis\nVM PACK FROM rootfs AS machine\nVM WRAP\n",
		want: []want{{4, "VM WRAP", "K3", []string{"started with VM PACK FROM", "already a machine"}}},
	}, {
		name: "K4: a converter before the first stage",
		src:  "VM PACK\nFROM alpine:latest\nRUN echo hi\n",
		want: []want{{1, "VM PACK", "K4", []string{"no stage above this one", "VM FROM"}}},
	}, {
		name: "K5: settings in a stage that never becomes a machine",
		src:  "FROM alpine:latest\nVM SET memory-mb=2048\n",
		want: []want{{2, "VM SET", "K5", []string{"never becomes a machine", "nothing would ever read"}}},
	}, {
		name: "K5: a snapshot of a machine that is never built",
		src:  "FROM alpine:latest\nVM SNAPSHOT\n",
		want: []want{{2, "VM SNAPSHOT", "K5", []string{"never becomes a machine", "no running VM to snapshot"}}},
	}, {
		name: "K6: there is nothing in a scratch image to wrap",
		src:  "FROM scratch\nCOPY app /app\nVM WRAP app\n",
		want: []want{{3, "VM WRAP", "K6", []string{"no filesystem", "VM PACK"}}},
	}, {
		name: "K6: nor in one a header converter names",
		src:  "VM WRAP FROM scratch app\nCOPY x /x\n",
		want: []want{{1, "VM WRAP FROM", "K6", []string{"FROM scratch", "no filesystem"}}},
	}, {
		// The same mistake written one line further away: the stage the
		// wrap names is itself empty.
		name: "K6: nor in an earlier stage that is one",
		src:  "FROM scratch AS empty\nCOPY app /app\nVM WRAP FROM empty app\nCOPY x /x\n",
		want: []want{{3, "VM WRAP FROM", "K6", []string{"empty, which is built FROM scratch on line 1"}}},
	}, {
		name: "K7: a stage cannot copy out of itself",
		src:  "FROM alpine:latest AS a\nCOPY --from=a /x /y\n",
		want: []want{{2, "COPY", "K7", []string{"copy out of itself"}}},
	}, {
		name: "K7: nor out of a stage below it",
		src:  "FROM alpine:latest AS a\nCOPY --from=b /x /y\nFROM alpine:latest AS b\nRUN echo hi\n",
		want: []want{{2, "COPY", "K7", []string{"declared below this one", "has not been built"}}},
	}, {
		name: "K7: nor out of a stage index the file does not have",
		src:  "FROM alpine:latest\nCOPY --from=3 /x /y\n",
		want: []want{{2, "COPY", "K7", []string{"names stage 3", "this file has 1"}}},
	}, {
		name: "K8: a parser directive in a file that uses VM",
		src:  "# syntax=docker/dockerfile:1\nFROM alpine:latest\nVM PACK\n",
		want: []want{{1, "# syntax=", "K8", []string{"inert", "drop the line"}}},
	}, {
		name: "K9: what a VM stage may not use (§6)",
		src:  "FROM alpine:latest\nVM PACK\nHEALTHCHECK CMD /app --check\nEXPOSE 8080\n",
		want: []want{
			{3, "HEALTHCHECK", "K9, §6", []string{"probe a container's main process", "vm status"}},
			{4, "EXPOSE", "K9, §6", []string{"port metadata"}},
		},
	}, {
		name: "K9: what a container stage above VM PACK may not use (§7)",
		src:  "FROM alpine:latest\nHEALTHCHECK CMD /app --check\nVM PACK\n",
		want: []want{{2, "HEALTHCHECK", "K9, §7", []string{"probe a container's main process", "what comes out of the stage is a disk"}}},
	}, {
		name: "K10: a file with no stage at all",
		src:  "RUN echo hi\n",
		want: []want{
			{1, "RUN", "§4.1", []string{"only ARG may appear before the first stage header"}},
			{1, "", "K10", []string{"no stage"}},
		},
	}, {
		name: "K10: a target stage nobody named and that does nothing",
		src:  "FROM alpine:latest AS base\nRUN echo hi\n\nFROM base\n",
		want: []want{{4, "FROM", "K10", []string{"copy of its own base under a new name"}}},
	}, {
		name: "§4.1: an instruction above the first stage header",
		src:  "COPY a /a\nFROM alpine:latest\nRUN echo hi\n",
		want: []want{{1, "COPY", "§4.1", []string{"only ARG may appear"}}},
	}, {
		name: "§4.1: two stages with one name",
		src:  "FROM alpine:latest AS base\nRUN echo one\nFROM alpine:latest AS BASE\nRUN echo two\n",
		want: []want{{3, "FROM", "§4.1", []string{"already declared on line 1", "case-insensitive"}}},
	}, {
		name: "§4.1: a stage named after a number it could be indexed by",
		src:  "FROM alpine:latest AS 2\nRUN echo hi\n",
		want: []want{{1, "FROM", "§4.1", []string{"is a number", "--from=2"}}},
	}, {
		name: "§4.4: a target the file does not have",
		src:  "FROM alpine:latest\nRUN echo hi\n",
		opts: Options{Target: "nowhere"},
		want: []want{{1, "", "§4.4", []string{`no stage called "nowhere"`}}},
	}, {
		name: "§5.1: there is no empty machine",
		src:  "VM FROM scratch\nRUN echo hi\n",
		want: []want{{1, "VM FROM", "§5.1", []string{"no empty machine", "VM PACK"}}},
	}, {
		name: "§5.2: an option VM WRAP does not have",
		src:  "FROM alpine:latest\nVM WRAP --wrapper=podman\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"--wrapper is not an option", "--runtime="}}},
	}, {
		name: "§5.2: an option given twice",
		src:  "FROM alpine:latest\nVM WRAP --restart=always --restart=no\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"--restart is given twice"}}},
	}, {
		name: "§5.2: a restart policy nothing implements",
		src:  "FROM alpine:latest\nVM WRAP --restart=sometimes\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"is not one of always, on-failure, no"}}},
	}, {
		name: "§5.2: an option written docker's other way",
		src:  "FROM alpine:latest\nVM WRAP --restart\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"--restart takes a value", "--restart=value"}}},
	}, {
		name: "§5.2: the service name given twice",
		src:  "FROM alpine:latest\nVM WRAP --name=redis cache\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"given twice", "Give it once"}}},
	}, {
		name: "§5.2: a service name a unit cannot carry",
		src:  "FROM alpine:latest\nVM WRAP My_Service\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"does not match", "granule-wrap@"}}},
	}, {
		name: "§5.2: an option after the positional it belongs before",
		src:  "FROM alpine:latest\nVM WRAP redis --restart=no\n",
		want: []want{{2, "VM WRAP", "§5.2", []string{"comes after a positional argument", "COPY --from="}}},
	}, {
		name: "§5.3: a variant nothing packs",
		src:  "FROM alpine:latest\nVM PACK --variant=freebsd\n",
		want: []want{{2, "VM PACK", "§5.3", []string{"is not one of debian, alpine, auto"}}},
	}, {
		name: "§5.3: headroom that is not a number",
		src:  "FROM alpine:latest\nVM PACK --disk-extra-mb=lots\n",
		want: []want{{2, "VM PACK", "§5.3", []string{"is not a whole number"}}},
	}, {
		name: "§5.3: VM PACK packs the stage above it and takes nothing else",
		src:  "FROM alpine:latest\nVM PACK rootfs\n",
		want: []want{{2, "VM PACK", "§5.3", []string{"takes no positional arguments"}}},
	}, {
		name: "§5.5: a setting nothing reads",
		src:  "FROM alpine:latest\nVM PACK\nVM SET memory=2048\n",
		want: []want{{3, "VM SET", "§5.5", []string{`"memory" is not a setting`, "closed set"}}},
	}, {
		name: "§5.5: a value that would fail at boot",
		src:  "FROM alpine:latest\nVM PACK\nVM SET memory-mb=lots\n",
		want: []want{{3, "VM SET", "§5.5", []string{"is not a positive whole number", "CHV_MEMORY_MB"}}},
	}, {
		name: "§5.5: a timeout with no unit",
		src:  "FROM alpine:latest\nVM PACK\nVM SET shutdown-timeout=30\n",
		want: []want{{3, "VM SET", "§5.5", []string{"is not a duration", "30s"}}},
	}, {
		name: "§5.5: a setting that is not a pair",
		src:  "FROM alpine:latest\nVM PACK\nVM SET memory-mb\n",
		want: []want{{3, "VM SET", "§5.5", []string{"is not a setting", "key=value"}}},
	}, {
		name: "§5.5: VM SET with nothing to set",
		src:  "FROM alpine:latest\nVM PACK\nVM SET\n",
		want: []want{{3, "VM SET", "§5.5", []string{"at least one key=value"}}},
	}, {
		name: "§5.5: the same key twice on one line",
		src:  "FROM alpine:latest\nVM PACK\nVM SET memory-mb=1024 memory-mb=2048\n",
		want: []want{{3, "VM SET", "§5.5", []string{"set twice on this line"}}},
	}, {
		name: "§5.6: VM SNAPSHOT says one thing and takes no arguments",
		src:  "FROM alpine:latest\nVM PACK\nVM SNAPSHOT --at=end\n",
		want: []want{{3, "VM SNAPSHOT", "§5.6", []string{"takes no arguments", "VM SET snapshot=on"}}},
	}, {
		name: "K11: there is no machine in scratch to unpack",
		src:  "VM UNPACK FROM scratch\nCOPY x /x\n",
		want: []want{{1, "VM UNPACK FROM", "K11", []string{"scratch is not a machine", "no disk to extract"}}},
	}, {
		// VM UNPACK is a stage header and nothing else, so an UNPACK
		// with no FROM is not a form the language has.
		name: "K11: VM UNPACK is always VM UNPACK FROM",
		src:  "FROM alpine:latest\nVM UNPACK ghcr.io/example/my-vm:latest\n",
		want: []want{{2, "VM UNPACK", "K11", []string{"always written VM UNPACK FROM", "starts a container stage"}}},
	}, {
		// A header names an image or an *earlier* stage, which is what
		// keeps the stage graph acyclic without walking it.
		name: "K7: a stage cannot start from itself",
		src:  "FROM myself AS myself\nRUN echo hi\n",
		want: []want{{1, "FROM", "K7", []string{"its own name", "cannot be built from itself"}}},
	}, {
		name: "K7: nor from a stage declared below it",
		src:  "FROM later AS first\nRUN echo hi\nFROM alpine:latest AS later\nRUN echo hi\n",
		want: []want{{1, "FROM", "K7", []string{"declared below this one", "has not been built"}}},
	}, {
		// Where the reference names a stage in this file, its kind is
		// known, so pointing a machine instruction at a container stage
		// is a mistake nothing has to pull an image to find.
		name: "§5.1: VM FROM of a container stage in the same file",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs\nRUN apt-get update\nVM FROM rootfs\nCOPY x /x\n",
		want: []want{{3, "VM FROM", "§5.1", []string{"container stage rather than one", "VM PACK FROM rootfs"}}},
	}, {
		name: "K11: VM UNPACK FROM of a container stage in the same file",
		src:  "FROM alpine:latest AS built\nRUN apk add --no-cache redis\nVM UNPACK FROM built\nCOPY x /x\n",
		want: []want{{3, "VM UNPACK FROM", "K11", []string{"is a container stage", "an ordinary FROM built"}}},
	}, {
		name: "§5.3: VM PACK FROM of a stage that is already a machine",
		src:  "VM FROM ghcr.io/example/vm:1 AS machine\nCOPY x /x\nVM PACK FROM machine\nCOPY y /y\n",
		want: []want{{3, "VM PACK FROM", "§5.3", []string{"is already a machine", "VM FROM machine derives from a machine"}}},
	}, {
		name: "§5.3: there is no rootfs in scratch to pack",
		src:  "VM PACK FROM scratch\nRUN systemctl enable nginx\n",
		want: []want{{1, "VM PACK FROM", "§5.3", []string{"guest rootfs", "granule/debian.rootfs"}}},
	}, {
		// The FROM of a header converter comes directly after the verb.
		// Anywhere else in the line it is an argument, and there is no
		// form that takes one.
		name: "§5.3: a FROM that is not where a header converter's is",
		src:  "FROM alpine:latest\nVM PACK --variant=alpine FROM other\n",
		want: []want{{2, "VM PACK", "§5.3", []string{"comes directly after the verb", "stage-header form"}}},
	}, {
		name: "§4.1: a converter's AS name colliding with a stage's",
		src:  "FROM alpine:latest AS app\nRUN apk add --no-cache redis\nVM PACK AS APP\n",
		want: []want{{3, "VM PACK", "§4.1", []string{`"APP" was already declared on line 1`}}},
	}, {
		name: "§4.1: a converter's AS with no name after it",
		src:  "FROM alpine:latest\nRUN apk add --no-cache redis\nVM PACK AS\n",
		want: []want{{3, "VM PACK", "§4.1", []string{"AS names the VM stage this converter produces"}}},
	}, {
		name: "an instruction no builder has",
		src:  "FROM alpine:latest\nINSTALL nginx\n",
		want: []want{{2, "INSTALL", "", []string{"is not an instruction", "a gap in granule"}}},
	}, {
		name: "VOLUME with relative path",
		src:  "FROM alpine:latest\nVOLUME var/log\n",
		want: []want{{2, "VOLUME", "§6", []string{"not an absolute path", "must start with '/'"}}},
	}, {
		name: "§5.5: invalid disk-mode setting",
		src:  "FROM alpine:latest\nVM PACK\nVM SET disk-mode=turbo\n",
		want: []want{{3, "VM SET", "§5.5", []string{"overlay, persistent or readonly"}}},
	}, {
		name: "§5.5: invalid extent-size-mb setting",
		src:  "FROM alpine:latest\nVM PACK\nVM SET extent-size-mb=-10\n",
		want: []want{{3, "VM SET", "§5.5", []string{"positive whole number of MiB"}}},
	}, {
		name: "§5.5: zero vCPUs in VM SET",
		src:  "FROM alpine:latest\nVM PACK\nVM SET cpus=0\n",
		want: []want{{3, "VM SET", "§5.5", []string{"positive whole number of vCPUs"}}},
	}, {
		name: "§5.3: unknown option in VM PACK",
		src:  "FROM alpine:latest\nVM PACK --foo=bar\n",
		want: []want{{2, "VM PACK", "§5.3", []string{"--foo is not an option"}}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := violations(t, tc.src, tc.opts)
			if len(got) != len(tc.want) {
				t.Fatalf("refused %d things, want %d:\n%s", len(got), len(tc.want), report(got))
			}
			for i, w := range tc.want {
				v := got[i]
				if v.Line != w.line || v.Instruction != w.instruction {
					t.Errorf("violation %d: %s at line %d, want %s at line %d", i, v.Instruction, v.Line, w.instruction, w.line)
				}
				if v.Rule != w.rule {
					t.Errorf("violation %d cites %q, want %q", i, v.Rule, w.rule)
				}
				for _, phrase := range w.phrases {
					if !strings.Contains(v.Reason, phrase) {
						t.Errorf("violation %d does not carry %q:\n%s", i, phrase, v.Reason)
					}
				}
			}
		})
	}
}

// TestValidateAccepts is the specification's own worked examples (§14),
// which are the shapes this language exists for: if one of them is
// refused, the rules are wrong rather than the file.
func TestValidateAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		opts Options
	}{{
		name: "§14.1 a Dockerfile is a Granule",
		src:  "FROM alpine:latest\n\n# This runs in a container and I get a container.\nRUN apk add --no-cache redis\n",
	}, {
		name: "§14.2 wrapping it in a machine",
		src:  "FROM alpine:latest\nRUN apk add --no-cache redis\nCMD [\"redis-server\", \"--protected-mode\", \"no\"]\nVM WRAP redis AS redis_vm\n",
	}, {
		name: "§14.3 packing a rootfs into a machine",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nCOPY greeting.txt /greeting.txt\nRUN apt-get update && apt-get install -y nginx\nVOLUME /var/log/nginx\nVM PACK AS web_vm\n",
	}, {
		name: "§14.4 deriving a machine from a machine",
		src:  "VM FROM ghcr.io/example/github-runner-vm:latest\nCOPY runner.service /etc/systemd/system/runner.service\nSHELL [\"/bin/bash\", \"-o\", \"pipefail\", \"-c\"]\nRUN systemctl enable runner.service\n",
	}, {
		name: "§14.5 updating a VM image offline without KVM",
		src:  "VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked\nCOPY updated-config.yaml /etc/service/config.yaml\nVM PACK AS updated_vm\n",
	}, {
		name: "§14.6 packing directly from an earlier stage",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12 AS rootfs_builder\nRUN apt-get update && apt-get install -y redis-server\nVM PACK FROM rootfs_builder AS redis_vm\nUSER redis\nRUN redis-server --test-config\nUSER root\nRUN systemctl enable redis-server\nVOLUME /var/lib/redis\nVM SET memory-mb=2048 cpus=2\n",
	}, {
		name: "§14.7 composing machines",
		src:  "VM FROM ghcr.io/example/github-runner-vm:latest AS github_vm\n\nVM FROM ghcr.io/gke-labs/granule-alpine-vm:latest\nCOPY --from=github_vm /etc/runner/config.json /etc/runner/config.json\nVM SET memory-max-mb=4096\n",
	}, {
		name: "§14.8 snapshotting a warmed VM for instant resume",
		src:  "VM FROM ghcr.io/example/python-ml-vm:latest AS warmed_vm\nRUN python3 -c \"import model; model.preload_weights()\"\nVM SNAPSHOT\nVM SET memory-mb=4096\n",
	}, {
		// The same thing said as a setting (§5.6), which has to be as
		// acceptable as the instruction it is equivalent to.
		name: "a snapshot asked for with VM SET",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nRUN apt-get update\nVM PACK\nVM SET snapshot=on memory-mb=4096\n",
	}, {
		name: "§14.9 a native entrypoint and command in a packed stage",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nRUN apt-get update && apt-get install -y python3\nCOPY app.py /app/app.py\nWORKDIR /app\nENTRYPOINT [\"python3\", \"app.py\"]\nCMD [\"--port\", \"8080\"]\nVM PACK AS app_vm\n",
	}, {
		// §6 gives a VM stage a SHELL, a VOLUME, an ENTRYPOINT and a
		// CMD, so none of them is a refusal in one -- and §7 gives the
		// same four to a rootfs above a pack, which is the stage that
		// builds the machine those four describe.
		name: "the instructions §6 and §7 give both kinds of stage",
		src:  "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nVOLUME /var/lib/data\nSHELL [\"/bin/bash\", \"-c\"]\nENTRYPOINT [\"/usr/bin/service\"]\nVM PACK\nSHELL [\"/bin/bash\", \"-c\"]\nVOLUME /var/log/service\nUSER app\nENTRYPOINT [\"/usr/bin/service\"]\nCMD [\"--port\", \"8080\"]\n",
	}, {
		// A stage above VM WRAP is building a container image, and an
		// ENTRYPOINT in it is the workload's. This is what makes "take
		// this Dockerfile and add one line" work (§7).
		name: "a container stage above VM WRAP may use all of Dockerfile",
		src:  "FROM alpine:latest\nENTRYPOINT [\"/usr/bin/app\"]\nEXPOSE 8080\nHEALTHCHECK CMD /usr/bin/app --check\nVM WRAP\n",
	}, {
		// A directive is a directive to docker in a file with no VM, and
		// granule passes it through (§3.3).
		name: "a parser directive in a file with no VM instruction",
		src:  "# syntax=docker/dockerfile:1\nFROM alpine:latest\nRUN apk add --no-cache redis\n",
	}, {
		// A value with a space in it is one setting, not two.
		name: "a quoted setting value",
		src:  "FROM alpine:latest\nVM PACK\nVM SET kernel-cmdline=\"console=ttyS0 quiet\" memory-hotplug=on\n",
	}, {
		// A stage that exists only to be copied out of may be empty, and
		// so may a target somebody named: both were asked for by name
		// (K10).
		name: "an empty stage that was asked for by name",
		src:  "FROM alpine:latest AS empty\nFROM alpine:latest\nCOPY --from=empty /x /y\n",
	}, {
		name: "an empty target the caller named",
		src:  "FROM alpine:latest AS base\nRUN echo hi\nFROM base AS shipped\n",
		opts: Options{Target: "shipped"},
	}, {
		name: "a VOLUME declaration in a container stage packed into a machine",
		src:  "FROM alpine:latest\nVOLUME /var/log /var/lib/data\nVM PACK\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := violations(t, tc.src, tc.opts); len(got) > 0 {
				t.Errorf("refused a file the specification accepts:\n%s", report(got))
			}
		})
	}
}

// TestValidateReportsEveryViolation: an author porting a container
// Dockerfile trips several of these at once, and one refusal per build
// turns one edit into five.
func TestValidateReportsEveryViolation(t *testing.T) {
	src := `FROM alpine:latest AS app
RUN apk add --no-cache nginx
VM PACK
STOPSIGNAL SIGQUIT
EXPOSE 80
VM SET memory=2048
VM WRAP
`
	got := violations(t, src, Options{})
	want := []want{
		{4, "STOPSIGNAL", "K9, §6", nil},
		{5, "EXPOSE", "K9, §6", nil},
		{6, "VM SET", "§5.5", nil},
		{7, "VM WRAP", "K2", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("refused %d things, want %d:\n%s", len(got), len(want), report(got))
	}
	for i, w := range want {
		if got[i].Line != w.line || got[i].Instruction != w.instruction || got[i].Rule != w.rule {
			t.Errorf("violation %d: %s at line %d (%s), want %s at line %d (%s)", i, got[i].Instruction, got[i].Line, got[i].Rule, w.instruction, w.line, w.rule)
		}
	}
}

// TestErrorMessage: a diagnostic names the file, the line in the
// author's own file and the instruction as the file spells it, so it can
// be searched for, and cites the rule so it can be looked up (§10.2).
func TestErrorMessage(t *testing.T) {
	_, err := Check("Granule", "FROM alpine:latest\nVM PACK\nhealthcheck CMD /app --check\n", Options{})
	var verr *Error
	if !errors.As(err, &verr) {
		t.Fatalf("Check returned %T (%v), want *Error", err, err)
	}
	msg := err.Error()
	for _, phrase := range []string{
		"Granule: 1 problem",
		"Granule:3: HEALTHCHECK: docker health checks probe",
		"(K9, §6)",
		"\nWhat a granule VM stage may use: RUN, COPY, ADD, ENV, WORKDIR, USER, SHELL, VOLUME, ENTRYPOINT, CMD, ARG, LABEL, VM SET, VM SNAPSHOT.",
		"docs/granulefile.md",
	} {
		if !strings.Contains(msg, phrase) {
			t.Errorf("message does not carry %q:\n%s", phrase, msg)
		}
	}
	if verr.Unimplemented() {
		t.Error("a refused HEALTHCHECK reads as unimplemented, and it is forbidden: the two are different answers (§10.3)")
	}
}

// TestLevels: a construct from a level an implementation does not build
// is refused as unimplemented, naming the level, and reads differently
// from one the language forbids (§2.3, §10.3). Validation itself builds
// nothing, so its default is the whole language.
func TestLevels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     string
		level   Level
		line    int
		instr   string
		phrases []string
	}{{
		name:    "a VM stage at level 2",
		src:     "VM FROM ghcr.io/example/vm:1\nCOPY x /x\n",
		level:   Level2,
		line:    1,
		instr:   "VM FROM",
		phrases: []string{"unimplemented", "level 3 (VM stages)", "level 2 (converters and offline extraction)", "gap rather than a rule"},
	}, {
		name:    "a RUN in a VM stage at level 2",
		src:     "FROM alpine:latest\nVM PACK\nRUN systemctl enable nginx\n",
		level:   Level2,
		line:    3,
		instr:   "RUN",
		phrases: []string{"unimplemented", "booted guest", "/dev/kvm"},
	}, {
		name:    "a converter at level 1",
		src:     "FROM alpine:latest\nRUN apk add --no-cache redis\nVM PACK\n",
		level:   Level1,
		line:    3,
		instr:   "VM PACK",
		phrases: []string{"unimplemented", "level 2 (converters and offline extraction)", "level 1 (Dockerfile)"},
	}, {
		name:    "a copy out of a VM stage at level 2",
		src:     "VM FROM ghcr.io/example/vm:1 AS m\nFROM alpine:latest\nCOPY --from=m /etc/x /etc/x\n",
		level:   Level2,
		line:    3,
		instr:   "COPY",
		phrases: []string{"unimplemented", "guest root filesystem"},
	}, {
		// A copy out of the container half of a two-name stage is an
		// ordinary container copy, and only the machine's name makes it a
		// level 3 one.
		name:    "a copy out of the machine half of a two-name stage at level 2",
		src:     "FROM alpine:latest AS rootfs\nRUN apk add --no-cache redis\nVM PACK AS machine\nFROM alpine:latest\nCOPY --from=rootfs /etc/x /etc/x\nCOPY --from=machine /etc/y /etc/y\n",
		level:   Level2,
		line:    6,
		instr:   "COPY",
		phrases: []string{"unimplemented", "guest root filesystem"},
	}, {
		name:    "offline extraction at level 1",
		src:     "VM UNPACK FROM ghcr.io/example/my-vm:latest\nCOPY x /x\n",
		level:   Level1,
		line:    1,
		instr:   "VM UNPACK FROM",
		phrases: []string{"unimplemented", "level 2 (converters and offline extraction)", "back into a container stage"},
	}, {
		name:    "a header converter at level 1",
		src:     "FROM alpine:latest AS rootfs\nRUN apk add --no-cache redis\nVM PACK FROM rootfs AS machine\nCOPY x /x\n",
		level:   Level1,
		line:    3,
		instr:   "VM PACK FROM",
		phrases: []string{"unimplemented", "starting a VM stage by converting"},
	}, {
		name:    "a snapshot at level 2",
		src:     "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nRUN apt-get update\nVM PACK\nVM SNAPSHOT\n",
		level:   Level2,
		line:    4,
		instr:   "VM SNAPSHOT",
		phrases: []string{"unimplemented", "level 3 (VM stages)", "memory and device state"},
	}, {
		// The setting form costs what the instruction costs, so it is the
		// same answer at the same level (§5.6).
		name:    "a snapshot asked for as a setting at level 2",
		src:     "FROM ghcr.io/gke-labs/granule-guest-rootfs:debian12\nRUN apt-get update\nVM PACK\nVM SET snapshot=on\n",
		level:   Level2,
		line:    4,
		instr:   "VM SET",
		phrases: []string{"unimplemented", "level 3 (VM stages)", "VM SNAPSHOT written as a setting"},
	}, {
		name:    "a native workload command at level 2",
		src:     "VM FROM ghcr.io/example/vm:1\nENTRYPOINT [\"/usr/bin/service\"]\n",
		level:   Level2,
		line:    2,
		instr:   "ENTRYPOINT",
		phrases: []string{"unimplemented", "workload the guest boots into", "without a unit file"},
	}, {
		name:    "an in-guest mount point at level 2",
		src:     "VM FROM ghcr.io/example/vm:1\nVOLUME /var/lib/data\n",
		level:   Level2,
		line:    2,
		instr:   "VOLUME",
		phrases: []string{"unimplemented", "in-guest mount point"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			// At level 3 -- the default, and what a --check that builds
			// nothing is -- the file is fine.
			if got := violations(t, tc.src, Options{}); len(got) > 0 {
				t.Fatalf("refused at the full level, where nothing is unimplemented:\n%s", report(got))
			}

			got := violations(t, tc.src, Options{Level: tc.level})
			var found *Violation
			for i := range got {
				if got[i].Line == tc.line && got[i].Instruction == tc.instr {
					found = &got[i]
				}
			}
			if found == nil {
				t.Fatalf("nothing refused at %s for %s on line %d:\n%s", tc.level, tc.instr, tc.line, report(got))
			}
			if !found.Unimplemented() {
				t.Errorf("%s reads as forbidden, and it is unimplemented (§10.3): %s", tc.instr, found.Reason)
			}
			for _, phrase := range tc.phrases {
				if !strings.Contains(found.Reason, phrase) {
					t.Errorf("reason does not carry %q:\n%s", phrase, found.Reason)
				}
			}
		})
	}
}

// Every setting §5.5 documents records a runtime variable that exists,
// so that nothing has to read a Granule to boot the result.
func TestSettingEnv(t *testing.T) {
	for _, key := range settingKeys() {
		env, ok := SettingEnv(key)
		if !ok || !strings.HasPrefix(env, "CHV_") {
			t.Errorf("%s records %q, want a CHV_ runtime variable", key, env)
		}
	}
	if _, ok := SettingEnv("memory"); ok {
		t.Error("SettingEnv answered for a key that is not a setting")
	}
}

func violations(t *testing.T, src string, opts Options) []Violation {
	t.Helper()
	f, err := Parse("Granule", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = Validate(f, opts)
	if err == nil {
		return nil
	}
	var verr *Error
	if !errors.As(err, &verr) {
		t.Fatalf("Validate returned %T (%v), want *Error", err, err)
	}
	return verr.Violations
}

func report(vs []Violation) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString("  line ")
		b.WriteString(strings.Join([]string{strconv.Itoa(v.Line), v.Instruction, v.Rule, v.Reason}, " | "))
		b.WriteString("\n")
	}
	return b.String()
}
