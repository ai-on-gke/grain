package workflows

import "testing"

func mustParse(t *testing.T, path, body string) Workflow {
	t.Helper()
	w, err := Parse(path, []byte(body))
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return w
}

// The environment grain itself hands a job is part of what that job
// does, so every variable in it -- including one somebody adds to
// runEnv tomorrow -- has to be able to move the key. This is the test
// that says so: it rebuilds the fingerprint Fingerprint builds, checks
// it is still the same one, and then tampers with each variable in turn.
func TestFingerprintCoversEveryVariableGrainSupplies(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  one:\n    steps:\n      - run: make test\n")
	req := (Request{Repo: "o/r", Ref: "main", Commit: "abc123", Event: EventPush}).Normalize()
	fp := jobFingerprint{
		Version:      fingerprintVersion,
		Repo:         req.Repo,
		Commit:       req.Commit,
		Env:          RunEnv(req, w.Name, w.Jobs[0].ID),
		JobID:        w.Jobs[0].ID,
		WorkflowPath: w.Path,
		WorkflowEnv:  w.Env,
		Job:          w.Jobs[0],
	}
	base := Fingerprint(req, w, w.Jobs[0])
	if got := HashOf("job", fp); got != base {
		t.Fatalf("this test no longer builds what Fingerprint builds:\n %s\n %s", base, got)
	}
	for name := range fp.Env {
		tampered := map[string]string{}
		for k, v := range fp.Env {
			tampered[k] = v
		}
		tampered[name] = "something else entirely"
		changed := fp
		changed.Env = tampered
		if HashOf("job", changed) == base {
			t.Errorf("a job told a different %s shares its key with one that was not", name)
		}
	}
}

func TestFingerprintCanonicalizesDigestPinnedSandboxImageAndExcludesBuiltinJobs(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  one:\n    steps:\n      - run: make test\n")
	digest := "sha256:6b2003bc1c0cfbb39c62a68421ab9fa9d4fc82183ab8f3775c4892cf54e02203"
	req1 := Request{
		Repo:         "o/r",
		Ref:          "main",
		Commit:       "abc123",
		Event:        EventPush,
		SandboxImage: "ghcr.io/bwsalmon/grain/guest:sha-66c5a15@" + digest,
	}
	req2 := req1
	req2.SandboxImage = "ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@" + digest
	if got1, got2 := Fingerprint(req1, w, w.Jobs[0]), Fingerprint(req2, w, w.Jobs[0]); got1 != got2 {
		t.Errorf("two sha- tags pointing at the same digest must share a key:\n %s\n %s", got1, got2)
	}

	req3 := req1
	req3.SandboxImage = "ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if Fingerprint(req1, w, w.Jobs[0]) == Fingerprint(req3, w, w.Jobs[0]) {
		t.Error("different guest digests must not share a key")
	}

	builtin := w.Jobs[0]
	builtin.Builtin = "grain:state-check"
	if Fingerprint(req1, w, builtin) != Fingerprint(req3, w, builtin) {
		t.Error("a built-in job runs inside the daemon rather than a sandbox, so the sandbox image must not move its key")
	}
}
