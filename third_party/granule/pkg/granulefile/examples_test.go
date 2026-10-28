package granulefile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// examplesDir holds the specification's worked examples as files
// granule is asked to build: see test/granulefiles/README.md. This test
// is the half of that suite which needs nothing -- no docker, no
// registry, no /dev/kvm -- so every example is parsed, validated and
// compiled into the build granule would run on every `go test ./...`.
// test/granulefiles/run.sh is the other half: it runs those builds and
// boots what they produce.
const examplesDir = "../../test/granulefiles"

// TestSpecExamples compiles every example and holds it to what its own
// directory says about it: the substrings the compiled build must
// carry, or the refusal it must produce.
func TestSpecExamples(t *testing.T) {
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("reading the examples: %v", err)
	}

	var examples []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(examplesDir, e.Name(), "Granule")); err == nil {
			examples = append(examples, e.Name())
		}
	}
	// A suite that quietly found no examples would pass forever. The
	// count is the specification's dozen worked examples and the ones
	// the sections above §14 give in passing.
	if len(examples) < 12 {
		t.Fatalf("found %d examples in %s, want at least the specification's dozen", len(examples), examplesDir)
	}

	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(examplesDir, name)
			path := filepath.Join(dir, "Granule")
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}

			// Checking comes first and is held to a higher standard than
			// building: a file the specification defines must validate
			// even where this tree cannot build it yet (§10.3).
			f, err := Check(path, string(source), Options{Level: Level3})
			if err != nil {
				t.Fatalf("checking %s: %v", path, err)
			}

			plan, planErr := PlanBuild(context.Background(), f, BuildOptions{
				Context: dir,
				Tag:     "granule-example:test",
			})

			if want := readExpectation(t, dir, "expect-build-error"); len(want) > 0 {
				if planErr == nil {
					t.Fatalf("%s compiled, and this example names a construct that is not built yet: it must be refused, not dropped", path)
				}
				for _, w := range want {
					if !strings.Contains(planErr.Error(), w) {
						t.Errorf("the refusal does not say %q:\n%v", w, planErr)
					}
				}
				return
			}
			if planErr != nil {
				t.Fatalf("compiling %s: %v", path, planErr)
			}

			for _, w := range readExpectation(t, dir, "expect-dockerfile") {
				if !strings.Contains(plan.Dockerfile, w) {
					t.Errorf("the compiled build does not contain %q:\n%s", w, plan.Dockerfile)
				}
			}

			// What the build will do inside the booted guest, for the
			// examples that have one: the half of those files no
			// Dockerfile can carry, and the half `go test` can check
			// without a machine that has /dev/kvm.
			if want := readExpectation(t, dir, "expect-boot"); len(want) > 0 {
				if plan.Boot == nil {
					t.Fatalf("%s compiles to no boot phase, and this example runs instructions inside the guest", path)
				}
				summary := bootSummary(plan.Boot)
				for _, w := range want {
					if !strings.Contains(summary, w) {
						t.Errorf("the boot phase does not contain %q:\n%s", w, summary)
					}
				}
			} else if plan.Boot != nil {
				t.Errorf("%s compiles to a boot phase nothing in this example asked for:\n%s", path, bootSummary(plan.Boot))
			}

			for _, w := range readExpectation(t, dir, "expect-unit") {
				if !anyFileContains(plan.Files, w) {
					t.Errorf("no generated support file contains %q; the build generated %v", w, fileNames(plan.Files))
				}
			}

			// Every COPY the file makes out of the build context has to
			// find its source, which is the one thing a compiled build
			// cannot say for itself -- and the failure it produces
			// otherwise is docker's, halfway through a build, naming a
			// path the author did write.
			for _, src := range contextSources(plan.Dockerfile) {
				if _, err := os.Stat(filepath.Join(dir, src)); err != nil {
					t.Errorf("the build copies %q out of the context, and %s has no such file", src, dir)
				}
			}
		})
	}
}

// bootSummary renders a plan's guest phase as the lines an example's
// expect-boot file is matched against: what will be run inside the
// machine, in order, with the account, directory and environment §6
// says each instruction runs with.
func bootSummary(b *BootPhase) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "stage: %s\n", b.Stage)
	fmt.Fprintf(&sb, "snapshot: %t\n", b.Snapshot)
	for _, s := range b.Steps {
		fmt.Fprintf(&sb, "step %d user=%s dir=%s env=%s argv=%s\n",
			s.Line, s.User, s.Dir, strings.Join(s.Env, ","), strings.Join(s.Argv, " "))
	}
	return sb.String()
}

// readExpectation reads one of an example's expectation files: one
// wanted substring per line, blank lines and comments ignored.
func readExpectation(t *testing.T, dir, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var want []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want = append(want, line)
	}
	return want
}

func anyFileContains(files map[string][]byte, want string) bool {
	for _, content := range files {
		if strings.Contains(string(content), want) {
			return true
		}
	}
	return false
}

func fileNames(files map[string][]byte) []string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	return names
}

// contextSources returns the sources of the COPY instructions that read
// the build context -- the ones with no --from= -- excluding the
// support files granule generates into the context itself.
func contextSources(dockerfile string) []string {
	var sources []string
	for _, line := range strings.Split(dockerfile, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.ToUpper(fields[0]) != "COPY" {
			continue
		}
		args := fields[1:]
		flagged := false
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			if strings.HasPrefix(args[0], "--from=") {
				flagged = true
			}
			args = args[1:]
		}
		if flagged || len(args) < 2 {
			continue
		}
		for _, src := range args[:len(args)-1] {
			if strings.HasPrefix(src, "container_") || strings.HasPrefix(src, "granule-") {
				continue // generated into the context by the build itself
			}
			sources = append(sources, src)
		}
	}
	return sources
}
