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

package hostd

import "slices"

// Feature names one capability of the host agent contract
// (design/design/agentd-stable-contract.design.md, "Features"). Each side
// lists the features its build implements in Hello.Features; a peer
// checks for a feature with Hello.Has before it relies on it, and does
// without it -- degrading, never failing -- when the other side lacks it.
//
// The rules that keep a host agent installed today working with the
// daemons of the next years are the design doc's, and contract_test.go
// enforces the mechanical half of them:
//
//   - a feature, once released, is never renamed, never removed before its
//     deprecation window has passed, and never changes meaning;
//   - new behaviour a peer must know about before using is a new feature,
//     not a ProtocolVersion bump;
//   - every wire field and allowlist key belongs to exactly one feature
//     (contractFields), so adding one forces naming the feature it ships
//     under, and the golden contract (testdata/agentd-contract.golden)
//     and the design doc's inventory must change in the same commit.
type Feature string

// The features of host agent contract v1. The first six are what
// protocols 1 to 4 already promised, so an older peer that predates
// Hello.Features is credited with them from its protocol version
// (legacyFeatures). The rest cannot be inferred from a version: a peer
// that does not list them does not have them, whatever it speaks.
const (
	// FeatureStart: OpStart attached over the connection, OpList, stdin,
	// signals and the Exit frame (protocol 1).
	FeatureStart Feature = "start"
	// FeatureDetach: detached starts (Request.Detach) with an on-host
	// spool, OpProcesses, OpAttach from a Cursor, and OpCancel
	// (protocol 2).
	FeatureDetach Feature = "detach"
	// FeatureDrain: OpDrain, with Request.Idle asking for a cutover, and
	// Hello.Draining / Hello.Cutover (protocol 2).
	FeatureDrain Feature = "drain"
	// FeatureSocketOwner: OpSocketOwner (protocol 3).
	FeatureSocketOwner Feature = "socket_owner"
	// FeatureStage: OpStage, confined to Hello.StageRoot, over a session
	// the host dialed out on (protocol 4).
	FeatureStage Feature = "stage"
	// FeatureListen: OpListen and the OpCarry streams the host opens back
	// to the daemon (protocol 4).
	FeatureListen Feature = "listen"

	// FeatureRenew: the host renews its credential in-band (OpRenew) and
	// the daemon answers it.
	FeatureRenew Feature = "renew"
	// FeatureLogins: Hello.Logins is authoritative -- an empty list means
	// "no logins", where a peer without the feature means "not known".
	FeatureLogins Feature = "logins"
	// FeatureAgentsOnly: Hello.AgentsOnly is authoritative -- false means
	// the host serves capabilities too, where a peer without the feature
	// means "not known".
	FeatureAgentsOnly Feature = "agents_only"
	// FeatureFeatures: Hello.Features itself. Listed so that a peer's
	// silence about every other feature is a statement, not an omission.
	FeatureFeatures Feature = "features"
	// FeatureEnvOpen: an allowlist entry's env may include "*" to admit any
	// variable name outside EnvDeny (identity variables, LD_*, and the
	// interpreter/shell injection variables), path/paths_under/cwd_under
	// accept ~/ relative to hostd's home, and top-level and entry-level x-*
	// keys are ignored.
	FeatureEnvOpen Feature = "env_open"
	// FeatureLoginsMap: Allowlist.Logins configures CLI login paths,
	// Hello.LoginTargets reports the resolved regular-file targets for
	// StageSymlink, and StageSymlink accepts any target the map names.
	// Replaces Hello.CodexAuth.
	FeatureLoginsMap Feature = "logins_map"
	// FeatureHookPolicy: the host's agy-tool-hook and claude-stop-hook
	// subcommands accept -policy <path> and evaluate a staged per-run
	// hook policy file instead of a compiled-in tool roster or stop rule.
	FeatureHookPolicy Feature = "hook_policy"
)

// Features is every feature this build implements, in the order they
// were added. Append only: contract_test.go holds it to the golden file.
var Features = []Feature{
	FeatureStart,
	FeatureDetach,
	FeatureDrain,
	FeatureSocketOwner,
	FeatureStage,
	FeatureListen,
	FeatureRenew,
	FeatureLogins,
	FeatureAgentsOnly,
	FeatureFeatures,
	FeatureEnvOpen,
	FeatureLoginsMap,
	FeatureHookPolicy,
}

// legacyFeatures is what a peer that predates Hello.Features is credited
// with, from the protocol version it speaks: exactly what that version's
// Supports* gate promised.
func legacyFeatures(protocol int) []Feature {
	var out []Feature
	if protocol >= 1 {
		out = append(out, FeatureStart)
	}
	if protocol >= 2 {
		out = append(out, FeatureDetach, FeatureDrain)
	}
	if protocol >= 3 {
		out = append(out, FeatureSocketOwner)
	}
	if protocol >= 4 {
		out = append(out, FeatureStage, FeatureListen)
	}
	return out
}

// Has reports whether the peer that said h implements f: from its
// Features when it lists any, else from its protocol version. A feature
// the peer does not know is simply absent; it is never an error.
func (h Hello) Has(f Feature) bool {
	if len(h.Features) > 0 {
		return slices.Contains(h.Features, f)
	}
	return slices.Contains(legacyFeatures(h.Protocol), f)
}
