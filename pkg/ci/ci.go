// Package ci is grain's own CI: the workflows declared for a
// repository, and the engine that runs them against one repo and one
// branch, each job in a sandbox of its own, as many at a time as the
// deployment allows.
//
// A workflow is declared in one of two places, and the engine cannot
// tell which. A repository grain works in may carry Grain's strict native
// format in `.grain/workflows`, or use its existing GitHub Actions files
// from `.github/workflows` when that Grain directory declares no
// workflow of its own -- whether because it is absent or because nothing
// in it is a workflow file. A
// repository grain does *not* control cannot -- a grain-specific CI
// directory in somebody else's tree is a pull request they have to
// accept -- so for those the same files are declared on grain's side,
// against the repo (model.RepoWorkflow), and read back through the same
// Source interface. Layered composes the two where a repository has
// both. See source.go for all three.
//
// # Native Grain workflows
//
// The native format looks like GitHub Actions because everybody who will
// ever write one of these files has already
// written the other kind. The vocabulary here is GitHub's wherever
// GitHub has a word for the thing -- `name`, `on`, `jobs`, `needs`,
// `steps`, `run`, `env`, `runs-on`, `timeout-minutes`,
// `working-directory`, `continue-on-error` -- spelled the same way,
// nested the same way, meaning the same thing. What grain does not
// implement it *rejects*, loudly, naming the file and the key (Parse,
// over a decoder with KnownFields set): a format that quietly ignores
// half of what it was handed is worse than one that never claimed the
// resemblance, because `uses: actions/checkout@v4` silently doing
// nothing is a green tick over a job that never checked anything out.
//
// The deliberate divergences, each of which is a thing grain has and
// GitHub does not, or the reverse:
//
//   - **There is no `uses:`.** grain has no action marketplace, no
//     runner image catalogue and no intention of growing either. A step
//     is a shell script. The one `uses:` every workflow starts with --
//     `actions/checkout` -- is not a step here at all: a job's sandbox
//     arrives with the repository already checked out at the commit
//     being tested, because that is what grain's sandboxes already do
//     for an agent run (see Sandboxes, and pkg/orchestrator/checkout.go
//     for the machinery that does it).
//   - **`runs-on` names a sandbox, not a runner pool.** grain has one
//     kind of machine to offer -- its own sandbox (RunnerLabel) -- and
//     the interesting question is not which OS image but how big:
//     `sandbox: {cpus, memory-mb, disk-gb}`, the same three dimensions
//     model.Config and orchestrator.Shape already carry.
//   - **No `secrets` context.** Jobs receive only the capabilities an
//     operator explicitly configured for this repo's CI runs.
//   - **No `${{ }}` expressions.** They are the part of Actions that
//     turns a config file into a language, and nothing here needs one
//     yet. Variables a step can read are declared with `env:` and read
//     with the shell's own `$VAR`.
//   - **Artifacts are a key on the job, not a step.** GitHub keeps a
//     job's files with `uses: actions/upload-artifact` under an `if:
//     failure()`; there is no `uses:` and no `if:` here, and a step
//     could not do it in any case -- a grain sandbox is destroyed the
//     moment its job ends, so the copy has to be grain's. A job says
//     `artifacts:` with that action's own `name` and `path`, and grain
//     collects those paths out of the sandbox for a job that did not
//     succeed. See artifacts.go for the whole of it, including why it is
//     failure-only and what bounds what is kept.
//   - **`paths:` and `paths-ignore:` are keys on the job, not on the
//     trigger.** GitHub spells them under `on:`, where they decide
//     whether a workflow runs at all. Here they are on the job -- the
//     unit that gets a sandbox and the unit that is cached -- and they
//     decide something stronger than whether to run: a job that declares
//     which files it reads is *keyed* on the content of those files
//     rather than on the commit, so a commit touching nothing it reads
//     is a cache hit and the job does not run. Same globs as GitHub,
//     same `!` negation, same evaluation order, and the same refusal to
//     take both keys in one job. paths.go is the whole of what declaring
//     one promises and what defends it; read it before adding one.
//   - **A step may say it is not testing the tree.**
//     `infrastructure: true` on a step (Step.Infrastructure) marks it as
//     the one that fetches the environment the job's other steps need --
//     an image out of a content-addressed cache, a pinned tool, a base
//     layer, a rebuild of something nothing published. GitHub has no word
//     for this and does not need one, because on GitHub the runner image
//     is somebody else's problem; here a job's environment can be a
//     gigabyte that a cache was supposed to hold, and the difference
//     between "the cache was cold" and "this branch is broken" has to be
//     sayable. A step so declared is off the job's `timeout-minutes`
//     clock, and its failure concludes the job ConclusionErrored rather
//     than ConclusionFailure -- which is to say that a cache miss in
//     grain costs time and can never cost a verdict.
//
// # GitHub Actions compatibility
//
// When `.grain/workflows` does not exist, LoadDir reads
// `.github/workflows`. Grain parses only the workflow metadata it needs
// to plan sandboxes, then runs each top-level job with a pinned act binary
// inside Grain's existing VM. act owns actions, expressions, conditions and
// matrices; Grain still owns checkout authority, resources, capabilities,
// isolation and the verdict.
//
// The first compatibility surface accepts ubuntu-latest jobs with steps.
// It rejects reusable-workflow jobs and `needs`, because independently
// sandboxed act jobs cannot yet preserve outputs or artifacts between jobs.
// A Grain task branch is a push from Grain's point of view and a
// pull_request run from an Actions workflow's point of view; planning makes
// that translation when no push trigger matches the branch.
//
// An Actions job is cached like a native one: `cache: true` by default,
// `cache: false` to opt a job out, and the same Fingerprint over the same
// inputs. It was not, at first, on the argument that an act run is less
// hermetic than a native job -- it may manage caches of its own and have
// side effects grain cannot infer from the file. That argument is
// declined rather than forgotten: a native job also runs arbitrary steps
// that reach the network, so the promise a key makes is no weaker in kind
// here, and "less hermetic" is what `cache: false` is for in either
// format. What it cost was measurable -- a repository whose whole CI is
// `.github/workflows` could never hit the cache at all, and re-ran every
// job of an unchanged commit in full.
//
// # What a workflow file is trusted with
//
// Workflow files cannot grant capabilities. Service reads the explicit set
// from operator-owned repo settings, overwrites caller-supplied grants, and
// includes the set in job fingerprints. cmd/grain materializes those grants
// in each job's sandbox and revokes their leases during teardown. Agent task
// defaults are not inherited. An empty CI set gives only a read-only checkout;
// pkg/cli/ci_hostile_test.go exercises that default with a hostile workflow.
//
// A workflow declared on grain's side rather than in the repository is
// *operator* config -- written through grain's own settings surface,
// proposed in a pull request against the state repository -- and an
// in-repo one is not. That difference is real and is the reason the
// second kind of declaration exists at all. It is deliberately not a
// difference in what a workflow may do: both go through the same
// Parse, produce the same Job, and receive the same explicitly configured
// capabilities. Neither form of workflow can widen its own grants.
//
// # One commit is tested once
//
// The point of CI on a task queue is that the same commit is tested
// once, and every decision here is made against a key a result can be
// looked up by. Fingerprint hashes the repository, the commit, the
// job's own declaration, the shape of the sandbox it asked for and the
// environment grain itself supplies into one Key; the engine consults
// Cache before it acquires a sandbox and writes back to it after; every
// JobRun carries the key it was produced under and says whether it was
// remembered rather than run (JobRun.Cached).
//
// DirCache is that cache, as files on the daemon's disk -- deliberately
// not a table in model.Store, because an entry holds a job's whole log
// and everything in the store is committed to the state repository as
// text. Its bounds are about disk rather than about correctness: a key
// is a promise that the work is identical, so there is nothing here to
// invalidate. What keeps that promise honest is fingerprintVersion --
// anything that changes what a job does has to be in the key or in that
// number, or a stale hit is a green tick over code nobody tested.
package ci
