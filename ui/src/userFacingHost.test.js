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

// "hostd" is an implementation detail: the UI calls the machines grain
// runs host work on hosts, and the process on them the host agent. This
// scans every string a page can show -- JSX text, and string and template
// literals that read as prose -- for the word, so a new label, banner or
// error that says hostd fails here rather than reaching a user.
//
// Identifiers stay hostd (JSON fields like hostd and hostdFallback, API
// paths, test ids, a capability server's needs-hostd:<name> state), and
// so do the names of the binary, unit and config file a user has to type
// (grain-hostd, /etc/grain/hostd.yaml, -hostd-token-file): ALLOWED strips
// those before the word is looked for.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "@babel/parser";
import { describe, expect, it } from "vitest";

const SRC = dirname(fileURLToPath(import.meta.url));

// ALLOWED are the spellings a user may still be shown: the host agent's
// binary, unit, state and config paths, the daemon flags that name it,
// and wire names that only appear inside a code span or a URL.
const ALLOWED = [
  /\/run\/grain-hostd[\w./-]*/g, // the socket
  /\/usr\/local\/lib\/grain\/hostd[\w./-]*/g, // the installed copies
  /\/etc\/grain\/hostd\.yaml(\.default)?/g, // the allowlist
  /grain-hostd[\w.-]*/g, // binary, unit, install script, state dir
  /-hostd-[a-z-]+/g, // daemon flags (-hostd-token-file, -hostd-socket)
  /GRAIN_HOSTD[A-Z_]*/g, // installer and daemon environment
  /\/api\/hostds?[\w/{}.-]*/g, // API paths
  /\/hostd\/install\b/g, // the shared --join snippet's installer URL
  /needs-hostd:?/g, // a capability server's state, on the wire
  /hostdFallback/g, // a JSON field
];

const WORD = /\bhostds?\b/i;

// prose is whether a literal is text a person reads rather than an
// identifier, a key or a path: it has a space in it.
const prose = (s) => /\s/.test(s.trim());

function sourceFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      out.push(...sourceFiles(path));
    } else if (/\.(jsx?|tsx?)$/.test(name) && !/\.test\.|\.spec\./.test(name)) {
      out.push(path);
    }
  }
  return out;
}

// shownStrings walks file's AST and returns every JSX text and prose
// literal, with its line.
function shownStrings(file) {
  const ast = parse(readFileSync(file, "utf8"), {
    sourceType: "module",
    plugins: ["jsx"],
    errorRecovery: true,
  });
  const out = [];
  const visit = (node, parentKey) => {
    if (!node || typeof node.type !== "string") return;
    // Import sources, object keys and test ids are identifiers.
    if (node.type === "ImportDeclaration") return;
    if (node.type === "JSXAttribute") {
      const name = node.name?.name;
      if (
        name === "data-testid" ||
        name === "testId" ||
        name === "testid" ||
        name === "className" ||
        name === "anchor" ||
        name === "key"
      )
        return;
    }
    if (node.type === "JSXText" && node.value.trim()) {
      out.push({ text: node.value, line: node.loc.start.line });
    }
    if (
      node.type === "StringLiteral" &&
      parentKey !== "key" &&
      prose(node.value)
    ) {
      out.push({ text: node.value, line: node.loc.start.line });
    }
    if (node.type === "TemplateElement" && prose(node.value.cooked ?? "")) {
      out.push({ text: node.value.cooked, line: node.loc.start.line });
    }
    for (const [key, child] of Object.entries(node)) {
      if (
        key === "loc" ||
        key === "leadingComments" ||
        key === "trailingComments"
      )
        continue;
      if (Array.isArray(child)) child.forEach((c) => visit(c, key));
      else if (child && typeof child.type === "string") visit(child, key);
    }
  };
  visit(ast.program, "program");
  return out;
}

function saysHostd(text) {
  let rest = text;
  for (const re of ALLOWED) rest = rest.replace(re, "");
  return WORD.test(rest);
}

describe("user-facing text says host, not hostd", () => {
  it("allows the binary, unit and config names, and nothing else", () => {
    expect(saysHostd("sudo systemctl restart grain-hostd.service")).toBe(false);
    expect(
      saysHostd("paste it under capabilities: in /etc/grain/hostd.yaml"),
    ).toBe(false);
    expect(saysHostd("this daemon's -hostd-token-file")).toBe(false);
    expect(saysHostd("ran on hostd ws-a")).toBe(true);
    expect(saysHostd("Hostds")).toBe(true);
    expect(saysHostd("grain hostd is up")).toBe(true);
  });

  it("finds no hostd in any string a page shows", () => {
    const found = [];
    for (const file of sourceFiles(SRC)) {
      for (const { text, line } of shownStrings(file)) {
        if (saysHostd(text)) {
          found.push(
            `${relative(SRC, file)}:${line}: ${text.trim().replace(/\s+/g, " ")}`,
          );
        }
      }
    }
    expect(found).toEqual([]);
  });
});
