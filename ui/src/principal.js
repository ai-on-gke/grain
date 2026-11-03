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

// How the UI names whoever did something (operator-attribution spec §4).
// The server computes every label (ui.PrincipalLabel), so the UI, the CLI
// and MCP all say the same thing; these helpers only pick it out of a
// response, and fall back to the raw id for a daemon too old to send one.

// whoamiParts splits GET /api/whoami's actor label into the name the user
// tile shows and the muted qualifier beside it: "operator" and
// "(access code)" for the shared access code, "operator" and "(local)"
// over loopback, and the whole email with no qualifier for a verified
// sign-in (spec §5.4).
export function whoamiParts(whoami) {
  const label = String(whoami?.actor?.label || whoami?.actor?.id || "");
  if (!label) return { name: "", qualifier: "" };
  if (!whoami?.verified) {
    const m = label.match(/^(.*\S)\s+(\([^()]+\))$/);
    if (m) return { name: m[1], qualifier: m[2] };
  }
  return { name: label, qualifier: "" };
}

// commentAuthor is how a comment's author reads: its authorLabel, which
// already names a verified on-behalf-of ("session 7 for alice@…"), plus
// whose words grain relayed when the label leaves them out (an agent's
// question relayed by grain reads "grain on behalf of task 42").
export function commentAuthor(c) {
  let who = c?.authorLabel || c?.author || "";
  const behalf = c?.authorLabel
    ? c?.onBehalfOfLabel || c?.onBehalfOf
    : c?.onBehalfOf;
  if (behalf && !who.endsWith(` for ${behalf}`)) {
    who = `${who} on behalf of ${behalf}`;
  }
  return who;
}

// transitionActor is who asked for a timeline step (a close, a reopen):
// its actorLabel, or the older "by" string.
export function transitionActor(tr) {
  return tr?.actorLabel || tr?.byLabel || tr?.by || "";
}
