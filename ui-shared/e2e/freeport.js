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

// Non-ephemeral unprivileged loopback port reservation for the few specs that
// cannot let the child pick `:0` itself -- either because a replacement daemon
// has to come back on the exact origin the browser already holds open across a
// restart, or because several helper processes cross-reference one another's
// addresses on the command line before any of them binds.
//
// Specs that only need one isolated server should pass `-addr 127.0.0.1:0` (or
// `-ui-addr 127.0.0.1:0`) and read the bound port from the child's startup log
// instead, as ui-shared/e2e/demo.js does.
//
// Why 20000-31999 rather than listen(0):
//   - listen(0) + close() hands back a port from the OS ephemeral range
//     (32768-60999 on Linux, 49152-65535 on macOS and Windows) -- the very pool
//     every other Playwright worker's `:0` server and outbound loopback fetch()
//     draws from, both before the first child binds and while a stopped daemon's
//     port sits unbound across a restart assertion.
//   - 20000-31999 sits above grain's 42xx convention and 9728 sandbox block and
//     strictly below those OS ephemeral ranges, so kernel `:0` binds and client
//     connections never land on it.
//   - `claimed` keeps back-to-back calls inside one worker (before any child
//     binds) from handing out the same port twice, and a random starting offset
//     keeps parallel workers off the same candidate; `withFreePort` retries the
//     initial start if another process still wins the bind in between.
export const FREE_PORT_MIN = 20_000;
export const FREE_PORT_MAX = 31_999;
const PORT_SPAN = FREE_PORT_MAX - FREE_PORT_MIN + 1;

const claimed = new Set();

export function tryBindLoopback(port) {
  return new Promise((resolve) => {
    const srv = net.createServer();
    srv.unref();
    srv.once("error", () => resolve(false));
    srv.listen(port, "127.0.0.1", () => {
      srv.close(() => resolve(true));
    });
  });
}

export async function freePort({
  min = FREE_PORT_MIN,
  max = FREE_PORT_MAX,
} = {}) {
  const span = max - min + 1;
  const start = Math.floor(Math.random() * span);
  for (let i = 0; i < span; i++) {
    const port = min + ((start + i) % span);
    if (claimed.has(port)) continue;
    if (await tryBindLoopback(port)) {
      claimed.add(port);
      return port;
    }
  }
  throw new Error(`no free loopback port found in ${min}-${max}`);
}

export async function withFreePort(startFn, attempts = 5) {
  for (let attempt = 1; ; attempt++) {
    const addr = `127.0.0.1:${await freePort()}`;
    try {
      const value = await startFn(addr);
      return { addr, value };
    } catch (err) {
      if (
        attempt >= attempts ||
        !/address already in use/i.test(String(err?.message ?? err))
      ) {
        throw err;
      }
    }
  }
}
