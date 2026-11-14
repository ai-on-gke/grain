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

// A GitHub fork head label, "<fork-owner>:<branch>" -- what a pull
// request's page shows as its head, and what people paste into a task's
// branch. The server resolves it to the open pull request it heads
// (pkg/forkhead). This is ui/src/forkHead.js ported, and so the twin of
// model.ParseForkHead: the form says what a label means, or what is
// wrong with it, before submit, in the server's own words.

// Shaped like an attempt at a label: a ':' among nothing but the
// characters of a login and a branch. Anything else is left to the plain
// branch-name check.
const SHAPE = /^[A-Za-z0-9._/:-]*:[A-Za-z0-9._/:-]*$/;
const LOGIN = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$/;
const BRANCH = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/;

export type ForkHead = { owner: string; branch: string } | { error: string };

function validBranchName(name: string): boolean {
  return (
    BRANCH.test(name) &&
    !name.includes("..") &&
    !name.includes("//") &&
    !name.endsWith("/")
  );
}

// parseForkHead returns null for anything that is not shaped like a label
// (a plain branch, which stays as typed), {owner, branch} for a
// well-formed one, and {error} for a malformed one.
export function parseForkHead(
  text: string | null | undefined,
): ForkHead | null {
  const s = (text || "").trim();
  if (!SHAPE.test(s)) return null;
  const i = s.indexOf(":");
  const owner = s.slice(0, i);
  const branch = s.slice(i + 1);
  if (branch.includes(":")) {
    return {
      error: `"${s}" has more than one ':': a fork head is <owner>:<branch>`,
    };
  }
  if (owner === "" || branch === "") {
    return {
      error: `"${s}" is not a fork head: it needs both an owner and a branch, as <owner>:<branch>`,
    };
  }
  if (!LOGIN.test(owner) || owner.includes("--")) {
    return {
      error: `"${s}" is not a fork head: "${owner}" is not a GitHub login`,
    };
  }
  if (!validBranchName(branch)) {
    return {
      error: `"${s}" is not a fork head: "${branch}" is not a usable branch name`,
    };
  }
  return { owner, branch };
}

// forkHeadHelp is what a branch box says about a well-formed label.
export function forkHeadHelp(
  { owner, branch }: { owner: string; branch: string },
  repo?: string,
): string {
  const on = repo ? ` on ${repo}` : "";
  return (
    `GitHub fork head: branch ${branch} on ${owner}'s fork. ` +
    `grain files this task against the open pull request${on} it heads.`
  );
}
