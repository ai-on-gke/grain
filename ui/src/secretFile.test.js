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

import { describe, expect, it } from "vitest";
import readSecretFile, { maxSecretFileBytes } from "./secretFile.js";

// The PEM shape is the whole point of the feature: what comes back has to
// be byte-for-byte what was on disk, line breaks and all, because a value
// whose line breaks were lost is exactly what pasting one produces and
// what nothing notices until a JWT fails to sign months later.
const pem = [
  "-----BEGIN RSA PRIVATE KEY-----",
  "MIIEowIBAAKCAQEAy8Dbv8prpJ/0kKhlGeJYozo2t60EG8L0561g13R29LvMR5hy",
  "vGZlGJpmn65+A4xHXInJYiPuKzrKUnApeLZ+vw1HocOAZtWK0z3r26uA8kQYOKX9",
  "-----END RSA PRIVATE KEY-----",
  "",
].join("\n");

describe("readSecretFile", () => {
  it("reads a key file's text exactly, line breaks and all", async () => {
    const file = new File([pem], "grain.2026-09-07.private-key.pem");
    await expect(readSecretFile(file)).resolves.toBe(pem);
  });

  it("refuses a file too big to be a credential", async () => {
    const file = new File(["x".repeat(maxSecretFileBytes + 1)], "backup.zip");
    await expect(readSecretFile(file)).rejects.toThrow(/too big/);
  });

  it("refuses an empty file rather than storing nothing", async () => {
    const file = new File(["  \n"], "empty.pem");
    await expect(readSecretFile(file)).rejects.toThrow(/is empty/);
  });

  // A .p12 or a stray image would otherwise be stored verbatim and fail
  // wherever it was finally resolved, with nothing pointing back here.
  it("refuses a file that is not text", async () => {
    const file = new File(["MII\0\0key"], "key.p12");
    await expect(readSecretFile(file)).rejects.toThrow(/does not look like/);
  });
});
