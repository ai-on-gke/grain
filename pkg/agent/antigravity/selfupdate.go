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

package antigravity

// agy updates its own binary, and this package does not let it.
//
// The Dockerfile fetches an immutable release artifact and checks its
// sha256, and says why: "Google publishes release hashes; keeping them
// beside the version makes a changed artifact fail the build instead of
// silently changing the agent runtime." docs/agy-surface.md is the other
// half of that bargain -- every fact this package rests on that lives
// nowhere but in the binary, read off *that* release, with its size and
// digest at the top of the file.
//
// Neither survives a binary that rewrites itself, and agy does. Measured
// on 2026-09-17 (the 2026-09-17 agy-self-update hand run, and first
// noticed as an aside in the 2026-09-17 agy-resume hand run §5): one
// dispatch-shaped exec of the pinned 1.1.27 -- the argv Framework.Run
// builds, the private HOME writeAgyHome writes, the turn on stdin --
// replaced the binary in place with 1.2.5 within seconds. 210,551,040
// bytes and sha256 93eb2118… became 217,284,864 and 84808e10…, byte for
// byte what antigravity.google/cli/install.sh serves. That installer says
// so itself, when it finds an agy already installed: "The Antigravity CLI
// automatically self-updates in the background during regular runs."
//
// So: no. A deployment runs the agy its image was built with until an
// image is built with another one, and the way that is held is this
// variable, in the environment of every agy this package starts.
//
// Two details, both measured, both easy to get wrong:
//
//   - the value has to be "true". With AGY_CLI_DISABLE_AUTO_UPDATE=1 the
//     binary replaced itself exactly as it does with the variable unset,
//     so agy compares it against a string rather than parsing it as a
//     boolean. Nothing documents that: 1.1.27's string table holds the
//     name once and nothing else about it, and docs/agy-surface.md's
//     flag list does not mention the updater at all. A release that
//     starts parsing the value properly will still honour "true"; one
//     that renames the variable will not, which is what the live probe
//     in live_selfupdate_stub_test.go is for.
//   - file permissions are not a substitute for it. Both images unpack
//     agy as root and `chmod -R a+rX` it, which stops a non-root process
//     from writing it -- and stops nothing at all when agy runs as root,
//     since root ignores the mode bits. The main Dockerfile declares no
//     USER of its own, and deploy/kubernetes/grain.yaml set no runAsUser
//     when this was measured, so a Kubernetes deployment's daemon was
//     root and its pin was being walked through on the first dispatch of
//     every fresh pod; the systemd unit scripts/setup.sh writes passes
//     --user with the host's grain uid, so a single-node deployment was
//     safe by accident rather than by decision. That manifest now runs
//     the daemon as uid 1000 (grain/task-209, and its securityContext
//     comment says why) -- which closes the same hole for the two other
//     pinned CLIs, and leaves this variable doing what no permission
//     inside an image ever could: covering a GRAIN_AGY_PATH binary
//     sitting writable in an operator's own home directory, and every
//     deployment shape nobody here gets to choose the uid of.
//
// The images set it as an ENV too, for the execs that do not come from
// this package at all: the build's own `agy --version` check, an
// operator's shell in the container, and whatever a task decides to run
// with the agy in the sandbox image.
//
// What this gives up is a fix arriving on its own: an agy release that
// repairs something a deployment is hitting now waits for a Dockerfile
// bump. That is what the pin was always asking for, and it is what makes
// docs/agy-surface.md, this package's live probes and every capability
// claim in it statements about the binary a deployment is actually
// running rather than about one it was built with.
const disableAgyAutoUpdate = "AGY_CLI_DISABLE_AUTO_UPDATE=true"

// agyEnv is the environment every agy this package starts is given: the
// private HOME it is to read its configuration out of, the updater above
// switched off, and the deployment's own Gemini key where there is one.
//
// It is *added* to the environment of whatever runs the process rather
// than substituted for it -- the contract agent.Command.Env and
// agent.TerminalCommand.Env both state -- so PATH and everything else the
// daemon was started with still reaches agy.
//
// GOOGLE_API_KEY is cleared beside the key rather than left as the
// controller's own environment has it: agy reads both and prefers
// GOOGLE_API_KEY when the two are set ("Warning: Both GOOGLE_API_KEY and
// GEMINI_API_KEY are set. Using GOOGLE_API_KEY."), so an unrelated key
// exported on the controller would quietly become the credential every
// run bills against instead of the one this deployment configured. An
// empty key is not an error here: a deployment may mean agy to
// authenticate from an ambient session of its own.
func agyEnv(home, apiKey string) []string {
	env := []string{"HOME=" + home, disableAgyAutoUpdate}
	if apiKey != "" {
		env = append(env, "GEMINI_API_KEY="+apiKey, "GOOGLE_API_KEY=")
	}
	return env
}
