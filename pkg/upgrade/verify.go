package upgrade

// Who published the image an upgrade is about to run (grain/task-1773).
//
// pinImage and kubePin make an upgrade run exactly the bytes it checked,
// and TagForBranch makes the tag it pulled them by mean exactly one
// branch. Neither says anything about who put those bytes there: anyone
// who can push to the package -- or who controls the registry, or a
// proxy in front of it -- can still have their image health-checked,
// which is running it, and then booted. This step is what answers that,
// and it answers it before anything runs the image at all.
//
// The answer is a signature, and checking one is somebody else's job:
// VerifyCmd is a command, on a real deployment `cosign verify` with the
// release key (docs/image-signing.md). This package decides only *when*
// it runs -- after the pin, so it is asked about the digest that will be
// run and not about a tag that can move between the two, and before the
// health check -- and what its answer means.
//
// A deployment that configures none is warned, not refused. Nothing that
// was published before release-images.sh began signing carries a
// signature, so refusing unsigned images by default would strand every
// installed deployment on the build it runs now -- including one asked
// to roll back -- until an operator configured a key it had no reason to
// know it needed. The step says, every time, that what it is about to run
// was not verified; configuring VerifyCmd is how a deployment opts into
// refusing.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// stepVerify is the step that checks who published the image: after the
// pull on the image path, and first of all on the Kubernetes one, where
// nothing runs before it either.
const stepVerify = "signature"

// verifyTimeout bounds one VerifyCmd. cosign fetches a signature
// manifest and a layer or two of a few hundred bytes, starts no
// container and pulls no image, so this is a network round trip's bound
// rather than imageRunTimeout's container lifetime.
var verifyTimeout = 2 * time.Minute

// unverifiedNote is what the signature step says when this deployment
// has no VerifyCmd: skipped, and in words that do not let anybody read
// the skip as a pass.
const unverifiedNote = "no signature verification configured (-upgrade-verify-cmd): running whatever the registry serves for this tag, whoever published it"

// digestRef is ref -- `repo:tag@sha256:...`, or `repo@sha256:...` --
// with the tag taken out. What a verifier is asked about is the digest
// alone: a signature covers a manifest digest, not a tag, and a ref
// carrying both is one not every tool parses the same way.
func digestRef(ref string) (string, bool) {
	_, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") || len(digest) == len("sha256:") {
		return "", false
	}
	return repositoryOf(ref) + "@" + digest, true
}

// verifyPinned runs verifyCmd against ref, an image already pinned to
// its digest, and returns nil only if it exits 0. The image path's form:
// the pull has already said which digest this upgrade runs, so all that
// is left to ask is whether that digest is signed.
//
// A verifier that prints which digests it verified -- cosign's JSON
// does -- is also held to having verified this one. A command that
// ignores the argument it was handed and verifies something else is a
// misconfiguration this would otherwise pass in silence.
func verifyPinned(ctx context.Context, p *progress, step string, verifyCmd []string, ref string) error {
	target, ok := digestRef(ref)
	if !ok {
		return fmt.Errorf("%s is not pinned to a digest, so there is nothing a signature could be checked against", ref)
	}
	argv := append(append([]string{}, verifyCmd...), target)
	out, err := runAsked(ctx, p, step, verifyTimeout, argv)
	if err != nil {
		return fmt.Errorf("verifying the signature on %s: %w", target, err)
	}
	digests := verifiedDigests(out)
	if len(digests) == 0 {
		return nil
	}
	_, want, _ := strings.Cut(target, "@")
	for _, d := range digests {
		if d == want {
			return nil
		}
	}
	return fmt.Errorf("verifying the signature on %s: the verifier reported signatures for %s and not for the digest it was asked about",
		target, strings.Join(digests, ", "))
}

// verifyTag runs verifyCmd against tag, an image that has not been
// pulled, and returns `tag@sha256:...` for the one digest the verifier
// says it verified. The Kubernetes path's form: there, the first thing to
// fetch an image is a pod that runs it, so the digest has to be known --
// and signed -- before any pod exists, and the verifier is what resolves
// it.
//
// That needs the verifier to say what it verified, which `cosign verify`
// does on stdout as one JSON payload per signature (verifiedDigests). A
// verifier that only exits 0 resolves nothing, and is refused here rather
// than treated as a pass: the pod would then pull whatever the tag named
// by the time it started, which need not be what was verified.
//
// A tag that is already pinned -- the sandbox reference a release stamps
// in, `repo:sha-<short>@sha256:...` -- has nothing left to resolve: it is
// verified as verifyPinned verifies one, and comes back unchanged.
func verifyTag(ctx context.Context, p *progress, step string, verifyCmd []string, tag string) (string, error) {
	if strings.Contains(tag, "@") {
		if err := verifyPinned(ctx, p, step, verifyCmd, tag); err != nil {
			return "", err
		}
		return tag, nil
	}
	argv := append(append([]string{}, verifyCmd...), tag)
	out, err := runAsked(ctx, p, step, verifyTimeout, argv)
	if err != nil {
		return "", fmt.Errorf("verifying the signature on %s: %w", tag, err)
	}
	digests := verifiedDigests(out)
	switch len(digests) {
	case 1:
		return tag + "@" + digests[0], nil
	case 0:
		return "", fmt.Errorf("verifying the signature on %s: the verifier exited 0 but named no digest it verified (it printed %q), so there is nothing to pin the check pod to", tag, strings.TrimSpace(out))
	}
	return "", fmt.Errorf("verifying the signature on %s: the verifier named %d different digests (%s), so which one the tag means is not settled", tag, len(digests), strings.Join(digests, ", "))
}

// verifiedDigests reads the manifest digests out of what `cosign verify`
// prints on stdout: a JSON array of simple-signing payloads, or -- with
// --output text, or from older releases -- one payload per line, each
// carrying critical.image["docker-manifest-digest"]. Distinct, in the
// order first seen; nil for output that is none of those, which is what
// a verifier that only reports by exit status prints.
func verifiedDigests(out string) []string {
	type payload struct {
		Critical struct {
			Image struct {
				Digest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	var payloads []payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payloads); err != nil {
		payloads = nil
		for _, line := range strings.Split(out, "\n") {
			var one payload
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &one) == nil {
				payloads = append(payloads, one)
			}
		}
	}
	var digests []string
	seen := map[string]bool{}
	for _, pl := range payloads {
		d := pl.Critical.Image.Digest
		if !strings.HasPrefix(d, "sha256:") || len(d) == len("sha256:") || seen[d] {
			continue
		}
		seen[d] = true
		digests = append(digests, d)
	}
	return digests
}
