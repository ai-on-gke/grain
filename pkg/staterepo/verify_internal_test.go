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

package staterepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReviews answers MergedReview from a map.
type fakeReviews map[string]*ForgeReview

func (f fakeReviews) MergedReview(_ context.Context, sha string) (*ForgeReview, error) {
	return f[sha], nil
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// sshKey makes an ed25519 key and returns its private key path and its
// allowed_signers line.
func sshKey(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	key := filepath.Join(dir, name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return key, name + "@example.com " + strings.TrimSpace(string(pub))
}

// commitAndPush commits a file change in work, signed with key when key
// is not empty, pushes it, and returns the commit.
func commitAndPush(t *testing.T, work, file, content, key string, extra ...string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "--all", ".")
	args := []string{"-c", "user.email=writer@example.com", "-c", "user.name=writer"}
	if key != "" {
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+key)
	}
	args = append(args, "commit", "--quiet", "-m", "change "+file)
	if key != "" {
		args = append(args, "-S")
	}
	runGit(t, work, args...)
	runGit(t, work, append([]string{"push", "--quiet"}, append(extra, "origin", "HEAD:main")...)...)
	return runGit(t, work, "rev-parse", "HEAD")
}

func head(t *testing.T, r *Repo) string {
	t.Helper()
	h, err := r.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestRequireVerifiedAppliesOnlyCommitsGrainCanVouchFor pins task 3973:
// in require_verified mode the working tree an external config
// repository is imported from moves only across commits that are signed
// by an allowed key, are the merge of a pull request approved on the
// forge or merged by grain, or that an operator accepted; the first one
// that is none of those is held, by name, with everything after it.
func TestRequireVerifiedAppliesOnlyCommitsGrainCanVouchFor(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	runGit(t, tmp, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(tmp, "work")
	runGit(t, tmp, "clone", "--quiet", remote, work)
	runGit(t, work, "checkout", "--quiet", "-b", "main")
	c0 := commitAndPush(t, work, "README.md", "base\n", "")

	allowedKey, allowedLine := sshKey(t, tmp, "allowed")
	otherKey, _ := sshKey(t, tmp, "other")
	signers := filepath.Join(tmp, "allowed_signers")
	if err := os.WriteFile(signers, []byte(allowedLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Open(ctx, Config{Dir: filepath.Join(tmp, "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	reviews := fakeReviews{}
	v := &Verifier{AllowedSignersFile: signers, Reviews: reviews, RequiredApprovals: 2, Mergers: []string{"grain-bot[bot]"}}
	r.SetVerifier(v)

	// Nothing applied from the repository yet, and its tip is unsigned:
	// held, and nothing to import.
	if _, err := r.PullExternal(ctx); !errors.Is(err, ErrHeld) {
		t.Fatalf("PullExternal of an unverified first commit = %v, want ErrHeld", err)
	}
	if h := r.Held(); h == nil || h.SHA != c0 || !strings.Contains(h.Reason, "not signed by an allowed key") {
		t.Fatalf("Held = %+v, want %s held as unsigned", h, c0)
	}
	// An operator accepts it by name.
	v.Accepted = []string{c0}
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatalf("PullExternal of an accepted commit: %v", err)
	}
	if head(t, r) != c0 || r.Held() != nil {
		t.Fatalf("HEAD = %s, held %+v; want %s applied and nothing held", head(t, r), r.Held(), c0)
	}
	if err := r.setLoadedHead(ctx, c0); err != nil {
		t.Fatal(err)
	}

	// A direct push nobody reviewed, then a signed commit on top of it:
	// the unreviewed one holds both.
	c1 := commitAndPush(t, work, "tables_a.json", "[]\n", "")
	c2 := commitAndPush(t, work, "tables_b.json", "[]\n", allowedKey)
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatalf("PullExternal: %v", err)
	}
	if h := r.Held(); head(t, r) != c0 || h == nil || h.SHA != c1 || !strings.Contains(h.Reason, "not the merge of a pull request") {
		t.Fatalf("HEAD = %s, held %+v; want %s kept and %s held", head(t, r), h, c0, c1)
	}

	// One approval of the two required is still not enough.
	reviews[c1] = &ForgeReview{Number: 7, Author: "writer", MergedBy: "writer", Approvers: []string{"reviewer"}}
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if h := r.Held(); h == nil || h.SHA != c1 || !strings.Contains(h.Reason, "#7 has 1 of the 2 approvals") {
		t.Fatalf("held %+v, want %s held for want of approvals", h, c1)
	}
	// Two are, and the signed commit after it verifies too.
	reviews[c1].Approvers = []string{"reviewer", "second"}
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if head(t, r) != c2 || r.Held() != nil {
		t.Fatalf("HEAD = %s, held %+v; want %s applied", head(t, r), r.Held(), c2)
	}
	if err := r.setLoadedHead(ctx, c2); err != nil {
		t.Fatal(err)
	}

	// Signed, but by a key nobody allowed; then merged by grain itself.
	c3 := commitAndPush(t, work, "tables_c.json", "[]\n", otherKey)
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if h := r.Held(); head(t, r) != c2 || h == nil || h.SHA != c3 {
		t.Fatalf("HEAD = %s, held %+v; want %s held (signed by a key not allowed)", head(t, r), h, c3)
	}
	reviews[c3] = &ForgeReview{Number: 8, Author: "writer", MergedBy: "grain-bot[bot]"}
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if head(t, r) != c3 || r.Held() != nil {
		t.Fatalf("HEAD = %s, held %+v; want %s applied (merged by grain)", head(t, r), r.Held(), c3)
	}
	if err := r.setLoadedHead(ctx, c3); err != nil {
		t.Fatal(err)
	}

	// History rewritten under it, even to a signed commit: held.
	runGit(t, work, "reset", "--quiet", "--hard", c0)
	c4 := commitAndPush(t, work, "tables_d.json", "[]\n", allowedKey, "--force")
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if h := r.Held(); head(t, r) != c3 || h == nil || h.SHA != c4 || !strings.Contains(h.Reason, "rewritten") {
		t.Fatalf("HEAD = %s, held %+v; want %s kept and %s held as a rewrite", head(t, r), h, c3, c4)
	}

	// The rewrite's hold is released by accepting its tip by name.
	v.Accepted = append(v.Accepted, c4)
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if head(t, r) != c4 || r.Held() != nil {
		t.Fatalf("HEAD = %s, held %+v; want the accepted rewrite %s applied", head(t, r), r.Held(), c4)
	}
	if err := r.setLoadedHead(ctx, c4); err != nil {
		t.Fatal(err)
	}
	tip := c4

	// Signed with OpenPGP by a key this host's own keyring holds: git
	// would report that a good signature, but it is no allowed SSH key.
	if _, err := exec.LookPath("gpg"); err == nil {
		gnupg := filepath.Join(tmp, "gnupg")
		if err := os.Mkdir(gnupg, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GNUPGHOME", gnupg)
		if out, err := exec.Command("gpg", "--quiet", "--batch", "--passphrase", "", "--quick-gen-key",
			"host <host@example.com>", "ed25519", "sign", "never").CombinedOutput(); err != nil {
			t.Fatalf("gpg: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(work, "tables_e.json"), []byte("[]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, work, "add", "--all", ".")
		runGit(t, work, "-c", "user.email=host@example.com", "-c", "user.name=host", "-c", "gpg.format=openpgp",
			"commit", "--quiet", "-S", "-m", "change tables_e.json")
		runGit(t, work, "push", "--quiet", "origin", "HEAD:main")
		c5 := runGit(t, work, "rev-parse", "HEAD")
		if got := runGit(t, work, "log", "-1", "--format=%G?", c5); got != "G" {
			t.Fatalf("the OpenPGP-signed commit reads as %q to plain git, want G", got)
		}
		if _, err := r.PullExternal(ctx); err != nil {
			t.Fatal(err)
		}
		if h := r.Held(); head(t, r) != c4 || h == nil || h.SHA != c5 || !strings.Contains(h.Reason, "not signed by an allowed key") {
			t.Fatalf("HEAD = %s, held %+v; want %s kept and the OpenPGP-signed %s held", head(t, r), h, c4, c5)
		}
		tip = c5
	}

	// Off again, and the tip is applied as it always was.
	r.SetVerifier(nil)
	if _, err := r.PullExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if head(t, r) != tip || r.Held() != nil {
		t.Fatalf("HEAD = %s with require_verified off, want %s", head(t, r), tip)
	}
}
