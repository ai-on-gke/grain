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
// request from a fork names as its head. Typed as the Base branch of a
// task that does not create a branch of its own, the server files the
// task against the open pull request it heads (pkg/forkhead). This is
// classic's ui/src/forkHead.js, so both sheets explain a well-formed
// label and refuse a malformed one before it is sent, in the server's
// own words.

export type ForkHead =
  { owner: string; branch: string; error?: undefined } | { error: string };

// SHAPE is the text a fork head is read from at all: branch-name
// characters around at least one ':'. Anything else is an ordinary
// branch name and none of this file's business.
const SHAPE = /^[A-Za-z0-9._/:-]*:[A-Za-z0-9._/:-]*$/;
const LOGIN = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$/;
const BRANCH = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/;

function validBranchName(name: string): boolean {
  return (
    BRANCH.test(name) &&
    !name.includes("..") &&
    !name.includes("//") &&
    !name.endsWith("/")
  );
}

// parseForkHead reads text as a fork head label: null when it is not
// shaped like one, the owner and branch when it is a good one, and the
// server's refusal when it is shaped like one but malformed.
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

// forkHeadHelp is the line under the box for a well-formed label.
export function forkHeadHelp(
  head: { owner: string; branch: string },
  repo: string,
): string {
  const on = repo ? ` on ${repo}` : "";
  return (
    `GitHub fork head: branch ${head.branch} on ${head.owner}'s fork. ` +
    `grain files this task against the open pull request${on} it heads.`
  );
}
