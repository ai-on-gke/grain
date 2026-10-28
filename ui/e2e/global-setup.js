import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { newAccessCode } from "./accesscode.js";

// Builds the one `grain` binary every worker's own demo server runs
// (fixtures.js), once, before any worker starts.
//
// It used to be playwright.config.js's `webServer` that compiled this,
// as `go run ../cmd/grain demo` -- fine for the single shared server
// that was, and the wrong shape now there is one per worker. `go run`
// compiles and links before it execs, so N workers reaching for it at
// once means N processes doing the same cold build concurrently on the
// same handful of cores, each racing the fixture's own startup timeout.
// Building here instead costs the suite one link, in a phase that has
// nothing else to do, and leaves each worker with a binary to exec in a
// few milliseconds.
//
// This needs ../pkg/ui/static to already hold a built frontend, exactly
// as `go run` did -- go:embed compiles in whatever is there, so a
// `grain demo` built over the placeholder serves the placeholder. The
// Makefile's test-e2e targets depend on `frontend` for that reason.
export default async function globalSetup() {
  const root = fileURLToPath(new URL("../..", import.meta.url));
  // A fresh temp directory rather than a fixed path in the tree: two
  // runs of this suite on one machine -- a developer's, while CI is not
  // the only thing that ever runs it -- must not have the second one
  // rewriting the binary the first one's workers are executing.
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-"));
  const bin = path.join(dir, "grain");
  execFileSync(
    "go",
    [
      "build",
      // grain_e2e compiles in the local-directory sandbox backend
      // (pkg/daemon's grain_e2e seam) that config.spec.js's daemon
      // dispatches into: no deployment build has one, and this suite
      // owns no container runtime to give it a real backend instead.
      "-tags",
      "grain_e2e",
      "-ldflags",
      "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0",
      "-o",
      bin,
      "./cmd/grain",
    ],
    {
      cwd: root,
      stdio: "inherit",
    },
  );
  // The Director journey needs a running orchestrator around the same
  // server, not demo's static rows. Its small helper is built once here
  // for the same reason as grain itself; only director.spec.js starts it.
  const directorBin = path.join(dir, "grain-director-e2e");
  execFileSync("go", ["build", "-o", directorBin, "./ui/e2e/directorserver"], {
    cwd: root,
    stdio: "inherit",
  });
  // The console resting-note walkthrough needs a session the orchestrator
  // actually parks, which demo's static rows never are.
  const consoleRestBin = path.join(dir, "grain-console-rest-e2e");
  execFileSync(
    "go",
    ["build", "-o", consoleRestBin, "./ui/e2e/consolerestserver"],
    {
      cwd: root,
      stdio: "inherit",
    },
  );
  // The deployment-default journey restarts a real server around one
  // persistent SQLite directory and drives this same grain CLI against it.
  const defaultsBin = path.join(dir, "grain-defaults-e2e");
  execFileSync("go", ["build", "-o", defaultsBin, "./ui/e2e/defaultsserver"], {
    cwd: root,
    stdio: "inherit",
  });
  // The release helper simulates external-boundary release transitions.
  const releaseHelperBin = path.join(dir, "grain-release-helper-e2e");
  execFileSync(
    "go",
    ["build", "-o", releaseHelperBin, "./ui/e2e/releasehelper"],
    {
      cwd: root,
      stdio: "inherit",
    },
  );
  // Workers are forked from this process after global setup returns, so
  // they inherit all binaries; global-teardown.js reads the directory to
  // clean binaries up.
  process.env.GRAIN_E2E_DEMO_BIN = bin;
  // One access code for every server the run starts -- each worker's
  // demo, and the daemons and fixture servers specs start of their own
  // -- so the access session a worker signs in for once (fixtures.js) is
  // good on all of them: the cookie is 127.0.0.1's, whatever the port,
  // and is signed with the code alone.
  process.env.GRAIN_E2E_ACCESS_CODE = newAccessCode();
  process.env.GRAIN_E2E_DIRECTOR_BIN = directorBin;
  process.env.GRAIN_E2E_CONSOLE_REST_BIN = consoleRestBin;
  process.env.GRAIN_E2E_DEFAULTS_BIN = defaultsBin;
  process.env.GRAIN_E2E_RELEASE_HELPER_BIN = releaseHelperBin;
  process.env.GRAIN_E2E_DEMO_BIN_DIR = dir;
}
