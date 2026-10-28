package granulefile

import "testing"

// A file is a sequence of stages, and a stage's kind follows from how it
// started and from whether a converter has been seen in it (§4.1, §4.2).
// Everything else in this package hangs off getting that right.
func TestParseStages(t *testing.T) {
	const src = `ARG VERSION=1.2.3

FROM alpine:latest AS base
RUN apk add --no-cache redis

FROM base AS packed
COPY greeting.txt /greeting.txt
VM PACK --variant=alpine
RUN systemctl enable redis

VM FROM ghcr.io/example/vm:1
VM SET memory-mb=2048
`
	f, err := Parse("Granule", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Globals) != 1 || f.Globals[0].Name != "ARG" {
		t.Fatalf("globals = %+v, want the one ARG above the first stage", f.Globals)
	}
	if len(f.Stages) != 3 {
		t.Fatalf("parsed %d stages, want 3", len(f.Stages))
	}
	if !f.UsesVM() {
		t.Error("UsesVM() = false, and this file uses VM: it decides whether parser directives are refused")
	}

	base, packed, derived := f.Stages[0], f.Stages[1], f.Stages[2]
	if base.Name != "base" || base.Ref != "alpine:latest" || base.Kind() != Container {
		t.Errorf("stage 0 = %+v, want the container stage `base` on alpine:latest", base)
	}
	if base.Converter() != nil {
		t.Errorf("stage 0 has a converter (%v), and nothing in it converts", base.Converter())
	}

	if packed.Kind() != VM {
		t.Errorf("stage 1 kind = %s, want VM: it ends in a converter", packed.Kind())
	}
	if c := packed.Converter(); c == nil || c.Verb != "PACK" || c.Line != 8 {
		t.Errorf("stage 1 converter = %+v, want the VM PACK on line 8", c)
	}
	// The kind changes at the converter and not before it: what is above
	// it is a container build, and that is what decides which
	// instructions it may use (§7).
	for i, want := range []Kind{Container, VM, VM} {
		if got := packed.KindAt(i); got != want {
			t.Errorf("stage 1 instruction %d (%v) is in a %s stage, want %s", i, packed.Instructions[i], got, want)
		}
	}

	if !derived.StartsAsVM() || derived.Kind() != VM {
		t.Errorf("stage 2 = %+v, want a VM stage from its header", derived)
	}
	if derived.Label() != "stage 2" {
		t.Errorf("unnamed stage labelled %q, want its index", derived.Label())
	}

	// Lookup is by name, case-insensitively, or by index (§4.1).
	for _, ref := range []string{"packed", "PACKED", "1"} {
		if s, _, ok := f.Stage(ref); !ok || s != packed {
			t.Errorf("Stage(%q) did not find the second stage", ref)
		}
	}
	if _, _, ok := f.Stage("nowhere"); ok {
		t.Error("Stage found a stage that is not in the file")
	}

	// The target is the last stage, or the one named (§4.4).
	if s, kind, ok := f.Target(""); !ok || s != derived || kind != VM {
		t.Error("Target(\"\") is not the last stage, producing a machine")
	}
	if s, kind, ok := f.Target("base"); !ok || s != base || kind != Container {
		t.Error("Target(\"base\") is not the container stage called base")
	}
}

// TestParseStageHeaders is §4.2's table: five things start a stage, and
// which kind each one starts is the whole of what the rest of this
// package hangs off.
func TestParseStageHeaders(t *testing.T) {
	const src = `FROM alpine:latest AS built
VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked
COPY updated-config.yaml /etc/service/config.yaml
VM FROM ghcr.io/example/vm:1 AS inherited
VM PACK FROM built --variant=alpine AS packed
VM WRAP FROM alpine:latest --restart=no redis AS wrapped
`
	f, err := Parse("Granule", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Stages) != 5 {
		t.Fatalf("parsed %d stages, want 5: %+v", len(f.Stages), f.Stages)
	}
	for i, want := range []struct {
		name   string
		ref    string
		header string
		kind   Kind
	}{
		{"built", "alpine:latest", "FROM", Container},
		{"unpacked", "ghcr.io/example/my-vm:latest", "VM UNPACK FROM", Container},
		{"inherited", "ghcr.io/example/vm:1", "VM FROM", VM},
		{"packed", "built", "VM PACK FROM", VM},
		{"wrapped", "alpine:latest", "VM WRAP FROM", VM},
	} {
		s := f.Stages[i]
		if s.Name != want.name || s.Ref != want.ref || s.Header.String() != want.header || s.HeaderKind() != want.kind {
			t.Errorf("stage %d: %s %s AS %s, a %s stage; want %s %s AS %s, a %s stage",
				i, s.Header, s.Ref, s.Name, s.HeaderKind(), want.header, want.ref, want.name, want.kind)
		}
		if s.Converter() != nil {
			t.Errorf("stage %d has an in-stage converter, and a header converter is not one: %v", i, s.Converter())
		}
	}
	// The one instruction in the file belongs to the stage above it, not
	// to the header below it.
	if got := len(f.Stages[1].Instructions); got != 1 {
		t.Errorf("the VM UNPACK FROM stage has %d instructions, want the one COPY", got)
	}
}

// TestParseConverterNamesTheStageItMakes: a stage that starts with a name
// and converts with another carries both, and they mean different things
// (§4.1). The header's is the container filesystem before the pack; the
// converter's is the machine made of it.
func TestParseConverterNamesTheStageItMakes(t *testing.T) {
	f, err := Parse("Granule", "FROM alpine:latest AS container_stage\nRUN apk add --no-cache redis\nVM PACK AS vm_stage\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := f.Stages[0]
	if s.Name != "container_stage" || s.ConvertedName != "vm_stage" {
		t.Fatalf("stage names = %q and %q, want container_stage and vm_stage", s.Name, s.ConvertedName)
	}
	if s.Label() != "vm_stage" {
		t.Errorf("stage labelled %q, want the machine's name", s.Label())
	}
	for _, tc := range []struct {
		ref  string
		kind Kind
	}{
		{"container_stage", Container},
		{"CONTAINER_STAGE", Container},
		{"vm_stage", VM},
		// An index addresses the stage, so it means what the stage
		// produces.
		{"0", VM},
	} {
		got, kind, ok := f.Stage(tc.ref)
		if !ok || got != s {
			t.Errorf("Stage(%q) did not find the stage", tc.ref)
			continue
		}
		if kind != tc.kind {
			t.Errorf("--from=%s reads the %s half, want the %s one", tc.ref, kind, tc.kind)
		}
	}
	// A stage whose converter names nothing is one name for the whole
	// stage, and that name means what the stage produces.
	f, err = Parse("Granule", "FROM alpine:latest AS only\nVM PACK\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, kind, ok := f.Stage("only"); !ok || kind != VM {
		t.Errorf("--from=only reads the %s half; with no name on the converter the stage has one name and it is the machine's", kind)
	}
}

// A file with no VM instruction is a Dockerfile, and is one stage of
// container build (§1.2).
func TestParseDockerfileIsAGranule(t *testing.T) {
	f, err := Parse("Dockerfile", "FROM alpine:latest\nRUN apk add --no-cache redis\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.UsesVM() {
		t.Error("UsesVM() = true for a file with no VM instruction")
	}
	if len(f.Stages) != 1 || f.Stages[0].Kind() != Container {
		t.Fatalf("stages = %+v, want one container stage", f.Stages)
	}
	if err := Validate(f, Options{}); err != nil {
		t.Errorf("a Dockerfile is a Granule, and this one was refused: %v", err)
	}
}

func TestParseVolumes(t *testing.T) {
	const src = `FROM alpine:latest
VOLUME ["/var/log", "/var/lib/data"]
VOLUME /etc/config /mnt/storage
`
	f, err := Parse("Granulefile", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vols, err := f.Stages[0].Volumes()
	if err != nil {
		t.Fatalf("Stage.Volumes(): %v", err)
	}
	want := []string{"/var/log", "/var/lib/data", "/etc/config", "/mnt/storage"}
	if len(vols) != len(want) {
		t.Fatalf("Volumes() returned %v, want %v", vols, want)
	}
	for i := range want {
		if vols[i] != want[i] {
			t.Errorf("Volumes()[%d] = %q, want %q", i, vols[i], want[i])
		}
	}
}

func TestParseCaseInsensitiveDirectives(t *testing.T) {
	const src = `from alpine:latest as base
run apk add --no-cache redis
vm pack --variant=alpine as packed_vm
vm set memory-mb=1024 cpus=2
`
	f, err := Parse("Granule", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Stages) != 1 {
		t.Fatalf("parsed %d stages, want 1", len(f.Stages))
	}
	s := f.Stages[0]
	if s.Kind() != VM {
		t.Errorf("stage kind = %v, want VM", s.Kind())
	}
	if s.Name != "base" || s.ConvertedName != "packed_vm" {
		t.Errorf("stage names = %q, %q; want base, packed_vm", s.Name, s.ConvertedName)
	}
	if err := Validate(f, Options{Level: Level3}); err != nil {
		t.Errorf("Validate failed on lowercase directives: %v", err)
	}
}

func TestParseTargetResolution(t *testing.T) {
	const src = `FROM alpine:latest AS builder
RUN apk add --no-cache gcc

FROM alpine:latest AS container_stage
COPY --from=builder /usr/bin/gcc /usr/bin/gcc

FROM container_stage AS machine_stage
VM PACK AS final_vm
`
	f, err := Parse("Granule", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Default target (last stage)
	target, kind, ok := f.Target("")
	if !ok || target.Label() != "final_vm" || kind != VM {
		t.Errorf("Target(\"\") = (%v, %v, %v), want (final_vm, VM, true)", target, kind, ok)
	}

	// Target container stage
	target, kind, ok = f.Target("container_stage")
	if !ok || target.Name != "container_stage" || kind != Container {
		t.Errorf("Target(\"container_stage\") = (%v, %v, %v), want (container_stage, Container, true)", target, kind, ok)
	}

	// Target by numeric index
	target, kind, ok = f.Target("0")
	if !ok || target.Name != "builder" || kind != Container {
		t.Errorf("Target(\"0\") = (%v, %v, %v), want (builder, Container, true)", target, kind, ok)
	}
	target, kind, ok = f.Target("2")
	if !ok || target.Label() != "final_vm" || kind != VM {
		t.Errorf("Target(\"2\") = (%v, %v, %v), want (final_vm, VM, true)", target, kind, ok)
	}
}

func TestParseVolumeArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    string
		want    []string
		wantErr bool
	}{
		{"json array", `["/var/log", "/data"]`, []string{"/var/log", "/data"}, false},
		{"shell form multiple", `/var/log /data /tmp`, []string{"/var/log", "/data", "/tmp"}, false},
		{"shell form single", `/var/log`, []string{"/var/log"}, false},
		{"empty string", ``, nil, true},
		{"whitespace only", `   `, nil, true},
		{"empty json array", `[]`, nil, true},
		{"invalid json", `["/var/log"`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVolumeArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseVolumeArgs(%q) err = %v, wantErr = %v", tc.args, err, tc.wantErr)
			}
			if !tc.wantErr && len(got) != len(tc.want) {
				t.Errorf("ParseVolumeArgs(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
