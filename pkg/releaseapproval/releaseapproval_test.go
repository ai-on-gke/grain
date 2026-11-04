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

package releaseapproval

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureRepo is a git repository with v1.2.0-pending tagged, and the
// commit it tags.
func fixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "one")
	run("tag", "-a", "v1.2.0-pending", "-m", "Release v1.2.0 pending approval")
	return dir, run("rev-parse", "HEAD")
}

func script(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../scripts/verify-release-approval.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// verify runs scripts/verify-release-approval.sh as promote-release.yml
// does, returning its stdout and whether it passed.
func verify(t *testing.T, dir string, env map[string]string) (string, string, bool) {
	t.Helper()
	cmd := exec.Command("bash", script(t))
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), stderr.String(), err == nil
}

func TestTheWorkflowsVerifierAcceptsWhatTheDeploymentSignsAndNothingElse(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed")
	}
	dir, commit := fixtureRepo(t)
	signer, err := LoadSigner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	rec := Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "approve", Commit: commit,
		Approver: "alice@example.com", Issued: now, Expires: now.Add(Lifetime)}
	signed, err := signer.Sign(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	env := func(over map[string]string) map[string]string {
		e := map[string]string{
			"APPROVAL": signed.Record, "APPROVAL_SIGNATURE": signed.Signature, "APPROVAL_PUBLIC_KEY": pub,
			"EXPECT_REPO": "example/widgets", "EXPECT_VERSION": "v1.2.0", "EXPECT_DECISION": "approve",
			"EXPECT_SHORT_SOAK_OK": "false", "EXPECT_ALLOW_BREAKING": "false", "NOW": strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
		}
		for k, v := range over {
			e[k] = v
		}
		return e
	}
	if by, stderr, ok := verify(t, dir, env(nil)); !ok || by != "alice@example.com" {
		t.Fatalf("a signed approval for this run = %q, ok=%v (%s); want alice@example.com", by, ok, stderr)
	}

	other, err := LoadSigner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := other.Sign(context.Background(), rec)
	tampered := strings.Replace(mustDecode(t, signed.Record), "alice@example.com", "mallory@example.com", 1)
	for name, over := range map[string]map[string]string{
		"no public key":                 {"APPROVAL_PUBLIC_KEY": ""},
		"no record":                     {"APPROVAL": ""},
		"another key's signature":       {"APPROVAL_SIGNATURE": forged.Signature},
		"a tampered record":             {"APPROVAL": base64.StdEncoding.EncodeToString([]byte(tampered))},
		"another repository":            {"EXPECT_REPO": "example/other"},
		"another version":               {"EXPECT_VERSION": "v1.3.0"},
		"the other decision":            {"EXPECT_DECISION": "reject"},
		"a short soak not granted":      {"EXPECT_SHORT_SOAK_OK": "true"},
		"breaking changes not accepted": {"EXPECT_ALLOW_BREAKING": "true"},
		"expired":                       {"NOW": strconv.FormatInt(now.Add(Lifetime+time.Second).Unix(), 10)},
	} {
		if by, _, ok := verify(t, dir, env(over)); ok {
			t.Errorf("%s: verified (approver %q); want refused", name, by)
		}
	}

	// A record for a commit the -pending tag no longer stages -- the
	// train re-staged the release -- is void.
	stale := rec
	stale.Commit = strings.Repeat("a", 40)
	s2, _ := signer.Sign(context.Background(), stale)
	if _, _, ok := verify(t, dir, env(map[string]string{"APPROVAL": s2.Record, "APPROVAL_SIGNATURE": s2.Signature})); ok {
		t.Error("a record for another commit verified; want refused")
	}
}

func TestTheWorkflowsVerifierAcceptsASignedRollbackAndBindsTargetAndReason(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed")
	}
	dir, commit := fixtureRepo(t)
	tagCmd := exec.Command("git", "tag", "-a", "v1.2.0", "-m", "Release v1.2.0")
	tagCmd.Dir = dir
	tagCmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := tagCmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v1.2.0: %v\n%s", err, out)
	}

	signer, err := LoadSigner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	rec := Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "rollback", Commit: commit,
		Approver: "alice@example.com", RollbackTo: "v1.1.0", Reason: "schema migration panicked: see issue #42",
		Issued: now, Expires: now.Add(Lifetime)}
	signed, err := signer.Sign(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	env := func(over map[string]string) map[string]string {
		e := map[string]string{
			"APPROVAL": signed.Record, "APPROVAL_SIGNATURE": signed.Signature, "APPROVAL_PUBLIC_KEY": pub,
			"EXPECT_REPO": "example/widgets", "EXPECT_VERSION": "v1.2.0", "EXPECT_DECISION": "rollback",
			"EXPECT_SHORT_SOAK_OK": "false", "EXPECT_ALLOW_BREAKING": "false",
			"EXPECT_ROLLBACK_TO": "v1.1.0", "EXPECT_REASON": "schema migration panicked: see issue #42",
			"NOW": strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
		}
		for k, v := range over {
			e[k] = v
		}
		return e
	}
	if by, stderr, ok := verify(t, dir, env(nil)); !ok || by != "alice@example.com" {
		t.Fatalf("a signed rollback = %q, ok=%v (%s); want alice@example.com", by, ok, stderr)
	}
	for name, over := range map[string]map[string]string{
		"another rollback_to": {"EXPECT_ROLLBACK_TO": "v1.0.0"},
		"empty rollback_to":   {"EXPECT_ROLLBACK_TO": ""},
		"altered reason":      {"EXPECT_REASON": "different reason"},
		"untagged release":    {"EXPECT_VERSION": "v1.1.0"},
	} {
		if by, _, ok := verify(t, dir, env(over)); ok {
			t.Errorf("%s: verified (approver %q); want refused", name, by)
		}
	}
}

func TestRecordRefusesValuesThatAreNotOneSafeToken(t *testing.T) {
	r := Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "approve", Commit: "abc",
		Approver: "alice\ncommit: evil", Issued: time.Unix(1, 0), Expires: time.Unix(2, 0)}
	if _, err := r.Text(); err == nil {
		t.Error("an approver with a newline encoded; want refused")
	}
	r.Approver, r.Decision = "alice", "promote"
	if _, err := r.Text(); err == nil {
		t.Error("decision promote encoded; want refused")
	}
}

func TestLoadSignerKeepsOneKey(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadSigner(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadSigner(dir)
	if err != nil {
		t.Fatal(err)
	}
	pa, _ := a.PublicKeyPEM()
	pb, _ := b.PublicKeyPEM()
	if pa != pb {
		t.Error("a second load made a new key")
	}
	if _, err := LoadSigner(""); err == nil {
		t.Error("no data directory gave a signer")
	}
}

func TestLoadSignerRacingItselfMakesOneKey(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	keys := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := LoadSigner(dir)
			if err != nil {
				errs[i] = err
				return
			}
			keys[i], errs[i] = s.PublicKeyPEM()
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("load %d: %v", i, errs[i])
		}
		if keys[i] != keys[0] {
			t.Fatalf("load %d read a different key than load 0", i)
		}
	}
}

func mustDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
