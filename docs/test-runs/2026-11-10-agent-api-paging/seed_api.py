#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""docs/test-plans/2026-11-10.md §6: seeds capability servers on a running daemon.

seed.go cannot write these into the store before boot: the daemon's first
start replaces seed.go's connections-only config row with one seeded from
its flags, dropping mcpServers (daemon.LoadConfig). So they go through the
operator's own route, PUT /api/capability-servers:

- n disabled servers https://tools.example.com/capabilities/cap-NNN
  (alternately a command and a serverUrl). Each is granted by template
  tmpl-NNN (seed.go), so GET /api/capability-servers/usage has a holder
  per server.
- The built-in gchat server configured with n webhook spaces, so GET
  /api/capability-servers/gchat-usage answers n rows. Nothing is posted:
  the webhook URLs are never called.
"""

import argparse
import json
import urllib.error
import urllib.request

GCHAT = "https://grain.dev/capabilities/gchat"


def put(url, code, body):
    req = urllib.request.Request(url + "/api/capability-servers", data=json.dumps(body).encode(), method="PUT",
                                 headers={"Authorization": "Bearer " + code, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        raise SystemExit(f"PUT {body['id']}: HTTP {e.code}: {e.read()[:400]!r}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--code-file", required=True, help="the daemon's local-api-token file")
    ap.add_argument("--count", type=int, default=260)
    ap.add_argument("--gchat-listen", default="",
                    help="the gchat data plane's address, when two daemons on one host would both take its default")
    ap.add_argument("--gchat-only", action="store_true", help="configure gchat and nothing else")
    args = ap.parse_args()
    code = open(args.code_file).read().strip()

    # The gchat entry keeps the command the built-in already runs with:
    # an entry with neither command nor serverUrl is refused.
    req = urllib.request.Request(args.url + "/api/capability-servers", headers={"Authorization": "Bearer " + code})
    with urllib.request.urlopen(req, timeout=60) as r:
        current = {s["id"]: s for s in json.load(r)}
    gchat = dict(current[GCHAT]["config"])
    if args.gchat_listen:
        argv = list(gchat.get("args", []))
        if "-listen" in argv:
            argv[argv.index("-listen") + 1] = args.gchat_listen
        else:
            argv += ["-listen", args.gchat_listen]
        gchat["args"] = argv
    spaces = [{"name": f"room-{i:03d}", "transport": "webhook", "allowed": {"per_hour": 30}} for i in range(args.count)]
    hooks = {s["name"]: f"https://chat.googleapis.com/v1/spaces/SEED{i:03d}/messages?key=k{i}&token=t{i}"
             for i, s in enumerate(spaces)}
    gchat["config"] = {"grain_url": "https://grain.example.com", "spaces": spaces}
    put(args.url, code, {"id": GCHAT, "config": gchat, "configSecrets": {"webhook_urls": json.dumps(hooks)}})
    if args.gchat_only:
        print(f"configured gchat with {len(spaces)} spaces")
        return

    for i in range(args.count):
        cfg = {"disabled": True}
        if i % 2 == 0:
            cfg["command"] = f"/usr/local/bin/mcp-tool-{i:03d}"
        else:
            cfg["serverUrl"] = f"https://mcp-{i:03d}.example.com/rpc"
        out = put(args.url, code, {"id": f"https://tools.example.com/capabilities/cap-{i:03d}", "config": cfg})
    print(f"configured gchat with {len(spaces)} spaces and {args.count} servers; {len(out)} servers listed")


if __name__ == "__main__":
    main()
