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

import net from "node:net";
import { describe, expect, it } from "vitest";
import {
  FREE_PORT_MAX,
  FREE_PORT_MIN,
  freePort,
  withFreePort,
} from "@grain/ui-shared/e2e/freeport.js";

describe("e2e freeport helper", () => {
  it("reserves unique ports outside the OS ephemeral range across back-to-back calls", async () => {
    const ports = await Promise.all([
      freePort(),
      freePort(),
      freePort(),
      freePort(),
    ]);
    expect(new Set(ports).size).toBe(4);
    for (const port of ports) {
      expect(port).toBeGreaterThanOrEqual(FREE_PORT_MIN);
      expect(port).toBeLessThanOrEqual(FREE_PORT_MAX);
      expect(port).toBeLessThan(32_768);
    }
  });

  it("skips a port already bound on loopback and remembers claimed ports", async () => {
    const occupied = net.createServer();
    await new Promise((resolve, reject) => {
      occupied.once("error", reject);
      occupied.listen(0, "127.0.0.1", resolve);
    });
    const { port } = occupied.address();
    try {
      await expect(freePort({ min: port, max: port })).rejects.toThrow(
        /no free loopback port/,
      );
    } finally {
      await new Promise((resolve) => occupied.close(resolve));
    }
    expect(await freePort({ min: port, max: port })).toBe(port);
    await expect(freePort({ min: port, max: port })).rejects.toThrow(
      /no free loopback port/,
    );
  });

  it("withFreePort retries EADDRINUSE and surfaces non-bind failures at once", async () => {
    let calls = 0;
    const seen = [];
    const { addr, value } = await withFreePort(async (candidate) => {
      calls += 1;
      seen.push(candidate);
      if (calls < 3) {
        throw new Error(
          `listen tcp ${candidate}: bind: address already in use`,
        );
      }
      return "started";
    });
    expect(calls).toBe(3);
    expect(new Set(seen).size).toBe(3);
    expect(addr).toBe(seen[2]);
    expect(value).toBe("started");

    let fatalCalls = 0;
    await expect(
      withFreePort(async () => {
        fatalCalls += 1;
        throw new Error("missing flag");
      }),
    ).rejects.toThrow("missing flag");
    expect(fatalCalls).toBe(1);
  });
});
