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
	"path/filepath"
	"strings"
	"testing"
)

// Releases cut before the switch to Docker Hub (#739) -- v3.1.0 among
// them -- had release jobs that published to ghcr.io/bwsalmon/grain/*
// alone, under the old leaf names (guest, k8s-sandbox). Promoting one to
// docker.io/grainai/* failed naming every sha- image as never published
// (task 3249), and re-running such a release would only publish to GHCR
// again. So promotion copies what Docker Hub lacks from GHCR by digest,
// refusing a copy whose digest differs, and then promotes exactly as it
// does any other release.

// legacyAnswers is a docker stub's answers for a registry pair in which
// GHCR holds the release's images (when ghcrHas) and Docker Hub holds a
// tag only once this run has written it (`imagetools create --tag`).
// Every digest asked for is sha256:1234 -- except a Docker Hub copy's,
// which is copiedDigest. The GHCR grain image is the stamped build of
// release v1.5.0 from commit.
func legacyAnswers(t *testing.T, commit string, ghcrHas bool, copiedDigest string) string {
	t.Helper()
	has := "exit 0"
	if !ghcrHas {
		has = "exit 1"
	}
	script := `written() { grep -qF -- "--tag $1 " "$DOCKER_STUB_LOG" || grep -qF -- "push $1" "$DOCKER_STUB_LOG"; }
case "$*" in
  "buildx imagetools inspect ghcr.io/"*" --format {{.Manifest.Digest}}")
    ` + strings.Replace(has, "exit 0", "echo sha256:1234; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain"*" --format "*revision*)
    ` + strings.Replace(has, "exit 0", "echo "+commit+"; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain"*" --format "*version*)
    ` + strings.Replace(has, "exit 0", "echo v1.5.0; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/"*) ` + has + ` ;;
  "pull -q ghcr.io/"*) ` + has + ` ;;
  "buildx imagetools inspect docker.io/grainai/"*" --format {{.Manifest.Digest}}")
    written "$4" || exit 1; echo ` + copiedDigest + `; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/grain:v1.5.0 --format "*revision*)
    written "$4" || exit 1; echo ` + commit + `; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/grain:v1.5.0 --format "*version*)
    written "$4" || exit 1; echo v1.5.0; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/"*) written "$4" || exit 1 ;;
  "pull -q docker.io/grainai/"*) written "$3" || exit 1 ;;
  "image inspect --format "*" docker.io/grainai/grain:v1.5.0") echo "docker.io/grainai/grain@sha256:9999" ;;
  "run --rm "*" sandbox-image") echo "${STUB_SANDBOX_IMAGE:-ghcr.io/bwsalmon/grain/guest:sha-` + commit[:7] + `@sha256:1234}" ;;
  "run --rm "*" k8s-sandbox-image") echo "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-` + commit[:7] + `@sha256:1234" ;;
  "run --rm "*" image") echo "ghcr.io/bwsalmon/grain/grain:sha-` + commit[:7] + `" ;;
  "run --rm "*" version --json") printf '{\n  "release": "v1.5.0"\n}\n' ;;
esac
`
	path := filepath.Join(t.TempDir(), "answers.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	return path
}

// The promotion of a pre-Docker-Hub release copies its sandbox images,
// their bases and its stamped grain image from GHCR by digest, rebuilds
// nothing, and then moves vX.Y.Z and latest onto them.
func TestPromoteOfAPreDockerHubReleaseCopiesItsGHCRImagesByDigest(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], true, "sha256:1234")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	for _, copy := range [][2]string{
		{"sandbox:sha-" + short, "guest"},
		{"sandbox-k8s:sha-" + short, "k8s-sandbox"},
		{"sandbox-base:sha-" + short, "guest-base"},
		{"sandbox-k8s-base:sha-" + short, "k8s-sandbox-base"},
		{"grain:v1.5.0", "grain"},
	} {
		containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/"+copy[0]+" ghcr.io/bwsalmon/grain/"+copy[1]+"@sha256:1234")
	}
	containsRun(t, out, "copied ghcr.io/bwsalmon/grain/guest:sha-"+short+" to docker.io/grainai/sandbox:sha-"+short+" by digest (sha256:1234)")
	containsRun(t, out, "docker.io/grainai/grain:v1.5.0 already reports v1.5.0; nothing to rebuild")
	if strings.Contains(docker, "\nbuild ") || strings.HasPrefix(docker, "build ") {
		t.Errorf("promote rebuilt the grain image although GHCR's stamped build was copied:\n%s", docker)
	}
	containsRun(t, docker, retagLine("sandbox", short, "v1.5.0"))
	containsRun(t, docker, retagLine("sandbox-k8s", short, "latest"))
	containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/grain:latest docker.io/grainai/grain:v1.5.0")
}

// A copy whose digest is not its source's is not the image that was
// tested: the promotion stops there and moves no release tag.
func TestPromoteOfAPreDockerHubReleaseRefusesACopyWithAnotherDigest(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], true, "sha256:5678")))
	if err == nil {
		t.Fatalf("promote succeeded although the copy's digest differs:\n%s", out)
	}
	containsRun(t, out, "the copy is not the image that was tested")
	if strings.Contains(docker, ":v1.5.0 docker.io/") || strings.Contains(docker, "--tag docker.io/grainai/sandbox:latest") {
		t.Errorf("promote moved a release tag after a bad copy:\n%s", docker)
	}
}

// A pre-Docker-Hub release that GHCR does not have either says so, and
// does not send the operator to re-run a release that publishes to GHCR.
func TestPromoteOfAPreDockerHubReleaseMissingFromGHCRSaysWhy(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], false, "sha256:1234")))
	if err == nil {
		t.Fatalf("promote succeeded with no images in either registry:\n%s", out)
	}
	containsRun(t, out, "never published docker.io/grainai/sandbox:sha-"+short)
	containsRun(t, out, "v1.5.0 predates the switch to Docker Hub (#739)")
	if strings.Contains(out, "POST /api/repos/") {
		t.Errorf("promote of a pre-Docker-Hub release gave the generic re-run advice:\n%s", out)
	}
	if strings.Contains(docker, "imagetools create") {
		t.Errorf("promote moved a tag with nothing to promote:\n%s", docker)
	}
}

// A release cut since the switch copies nothing from GHCR, and keeps the
// generic advice: its own release job publishes to Docker Hub.
func TestPromoteOfAPostDockerHubReleaseCopiesNothingFromGHCR(t *testing.T) {
	remote, commits := releaseRepo(t)
	missing := "docker.io/grainai/sandbox:sha-" + commits["released"][:7]
	env := publishEnv(stubAnswers(t, commits["released"][:7], "", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = missing
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with %s missing:\n%s", missing, out)
	}
	containsRun(t, out, "POST /api/repos/")
	if strings.Contains(docker, "ghcr.io") || strings.Contains(out, "predates") {
		t.Errorf("promote of a post-switch release looked at GHCR:\nout:\n%s\ndocker:\n%s", out, docker)
	}
}

// The dry run says which images a pre-Docker-Hub promotion would copy.
func TestPromoteDryRunOfAPreDockerHubReleaseNamesTheCopy(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "v1.5.0 predates the switch to Docker Hub (#739), so first copy by digest")
	containsRun(t, out, "ghcr.io/bwsalmon/grain/{guest,k8s-sandbox,guest-base,k8s-sandbox-base}:sha-"+short)
}

// A pre-Docker-Hub release GHCR has only part of -- its stamped grain
// image but not its sandbox images -- writes no :vX.Y.Z to Docker Hub:
// promotion refuses it as having nothing to move, and must not leave a
// release tag behind that says otherwise.
func TestPromoteOfAPartialPreDockerHubReleaseCopiesNoReleaseTag(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	env := publishEnv(legacyAnswers(t, commits["released"], true, "sha256:1234"))
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/guest:sha-" + short
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with GHCR's sandbox image missing:\n%s", out)
	}
	containsRun(t, out, "never published docker.io/grainai/sandbox:sha-"+short)
	if strings.Contains(docker, "--tag docker.io/grainai/grain:") {
		t.Errorf("promote copied the grain image although the release could not be promoted:\n%s", docker)
	}
}

// legacySigningEnv is publishEnv with a signing key in place and a cosign
// stub that logs what it is asked and, where unsigned, finds no
// signature on the GHCR source.
func legacySigningEnv(t *testing.T, answers string, unsigned bool) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "cosign.log")
	verify := "exit 0"
	if unsigned {
		verify = "exit 1"
	}
	stub := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$1\" in\n" +
		"  public-key) echo '-----BEGIN PUBLIC KEY-----'; echo 'stub'; echo '-----END PUBLIC KEY-----' ;;\n" +
		"  verify) " + verify + " ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "image-signing-key")
	if err := os.WriteFile(key, []byte("stub key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := publishEnv(answers)
	env["IMAGE_SIGNING_KEY"] = key
	env["PATH"] = dir + ":" + realToolPath(t)
	return env, log
}

// With a signing key, a GHCR image is signed on Docker Hub only after its
// GHCR digest verified against that same key: the copy is not vouched
// for on the strength of a mutable tag in another registry.
func TestPromoteOfAPreDockerHubReleaseSignsOnlyWhatVerified(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	env, log := legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), false)
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	asked, _ := os.ReadFile(log)
	containsRun(t, string(asked), "verify --key ")
	containsRun(t, string(asked), "--insecure-ignore-tlog=true ghcr.io/bwsalmon/grain/guest@sha256:1234")
	containsRun(t, string(asked), "docker.io/grainai/sandbox@sha256:1234")
	containsRun(t, out, "copied ghcr.io/bwsalmon/grain/guest:sha-"+short+" to docker.io/grainai/sandbox:sha-"+short)
}

// A GHCR image that carries no signature made with the job's key is
// copied unsigned, with a warning, and never signed with that key -- and
// with REQUIRE_IMAGE_SIGNING it is not copied at all.
func TestPromoteOfAPreDockerHubReleaseDoesNotSignAnUnverifiedSource(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]

	env, log := legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), true)
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote without REQUIRE_IMAGE_SIGNING failed on unsigned GHCR source: %v\n%s", err, out)
	}
	asked, _ := os.ReadFile(log)
	if strings.Contains(string(asked), "sign --yes --key "+env["IMAGE_SIGNING_KEY"]+" --tlog-upload=false --new-bundle-format=false --use-signing-config=false docker.io/grainai/") {
		t.Errorf("promote signed a copy whose source did not verify:\n%s", asked)
	}
	containsRun(t, out, "carries no signature made with")
	containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/sandbox:sha-"+short+" ghcr.io/bwsalmon/grain/guest@sha256:1234")

	env, _ = legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), true)
	env["REQUIRE_IMAGE_SIGNING"] = "true"
	out, docker, err = promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with REQUIRE_IMAGE_SIGNING and an unsigned source:\n%s", out)
	}
	containsRun(t, out, "REQUIRE_IMAGE_SIGNING is set, so it is not copied")
	if strings.Contains(docker, "imagetools create --tag docker.io/grainai/") {
		t.Errorf("promote copied an unverified source although signing is required:\n%s", docker)
	}
}

// When GHCR lacks a stamped grain:vX.Y.Z and promote falls back to
// rebuilding from grain:sha-<short>, the rebuilt grain:vX.Y.Z is signed
// only when its sha-<short> source verified against the job's key -- and
// an unverified sha-<short> is never vouched for by rebuilding it.
func TestPromoteRebuildSignsOnlyWhenShaSourceVerified(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]

	// Verified sha-<short> source: pulled by verified digest and rebuilt + signed.
	env, log := legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), false)
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/grain:v1.5.0"
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote failed on verified sha-<short> fallback: %v\n%s\ndocker:\n%s", err, out, docker)
	}
	asked, _ := os.ReadFile(log)
	containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/grain:sha-"+short+" ghcr.io/bwsalmon/grain/grain@sha256:1234")
	containsRun(t, docker, "pull docker.io/grainai/grain@sha256:1234")
	containsRun(t, string(asked), "sign --yes --key "+env["IMAGE_SIGNING_KEY"]+" --tlog-upload=false --new-bundle-format=false --use-signing-config=false docker.io/grainai/grain@sha256:9999")

	// Unverified sha-<short> source: still pulled by resolved digest, rebuilt unsigned with a warning, never signed.
	env, log = legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), true)
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/grain:v1.5.0"
	out, docker, err = promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote without REQUIRE_IMAGE_SIGNING failed on unsigned sha-<short> fallback: %v\n%s\ndocker:\n%s", err, out, docker)
	}
	asked, _ = os.ReadFile(log)
	if strings.Contains(string(asked), "sign --yes --key ") {
		t.Errorf("rebuild_release_grain signed a rebuild whose sha-<short> source did not verify:\n%s", asked)
	}
	containsRun(t, docker, "pull docker.io/grainai/grain@sha256:1234")
	containsRun(t, out, "so docker.io/grainai/grain:v1.5.0 is published unsigned")
}

// A GHCR grain:sha-<short> whose org.opencontainers.image.revision label
// names another commit (e.g. an older signed build retagged onto this
// commit's sha- tag) is refused before it can be copied or rebuilt.
func TestPromoteOfAPreDockerHubReleaseRefusesMismatchedShaRevision(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	env, _ := legacySigningEnv(t, legacyAnswers(t, commits["old"], true, "sha256:1234"), false)
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/grain:v1.5.0"
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with a grain:sha- image labelled with another commit:\n%s", out)
	}
	containsRun(t, out, "is labelled with "+commits["old"]+", not "+commits["released"]+", so it is not copied")
	if strings.Contains(docker, "--tag docker.io/grainai/grain:") || strings.Contains(docker, "--tag docker.io/grainai/sandbox:v1.5.0") {
		t.Errorf("promote copied grain or moved a release tag for a mismatched commit:\n%s", docker)
	}
}

// Malformed sandbox or image references returned by a source image (such
// as shell metacharacters that a release's Dockerfile RUN would expand, or
// leading hyphens) are refused before docker build runs.
func TestPromoteRefusesMalformedSourceImageReferences(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	for _, bad := range []string{
		"ghcr.io/bwsalmon/grain/guest:sha-bad;touch /tmp/injected",
		"-bad-leading-flag",
	} {
		env := publishEnv(legacyAnswers(t, commits["released"], true, "sha256:1234"))
		env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/grain:v1.5.0"
		env["STUB_SANDBOX_IMAGE"] = bad
		out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
		if err == nil {
			t.Fatalf("promote succeeded with malformed sandbox-image reference %q:\n%s", bad, out)
		}
		containsRun(t, out, "answered with malformed image references")
		if strings.Contains(docker, "\nbuild ") || strings.HasPrefix(docker, "build ") {
			t.Errorf("promote invoked docker build with malformed build-arg %q:\n%s", bad, docker)
		}
	}
}

// When a signing key is configured (even without REQUIRE_IMAGE_SIGNING),
// an unusable key, a registry auth failure during signature verification,
// or an unreadable source digest must fail closed rather than looking like
// an unsigned source and quietly publishing an unsigned release.
func TestPromoteFailsClosedWhenSigningKeyOrDigestCheckFails(t *testing.T) {
	t.Run("broken signing key fails copy_by_digest", func(t *testing.T) {
		remote, commits := preDockerHubReleaseRepo(t)
		env, _ := legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), false)
		dir := t.TempDir()
		stub := "#!/bin/sh\nif [ \"$1\" = \"public-key\" ]; then echo 'decrypt failed' >&2; exit 1; fi\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		env["PATH"] = dir + ":" + realToolPath(t)
		out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
		if err == nil {
			t.Fatalf("promote succeeded with an unusable signing key:\n%s", out)
		}
		containsRun(t, out, "could not derive a public key")
		if strings.Contains(docker, "imagetools create --tag docker.io/grainai/") {
			t.Errorf("promote copied an image unsigned when the signing key could not be unlocked:\n%s", docker)
		}
	})

	t.Run("registry auth error on cosign verify fails closed", func(t *testing.T) {
		remote, commits := preDockerHubReleaseRepo(t)
		env, _ := legacySigningEnv(t, legacyAnswers(t, commits["released"], true, "sha256:1234"), false)
		dir := t.TempDir()
		stub := "#!/bin/sh\ncase \"$1\" in\n" +
			"  public-key) echo '-----BEGIN PUBLIC KEY-----'; echo 'stub'; echo '-----END PUBLIC KEY-----' ;;\n" +
			"  verify) echo 'GET https://ghcr.io/v2/...: UNAUTHORIZED: authentication required' >&2; exit 1 ;;\n" +
			"esac\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		env["PATH"] = dir + ":" + realToolPath(t)
		out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
		if err == nil {
			t.Fatalf("promote succeeded when cosign verify was refused with UNAUTHORIZED:\n%s", out)
		}
		containsRun(t, out, "registry refused signature check")
		if strings.Contains(docker, "imagetools create --tag docker.io/grainai/") {
			t.Errorf("promote copied an image unsigned when cosign verify failed with UNAUTHORIZED:\n%s", docker)
		}
	})

	t.Run("unreadable source digest in rebuild_release_grain fails closed with signing key", func(t *testing.T) {
		remote, commits := releaseRepo(t)
		short := commits["released"][:7]
		// stubAnswers returns no digest for grain:sha-<short>, simulating a transient
		// failure reading the source digest after image_exists succeeded.
		env, _ := legacySigningEnv(t, stubAnswers(t, short, "", "v1.5.0"), false)
		out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
		if err == nil {
			t.Fatalf("promote succeeded with signing key and unreadable source digest:\n%s", out)
		}
		containsRun(t, out, "could not read the digest of docker.io/grainai/grain:sha-"+short)
		if strings.Contains(docker, "push ") || strings.Contains(docker, "imagetools create") {
			t.Errorf("promote published unsigned images after failing to read source digest:\n%s", docker)
		}
	})
}
