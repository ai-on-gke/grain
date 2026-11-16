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

// The checks the server makes of Tasks -> Branch name template
// (model.FormatTaskBranchName and model.ValidBranchName, pkg/model), so
// the box refuses a template by its label before Save rather than after
// a 400 that names the JSON key.

const MAX_BRANCH_NAME_LEN = 244;
const BRANCH_NAME_SAFE = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/;

// sampleBranchName is the branch the template gives task 42 of this
// deployment, with {slug} left out.
export function sampleBranchName(
  template: string,
  deploymentId: string,
): string {
  const dep = deploymentId.trim() || "deployment";
  return template
    .replace(/-\{slug\}$/, "")
    .replaceAll("{instance}", dep)
    .replaceAll("{deployment}", dep)
    .replaceAll("{id}", "42");
}

export function validBranchName(name: string): boolean {
  if (name.length > MAX_BRANCH_NAME_LEN || !BRANCH_NAME_SAFE.test(name)) {
    return false;
  }
  if (
    name.includes("..") ||
    name.includes("//") ||
    name.endsWith("/") ||
    name.endsWith(".")
  ) {
    return false;
  }
  return name
    .split("/")
    .every((part) => !part.startsWith(".") && !part.endsWith(".lock"));
}

// branchTemplateError is why the server would refuse template, or null.
// An empty template is grain's default and always fine.
export function branchTemplateError(
  template: string,
  deploymentId: string,
): string | null {
  const label = "Branch name template";
  if (template === "") return null;
  if (!template.includes("{id}")) {
    return `${label} must contain {id}, so every task gets its own branch.`;
  }
  const slugs = template.split("{slug}").length - 1;
  if (slugs > 1) return `${label} may contain {slug} only once.`;
  if (slugs === 1) {
    if (!template.endsWith("{id}-{slug}")) {
      return `${label} may only end in {slug}, straight after {id} and a hyphen (as in grain/{deployment}/task-{id}-{slug}).`;
    }
    const last = template.slice(template.lastIndexOf("/") + 1);
    if (last.includes("{deployment}") || last.includes("{instance}")) {
      return `${label} must keep {deployment} and {instance} out of the part that ends in {slug}.`;
    }
  }
  const name = sampleBranchName(template, deploymentId);
  if (/[{}]/.test(name)) {
    return `${label} has an unknown placeholder: use {id}, {deployment}, {instance} or a trailing -{slug}.`;
  }
  if (!validBranchName(name)) {
    if (name.length > MAX_BRANCH_NAME_LEN) {
      return `${label} gives a ${name.length}-character branch name, over the ${MAX_BRANCH_NAME_LEN}-character limit.`;
    }
    return `${label} gives "${name}", which is not a valid git branch name.`;
  }
  return null;
}
