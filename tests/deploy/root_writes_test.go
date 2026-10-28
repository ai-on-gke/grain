// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tests in this file are #2405: $GRAIN_DATA_DIR and everything under
// it belongs to the unprivileged account the daemon runs as, and
// setup.sh -- and the scripts it leaves behind for systemd -- run as
// root. Whatever root reads there is that account's to say, and every
// path root writes through there is one it could have pointed elsewhere.

// shellFunction is one function of setup.sh, whole, ready to be handed
// to bash.
func shellFunction(t *testing.T, name string) string {
	t.Helper()
	return body(t, setupText(t), name+"() {") + "\n}\n"
}

// runBash runs script under bash and returns what it printed, failing
// the test if it did not exit cleanly.
func runBash(t *testing.T, script string, env ...string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	return string(out)
}

// No root script sources a file that account can write. `. file` is the
// account running anything it likes as root, on the next start.
func TestNoRootScriptSourcesAFileTheDaemonCanWrite(t *testing.T) {
	sourced := regexp.MustCompile(`(?m)^\s*(\.|source)\s+"?(\\?\$\{?(IMAGE_REF_FILE|GRAIN_DATA_DIR|CONTROL_DIR)|/var/lib/grain/data)`)
	if m := sourced.FindString(setupCode(t)); m != "" {
		t.Errorf("setup.sh sources a file in the daemon's own directory as root: %q", strings.TrimSpace(m))
	}
	for _, fn := range []string{"install_cli_wrappers() {", "write_host_extractor() {", "write_image_ref_promoter() {"} {
		contains(t, body(t, setupText(t), fn), "$(declare -f image_ref_from)")
	}
}

// image_ref_from takes a ref file as data: the last GRAIN_IMAGE= line,
// only if it names an image in the deployment's own repository, and
// never anything the file says beyond that.
func TestAnImageRefFileIsReadAsDataNotRun(t *testing.T) {
	fn := shellFunction(t, "image_ref_from")
	dir := t.TempDir()
	pwned := filepath.Join(dir, "pwned")
	const repo = "ghcr.io/bwsalmon/grain"

	for _, c := range []struct {
		name, content, want string
	}{
		{"plain", "GRAIN_IMAGE=" + repo + ":main\n", repo + ":main"},
		{"pinned", "GRAIN_IMAGE=" + repo + ":main@sha256:0123abcd\n", repo + ":main@sha256:0123abcd"},
		{"last wins, quoted", "GRAIN_IMAGE=" + repo + ":old\n  GRAIN_IMAGE=\"" + repo + ":new\"\n", repo + ":new"},
		{"no trailing newline", "GRAIN_IMAGE=" + repo + ":main", repo + ":main"},
		{"a command on its own line", "touch " + pwned + "\nGRAIN_IMAGE=" + repo + ":main\n", repo + ":main"},
		{"a command after the ref", "GRAIN_IMAGE=" + repo + ":main; touch " + pwned + "\n", ""},
		{"a substitution", "GRAIN_IMAGE=" + repo + ":$(touch " + pwned + ")\n", ""},
		{"backticks", "GRAIN_IMAGE=" + repo + ":`touch " + pwned + "`\n", ""},
		{"docker flags", "GRAIN_IMAGE=" + repo + ":main --privileged -v /:/host\n", ""},
		{"another repository", "GRAIN_IMAGE=evil.example/grain:main\n", ""},
		{"a prefix of the repository", "GRAIN_IMAGE=" + repo + "-evil:main\n", ""},
		{"extra docker args are not an image", "GRAIN_EXTRA_DOCKER_RUN_ARGS=--privileged\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "image.env")
			if err := os.WriteFile(file, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(runBash(t, fn+`image_ref_from "$F" "$R"`, "F="+file, "R="+repo))
			if got != c.want {
				t.Errorf("image_ref_from = %q, want %q", got, c.want)
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Fatal("reading the ref file ran a command out of it")
			}
		})
	}

	// And not through a symlink the account left where the file should
	// be, whatever the file at the other end says.
	real := filepath.Join(dir, "elsewhere.env")
	if err := os.WriteFile(real, []byte("GRAIN_IMAGE="+repo+":main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "image.env")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runBash(t, fn+`image_ref_from "$F" "$R"`, "F="+link, "R="+repo)); got != "" {
		t.Errorf("image_ref_from followed a symlink to %q", got)
	}
}

// The container unit's pre-step is the only path from the file the
// daemon writes to the one systemd reads, and what crosses it is one
// GRAIN_IMAGE= line root wrote itself.
func TestThePromoterCopiesOnlyAnImageAcross(t *testing.T) {
	const repo = "ghcr.io/bwsalmon/grain"
	const running = "GRAIN_IMAGE=" + repo + ":running\n"

	for _, c := range []struct {
		name, request, want string
		symlink             bool
	}{
		{name: "an upgrade", request: "GRAIN_IMAGE=" + repo + ":next\n", want: "GRAIN_IMAGE=" + repo + ":next\n"},
		{name: "extra docker args ride along", request: "GRAIN_EXTRA_DOCKER_RUN_ARGS=--privileged -v /:/host\nGRAIN_IMAGE=" + repo + ":next\n",
			want: "GRAIN_IMAGE=" + repo + ":next\n"},
		{name: "an injected command", request: "GRAIN_IMAGE=" + repo + ":next; touch $PWNED\n", want: running},
		{name: "another repository", request: "GRAIN_IMAGE=evil.example/x:latest\n", want: running},
		{name: "a symlinked request", request: "GRAIN_IMAGE=" + repo + ":next\n", want: running, symlink: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pwned := filepath.Join(dir, "pwned")
			request := filepath.Join(dir, "data", "image.env")
			unitDir := filepath.Join(dir, "etc", "grain")
			unitFile := filepath.Join(unitDir, "image.env")
			promoter := filepath.Join(dir, "lib", "promote-image-ref.sh")
			if err := os.MkdirAll(filepath.Dir(request), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(unitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitFile, []byte(running), 0o644); err != nil {
				t.Fatal(err)
			}
			content := strings.ReplaceAll(c.request, "$PWNED", pwned)
			if c.symlink {
				real := filepath.Join(dir, "elsewhere.env")
				if err := os.WriteFile(real, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, request); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(request, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			runBash(t, shellFunction(t, "image_ref_from")+shellFunction(t, "write_image_ref_promoter")+
				"write_image_ref_promoter\n\"$IMAGE_REF_PROMOTER\"\n",
				"GRAIN_IMAGE="+repo, "GRAIN_USER=grain",
				"IMAGE_REF_FILE="+request, "UNIT_IMAGE_REF_DIR="+unitDir,
				"UNIT_IMAGE_REF_FILE="+unitFile, "IMAGE_REF_PROMOTER="+promoter)

			got, err := os.ReadFile(unitFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("the unit's ref file says %q, want %q", got, c.want)
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Error("the promoter ran a command out of the request")
			}
		})
	}

	// And the unit reads only the root-owned file, never the request.
	unit := between(t, setupText(t), "cat > /etc/systemd/system/grain-daemon.service", "\nUNIT\n")
	contains(t, unit, "EnvironmentFile=${UNIT_IMAGE_REF_FILE}")
	absent(t, unit, "EnvironmentFile=${IMAGE_REF_FILE}")
	contains(t, setupCode(t), "UNIT_IMAGE_REF_DIR=/etc/grain")
}

// Root's own writes into the data directory are made as the account,
// and through a rename: a symlink the account planted at the target is
// replaced, never written through, and one planted where a directory
// should be is refused.
func TestRootWritesIntoTheDataDirDoNotFollowSymlinks(t *testing.T) {
	code := setupCode(t)
	contains(t, body(t, code, "as_grain() {"), `setpriv --reuid="$GRAIN_USER" --regid="$GRAIN_GROUP" --clear-groups`)
	// Not a single chown of one path there is left: a recursive chown
	// does not follow symlinks (-P is its default), and anything else
	// is written by the account in the first place.
	for _, line := range strings.Split(code, "\n") {
		if strings.Contains(line, `chown "$GRAIN_USER:$GRAIN_GROUP"`) {
			t.Errorf("root chowns a path in the data directory, following any symlink there: %s", strings.TrimSpace(line))
		}
	}

	// The account is whoever runs this test: as_grain is the one thing
	// stubbed, and what is being shown is that nothing root-side touches
	// the path at all.
	lib := "as_grain() { \"$@\"; }\n" + shellFunction(t, "grain_write") + shellFunction(t, "grain_dir") + shellFunction(t, "seed_secret")

	dir := t.TempDir()
	// Empty, so seed_secret's "only if missing or empty" lets it through
	// to the write itself.
	victim := filepath.Join(dir, "shadow")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "gemini-api-key")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	runBash(t, lib+`seed_secret "$TARGET" "a new key"`, "TARGET="+target)
	if got, _ := os.ReadFile(victim); len(got) != 0 {
		t.Errorf("seed_secret wrote through a symlink: the file it pointed at now says %q", got)
	}
	if fi, err := os.Lstat(target); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the secret is not a file of its own: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "a new key\n" {
		t.Errorf("seed_secret wrote %q", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o600 {
		t.Errorf("the secret is mode %v, want 0600", fi.Mode().Perm())
	}

	// And the same for a file rewritten on every run, whatever is at the
	// other end of the link.
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte("root's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dir, "image.env")
	if err := os.Symlink(passwd, ref); err != nil {
		t.Fatal(err)
	}
	runBash(t, lib+`printf 'GRAIN_IMAGE=x\n' | grain_write 0644 "$TARGET"`, "TARGET="+ref)
	if got, _ := os.ReadFile(passwd); string(got) != "root's own\n" {
		t.Errorf("grain_write wrote through a symlink: the file it pointed at now says %q", got)
	}
	if fi, err := os.Lstat(ref); err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o644 {
		t.Errorf("grain_write left %v %v, want a 0644 file of its own", fi, err)
	}

	elsewhere := filepath.Join(dir, "etc")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "secrets")
	if err := os.Symlink(elsewhere, planted); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", lib+`grain_dir 0700 "$TARGET"`)
	cmd.Env = append(os.Environ(), "TARGET="+planted)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("grain_dir accepted a symlink: %s", out)
	}
	if fi, _ := os.Stat(elsewhere); fi.Mode().Perm() != 0o755 {
		t.Errorf("grain_dir changed the mode of a symlink's target to %v", fi.Mode().Perm())
	}
}

// Beside grain hostd /usr/local/bin/grain is the real binary, not the
// `docker run --user` wrapper, so run as root it would open the secrets
// database and run git in a state-repo the account controls. Every
// invocation goes through grain_cli, which runs it as the account there.
func TestTheCLIIsNeverRunAsRootBesideHostd(t *testing.T) {
	code := setupCode(t)
	cli := body(t, code, "grain_cli() {")
	contains(t, cli, `as_grain env -C / HOME="$GRAIN_DATA_DIR/home" /usr/local/bin/grain "$@"`)

	// At a command position: the start of a line (after any `if`/`!`),
	// a command substitution, or after a pipe or list operator.
	invoked := regexp.MustCompile(`(^\s*((if|then|!)\s+)*|\$\(|[|&;]\s*)"?(/usr/local/bin/grain|\$\{?HOST_GRAIN_BIN\}?)"?\s+[a-z-]`)
	for _, line := range strings.Split(code, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.Contains(cli, trimmed) {
			continue
		}
		if m := invoked.FindString(line); m != "" {
			t.Errorf("setup.sh runs the grain CLI without grain_cli, as root beside hostd: %s", trimmed)
		}
	}

	// And with the CLI writing as the account, nothing is left for root
	// to chown back after it.
	for _, fn := range []string{"configure_state_repo() {", "seed_gcp_minter_key() {", "mint_gemini_operating_key() {"} {
		absent(t, body(t, code, fn), "chown")
	}

	// Run for real, as whoever runs this test, with as_grain recording
	// what it was handed: beside hostd it goes through it; without hostd
	// the wrapper drops to the account itself.
	lib := `as_grain() { echo "as_grain $*"; }` + "\n" + cli + "\n}\n"
	if got := runBash(t, lib+`grain_cli secrets list`, "GRAIN_HOSTD=1", "GRAIN_DATA_DIR=/var/lib/grain/data"); !strings.Contains(got,
		"as_grain env -C / HOME=/var/lib/grain/data/home /usr/local/bin/grain secrets list") {
		t.Errorf("grain_cli beside hostd did not run as the account: %q", got)
	}
}

func TestThePromoterAcceptsKnownSuccessorRepositoriesAndRejectsUntrustedOnes(t *testing.T) {
	const legacyRepo = "ghcr.io/bwsalmon/grain/grain"
	const allowedPrimaryRepo = "registry.example.com/grain/enterprise-grain"
	const allowedLegacyRepo = "ghcr.io/example/grain/enterprise-grain"
	const customOSSRepo = "ghcr.io/grain-org/grain/grain"
	const running = "GRAIN_IMAGE=" + legacyRepo + ":v2.0.0\n"

	for _, c := range []struct {
		name, request, want string
	}{
		{
			name:    "shift from legacy repo to allowed primary repo",
			request: "GRAIN_IMAGE=" + allowedPrimaryRepo + ":v2.0.0\n",
			want:    "GRAIN_IMAGE=" + allowedPrimaryRepo + ":v2.0.0\n",
		},
		{
			name:    "shift from legacy repo to allowed legacy repo",
			request: "GRAIN_IMAGE=" + allowedLegacyRepo + ":v2.0.0\n",
			want:    "GRAIN_IMAGE=" + allowedLegacyRepo + ":v2.0.0\n",
		},
		{
			name:    "shift from legacy repo to configured OSS successor repo",
			request: "GRAIN_IMAGE=" + customOSSRepo + ":v2.0.0\n",
			want:    "GRAIN_IMAGE=" + customOSSRepo + ":v2.0.0\n",
		},
		{
			name:    "untrusted repository still rejected",
			request: "GRAIN_IMAGE=evil.example/grain:v2.0.0\n",
			want:    running,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			request := filepath.Join(dir, "data", "image.env")
			unitDir := filepath.Join(dir, "etc", "grain")
			unitFile := filepath.Join(unitDir, "image.env")
			promoter := filepath.Join(dir, "lib", "promote-image-ref.sh")
			if err := os.MkdirAll(filepath.Dir(request), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(unitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitFile, []byte(running), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(request, []byte(c.request), 0o644); err != nil {
				t.Fatal(err)
			}

			runBash(t, shellFunction(t, "image_ref_from")+shellFunction(t, "write_image_ref_promoter")+
				"write_image_ref_promoter\n\"$IMAGE_REF_PROMOTER\"\n",
				"GRAIN_IMAGE="+legacyRepo,
				"GRAIN_OSS_IMAGE_REPO="+customOSSRepo,
				"GRAIN_ALLOWED_IMAGE_REPOS="+allowedPrimaryRepo+","+allowedLegacyRepo,
				"GRAIN_USER=grain",
				"IMAGE_REF_FILE="+request, "UNIT_IMAGE_REF_DIR="+unitDir,
				"UNIT_IMAGE_REF_FILE="+unitFile, "IMAGE_REF_PROMOTER="+promoter)

			got, err := os.ReadFile(unitFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("the unit's ref file says %q, want %q", got, c.want)
			}
		})
	}
}
