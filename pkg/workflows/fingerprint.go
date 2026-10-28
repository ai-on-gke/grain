package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Key is a content address for work: two pieces of work with the same
// key are the same work, so a result recorded under one is a result the
// other need not produce again.
//
// A string rather than a [32]byte so it survives a round trip through
// JSON, a log line and whatever a cache eventually stores it in.
type Key string

// String makes a Key printable without a conversion at every call site.
func (k Key) String() string { return string(k) }

// fingerprintVersion is bumped whenever the *meaning* of a key changes
// -- a new field folded in, a normalization corrected, an environment
// variable added to what a job is run with. It is part of every key, so
// bumping it misses every entry a cache holds under the old rules
// rather than serving them for work they no longer describe.
//
// This is the whole safety property of the cache: a stale hit is a green
// tick over code nobody tested, and the only defence against one is that
// everything which could change the answer is either in the key or in
// this number.
//
// 2 is the environment grain supplies (runEnv) becoming part of the key.
// Version 1 hashed the commit and the job's declaration and left the
// branch and the event out on the argument that two branches at one
// commit are one tree -- true of the tree, false of the job, because
// grain hands every step GRAIN_CI_REF and GRAIN_CI_EVENT and a step is
// entitled to read them. Folding the map in wholesale rather than
// naming its fields is deliberate: it is what makes "a new variable is
// covered by the key" automatic rather than a rule somebody has to
// remember at the moment they add one.
//
// 3 is a job's `paths:` -- a job that declares which files it depends on
// is keyed on the content of those files rather than on the commit
// (paths.go). That changes what a key means for every job in every
// deployment, whether or not it declares one: a job that declares nothing
// is keyed on the same things it was, but the struct below now has a
// field for the ones that do, and json.Marshal writes it. So every entry
// held under the old rules is missed rather than served, which is what
// this number is for.
//
// 4 is a sandbox job's SandboxImage becoming part of the key, replacing
// the whole-cache .cache-epoch wipe (daemon:<rev>|guest:<image>) that
// cleared every cached result on every daemon upgrade. Built-in jobs
// (job.Builtin != "") run inside the daemon rather than a sandbox, so
// their SandboxImage stays empty (and state check already puts its build
// identity in step.Env).
const fingerprintVersion = 4

// jobFingerprint is exactly what a job's identity is made of. It is a
// struct fed to encoding/json rather than a hand-rolled string, because
// json.Marshal sorts map keys and writes struct fields in declaration
// order -- both stable across builds -- and because adding a field to
// this struct is then the whole of "the key now covers that too".
//
// What is deliberately *not* in here:
//
//   - every other job in the file. A job is the unit that runs and the
//     unit that is cached; editing a job beside it does not change what
//     this job does.
//   - who asked, when, and which run it belonged to. That is the point:
//     the same commit asked about twice is one answer.
//
// The branch and the event *are* in here, through Env, and that is a
// reversal of what version 1 did. The argument for leaving them out was
// that two branches at one commit are one tree, so a push and the pull
// request behind it should share an answer -- which is true of the tree
// and false of the job, because runEnv hands every step GRAIN_CI_REF and
// GRAIN_CI_EVENT and a workflow is entitled to branch on them. A key
// that ignores what the job is told is a key that promises work is
// identical when it is not. Getting that sharing back means taking the
// ref and the event out of what a job is told, which is a change to the
// contract with workflow files and not something a cache may decide on
// its own.
type jobFingerprint struct {
	Capabilities   []string `json:"capabilities,omitempty"`
	ReleaseSecrets []string `json:"releaseSecrets,omitempty"`
	Version        int      `json:"v"`
	Repo           string   `json:"repo"`
	// Commit is empty for a job keyed on its declared files' content
	// instead -- which is the whole of what `paths:` buys, since a key
	// holding the commit is a key that misses on every push. Content
	// below is what stands in its place, and paths.go is what says why
	// that is safe.
	Commit string `json:"commit"`
	// Env is the environment grain itself supplies (runEnv) -- the repo,
	// the ref, the commit, the event, the workflow's name and the job's
	// id, exactly as the steps will see them. Hashed as the map it is, so
	// that adding a variable there changes every key here without anyone
	// having to notice.
	//
	// It makes Repo and Commit above redundant, and they stay anyway:
	// they are what a person reading this struct expects a content
	// address to be made of, and a key whose only mention of the commit
	// is inside a map named "env" is one nobody would trust at a glance.
	Env          map[string]string `json:"env"`
	JobID        string            `json:"job"`
	WorkflowPath string            `json:"workflow"`
	// WorkflowEnv is the workflow-level env this job inherits -- part of
	// what the job does, unlike the rest of the file.
	WorkflowEnv map[string]string `json:"workflowEnv,omitempty"`
	// Job is the declaration itself: steps, env, sandbox shape,
	// timeouts, needs. Marshalled through the Go types rather than the
	// file's bytes so that a comment, a reordered key or a change of
	// quoting does not miss a cache entry -- and so that a change to
	// *any* declared field does.
	//
	// That last part carries more weight than it used to. A workflow no
	// longer has to live in the repository -- one declared on grain's own
	// side (model.RepoWorkflow, DeclaredSource) is edited without any
	// commit changing -- so "same repo, same commit" is no longer a
	// promise that the work is the same, and this field is what keeps the
	// key honest about it: an edited job is a different job here, whether
	// the edit arrived as a push or as somebody saving a form.
	Job Job `json:"jobSpec"`
	// Content is the files this job declared it depends on and what is in
	// them: "<path>\x00<blob>" per file, in path order. Present only for
	// a job whose `paths:` matched something in a tree this run could
	// read, and it is what Commit above stops being for such a job.
	//
	// The paths as well as the hashes, so that a file *appearing* or
	// *vanishing* changes the key even where nothing that stayed changed
	// its content -- a deleted test is a different answer from a passing
	// one.
	Content []string `json:"content,omitempty"`
	// SandboxImage is the canonical OCI image reference this job's sandbox
	// is created from (CanonicalSandboxImage(Request.SandboxImage)), or
	// empty for a built-in job that runs inside the daemon rather than a
	// sandbox (or on a host-directory backend with no image).
	SandboxImage string `json:"sandboxImage,omitempty"`
}

// CanonicalSandboxImage returns the content-identifying form of a sandbox
// image reference for job fingerprinting. When the reference is pinned by
// digest ("repo:tag@sha256:..."), a tag before "@" is just a human-readable
// label for the commit that stamped the reference
// ("ghcr.io/bwsalmon/grain/guest:sha-dc5b5a1@sha256:...") while the digest
// is the image's actual content. Dropping the tag when a digest is present
// keeps the key identical across grain commits that reuse the same
// content-addressed guest image.
func CanonicalSandboxImage(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return ref
	}
	prefix, digest := ref[:at], ref[at:]
	if colon := strings.LastIndex(prefix, ":"); colon > strings.LastIndex(prefix, "/") {
		prefix = prefix[:colon]
	}
	return prefix + digest
}

// Fingerprint is the key one job of one workflow runs under, keyed on the
// commit -- which is what a caller with no tree in hand has to do, and
// what every job did before `paths:` existed.
//
// See jobFingerprint for what it covers and what it deliberately does not,
// and FingerprintIn for the content-addressed form.
func Fingerprint(req Request, w Workflow, job Job) Key {
	return FingerprintIn(req, w, job, nil)
}

// FingerprintIn is Fingerprint against a tree whose content is known: a
// job that declares `paths:` matching files in tree is keyed on those
// files' content rather than on the commit, so the same job across two
// commits that touched nothing it reads is one piece of work.
//
// A nil tree, a job that declares no `paths:`, and a declaration that
// matches nothing in this tree all key on the commit, which is the
// unchanged behaviour and the safe default. paths.go's doc comment is the
// argument for why the other branch is safe at all; do not widen it
// without reading that and bumping fingerprintVersion.
func FingerprintIn(req Request, w Workflow, job Job, tree *Tree) Key {
	req = req.Normalize()
	sandboxImage := ""
	if job.Builtin == "" {
		sandboxImage = CanonicalSandboxImage(req.SandboxImage)
	}
	fp := jobFingerprint{
		Capabilities:   req.Capabilities,
		ReleaseSecrets: req.ReleaseSecrets,
		Version:        fingerprintVersion,
		Repo:           req.Repo,
		Commit:         req.Commit,
		Env:            RunEnv(req, w.Name, job.ID),
		JobID:          job.ID,
		WorkflowPath:   w.Path,
		WorkflowEnv:    w.Env,
		Job:            job,
		SandboxImage:   sandboxImage,
	}
	if files, ok := tree.Match(job.Paths, job.PathsIgnore); ok {
		fp.Commit = ""
		fp.Env = contentEnv(fp.Env)
		fp.Content = make([]string, 0, len(files))
		for _, f := range files {
			fp.Content = append(fp.Content, f.Path+"\x00"+f.Blob)
		}
	}
	return HashOf("job", fp)
}

// contentEnv is runEnv without the two values that move on every push:
// the commit and the branch. It is the one place a content key gives
// something up, and it is deliberately a named, listed pair rather than a
// filter over a pattern -- a variable this does not name stays in the key,
// so a new one grain starts handing to steps is covered without anybody
// remembering to come here.
//
// Declaring `paths:` is therefore also a promise that the job's answer
// does not depend on which commit or which branch it ran against. That is
// true of a build and of a test suite, and false of a job that stamps a
// version into something it publishes -- which is `cache: false`'s job,
// not this one's. GRAIN_CI_EVENT stays, because a workflow is entitled to
// branch on a push against a release and those are genuinely different
// work.
func contentEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		switch k {
		case "GRAIN_CI_COMMIT", "GRAIN_CI_REF":
			continue
		}
		out[k] = v
	}
	return out
}

// planKey is the run-level fingerprint: the same request producing the
// same set of job keys is the same run. It is what Service.Start
// deduplicates live runs by.
//
// Derived from the job keys rather than from the request alone so that
// two requests for one commit that would run *different* work -- a push
// and a pull_request event whose workflows differ -- are two runs.
//
// There is deliberately no whole-run cache keyed by this, even though a
// run whose every job is a hit is a run that need not have started. The
// saving would be nothing: a plan key cannot be computed until the
// repository's workflows have been fetched and parsed (Service.Start
// does both before it plans), and that fetch is the expensive part of
// starting a run. Once the plan exists, answering it job by job costs a
// handful of file reads and acquires no sandbox, and it leaves the run
// with an id, a record and a row per job that says which of them were
// cached -- which is what somebody looking at a suspiciously fast green
// tick came to see, and what an answer of "here is an older run's id"
// would take away.
func planKey(req Request, jobs []PlannedJob) Key {
	keys := make([]string, 0, len(jobs))
	for _, j := range jobs {
		key := j.ID + "=" + string(j.Key)
		if req.Recheck && j.DependencyOnly {
			// A recheck reuses a dependency's cached pass and re-runs a
			// named job (Request.Recheck), so naming a job and only
			// needing it are two different requests.
			key += "~dependency"
		}
		keys = append(keys, key)
	}
	return HashOf("run", struct {
		Version  int      `json:"v"`
		Repo     string   `json:"repo"`
		Commit   string   `json:"commit"`
		Ref      string   `json:"ref,omitempty"`
		Event    Event    `json:"event"`
		Workflow string   `json:"workflow,omitempty"`
		Jobs     []string `json:"jobs"`
		// A recheck is not the same request as the run it is checking,
		// even when it plans the same jobs: it is asking for the work to
		// be done again, and Service.Start's live de-duplication would
		// otherwise hand it back the very run whose answer is in doubt.
		// omitempty keeps every ordinary run hashing exactly as it did.
		Recheck bool `json:"recheck,omitempty"`
		// A run whose caller named no event carries no release secrets,
		// so it is not the same run as an explicit manual dispatch of the
		// same jobs, and must not be handed back as one (or handed one).
		EventDefaulted bool `json:"event_defaulted,omitempty"`
	}{
		Version: fingerprintVersion,
		Repo:    req.Repo,
		Commit:  req.Commit,
		// The ref unconditionally, as the job keys now carry it: a run of
		// no jobs at all would otherwise be one run for every branch at a
		// commit, since it has no job keys to tell them apart.
		Ref:      req.Ref,
		Event:    req.Event,
		Workflow: req.Workflow,
		Jobs:     keys,
		Recheck:  req.Recheck,

		EventDefaulted: req.eventDefaulted,
	})
}

// HashOf is the one place a Key is made: a prefix naming what kind of
// thing was hashed (so a job key and a run key can never collide), the
// canonical JSON of the value, and SHA-256 of the pair.
func HashOf(kind string, v any) Key {
	body, err := json.Marshal(v)
	if err != nil {
		// Everything passed here is a plain struct of strings, ints,
		// bools, slices and maps of them. A marshal failure would be a
		// programming error in this file rather than anything a caller
		// did, and a key that silently became "" would be a cache
		// collision between every job in the deployment.
		panic(fmt.Sprintf("ci: fingerprinting %s: %v", kind, err))
	}
	sum := sha256.Sum256(append([]byte(kind+"\x00"), body...))
	return Key(kind + ":" + hex.EncodeToString(sum[:]))
}
