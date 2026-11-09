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

// A switchable HTTP reverse proxy for docs/test-plans/2026-11-09.md §5
// item 2, in front of a real grain daemon/demo. Its mode is changed at
// runtime with `POST /_proxy/mode {"mode": ..., "delayMs": ...}`:
//
//   forward  pass every request through
//   stall    accept the request and never answer it (no status, no bytes)
//   headers  answer 200 + JSON headers and one byte of body, then stall
//   delay    pass through, but only after delayMs (a slow-but-answering
//            daemon)
//   recover  pass new requests through, but keep holding the ones stall
//            or headers took (a proxy whose old backend connections are
//            half-open and never time out, while new ones work)
//
// With -prefix /grain it also serves the UI under a sub-path: a request
// for /grain/<rest> is forwarded as /<rest> (the way a path-prefix
// reverse proxy mounts an app), and anything outside the prefix is
// refused 404 unless -passthrough is given.
//
// Only /api/* requests are affected by stall/headers/delay unless
// -all is given, so a page can still load its assets.
//
//   node proxy.mjs -listen 4291 -upstream 127.0.0.1:4290 [-prefix /grain] [-passthrough] [-all]
import http from "node:http";

const args = process.argv.slice(2);
const flag = (name) => args.includes(name);
const opt = (name, dflt) => {
  const at = args.indexOf(name);
  return at >= 0 ? args[at + 1] : dflt;
};
const listen = Number(opt("-listen", "4291"));
const [upHost, upPort] = opt("-upstream", "127.0.0.1:4290").split(":");
const prefix = opt("-prefix", "");
const passthrough = flag("-passthrough");
const all = flag("-all");

let mode = "forward";
let delayMs = 0;
const held = new Set();
const log = [];

const server = http.createServer((req, res) => {
  if (req.url === "/_proxy/mode" && req.method === "POST") {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const next = JSON.parse(body || "{}");
      mode = next.mode || mode;
      delayMs = Number(next.delayMs ?? delayMs);
      if (mode === "forward" || mode === "delay") {
        // Let go of everything held so the browser sees its sockets close.
        for (const r of held) r.destroy();
        held.clear();
      }
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ mode, delayMs, held: held.size }));
    });
    return;
  }
  if (req.url === "/_proxy/log") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ mode, held: held.size, log: log.slice(-500) }));
    return;
  }
  let path = req.url;
  if (prefix) {
    if (path === prefix || path.startsWith(`${prefix}/`)) {
      path = path.slice(prefix.length) || "/";
    } else if (!passthrough) {
      log.push({ at: Date.now(), path: req.url, refused: 404 });
      res.writeHead(404, { "Content-Type": "text/plain" });
      res.end(`outside ${prefix}\n`);
      return;
    }
  }
  const affected = all || path.startsWith("/api/");
  log.push({ at: Date.now(), method: req.method, path, mode: affected ? mode : "forward" });
  if (affected && mode === "stall") {
    held.add(res);
    res.on("close", () => held.delete(res));
    return;
  }
  if (affected && mode === "headers") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.write("{");
    held.add(res);
    res.on("close", () => held.delete(res));
    return;
  }
  const go = () => {
    const headers = { ...req.headers, "x-forwarded-prefix": prefix || undefined };
    if (!prefix) delete headers["x-forwarded-prefix"];
    const up = http.request(
      { host: upHost, port: upPort, method: req.method, path, headers },
      (upRes) => {
        res.writeHead(upRes.statusCode, upRes.headers);
        upRes.pipe(res);
      },
    );
    up.on("error", (err) => {
      res.writeHead(502, { "Content-Type": "text/plain" });
      res.end(String(err));
    });
    req.pipe(up);
  };
  if (affected && mode === "delay") setTimeout(go, delayMs);
  // recover falls through to go(): only the held set is left alone.
  else go();
});
server.keepAliveTimeout = 120_000;
server.listen(listen, "127.0.0.1", () => {
  console.log(`proxy on 127.0.0.1:${listen} -> ${upHost}:${upPort} prefix=${prefix || "-"}`);
});
