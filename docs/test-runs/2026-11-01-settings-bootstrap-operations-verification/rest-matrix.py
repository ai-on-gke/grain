import json, os, subprocess, sys, time, urllib.request

H = "/home/debian/live"
BASE = "http://127.0.0.1:18731"
TOK = open(H + "/.grain/local-api-token").read().strip()
REPO = H + "/.grain/state-repo"
results = []


def call(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Authorization": "Bearer " + TOK, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode()[:300]


def git(*a):
    return subprocess.run(["git", "-C", REPO] + list(a), capture_output=True, text=True).stdout


def exported():
    return json.load(open(REPO + "/tables/grain_config.json"))


def rec(axis, setting, expect, got, ok, note=""):
    results.append(dict(axis=axis, setting=setting, expect=expect, got=got, ok=ok, note=note))
    print(("PASS " if ok else "FAIL ") + f"[{axis}] {setting}: expect {expect}, got {got} {note}", flush=True)


# field -> (value, bootstrap?, exported column name)
FIELDS = {
    # Models & sandboxes (bootstrap)
    "defaultModel": ("claude-opus-5-5", True),
    "maxWorkers": (7, True),
    "sandboxCpus": (3, True),
    "sandboxMemoryMb": (6144, True),
    "sandboxDiskGb": (30, True),
    "networkAccess": ("none", True),
    "sandboxCpuOvercommit": (3, True),
    "sandboxMemoryOvercommit": (1.5, True),
    "sandboxDiskFloorShare": (0.2, True),
    "sandboxImage": ("example.com/img:v1", True),
    "sessionSandboxIdleTtl": ("45m", False),
    # Legacy capabilities deployment-wide (config-managed)
    "githubHost": ("github.example.com", False),
    "gcpProject": ("proj-verif", False),
    "gcpServiceAccountEmail": ("sa@proj-verif.iam.gserviceaccount.com", False),
    # Caches
    "packageCacheSizeGb": (12, False),
    "actionsCacheSizeGb": (9, False),
    "packageCacheEnabled": (False, False),
    "actionsCacheEnabled": (False, False),
    # Engine
    "pollInterval": ("25s", False),
    "maxMergesPerRepo": (2, False),
    "mergeBatchSize": (3, False),
    "mergeGatherWindow": ("90s", False),
}

st, cfg = call("GET", "/api/config")
rec("baseline", "configRepoOnly", False, bool(cfg.get("configRepoOnly")), not cfg.get("configRepoOnly"))

# (a) unlocked: every field via the UI's PUT /api/settings, exported at once.
for f, (v, boot) in FIELDS.items():
    head = git("rev-parse", "HEAD").strip()
    st, body = call("PUT", "/api/settings", {f: v})
    got = body.get(f) if isinstance(body, dict) else body
    ok = st == 200
    newhead = git("rev-parse", "HEAD").strip()
    rec("a-unlocked-put", f, 200, st, ok, f"readback={got!r} exportCommit={'yes' if newhead != head else 'no'}")

ex = json.dumps(exported())
MS = {"sessionSandboxIdleTtl": 2700000, "pollInterval": 25000, "mergeGatherWindow": 90000}
for f, (v, boot) in FIELDS.items():
    if f in ("packageCacheEnabled", "actionsCacheEnabled"):
        continue
    if f in MS:
        v = MS[f]
    sval = json.dumps(v)[1:-1] if isinstance(v, str) else str(v)
    rec("a-export", f, "value in tables/grain_config.json", sval in ex, sval in ex)

# secrets: write-only, never exported, never read back
st, _ = call("PUT", "/api/secrets/verif-secret/token", {"value": "sup3r-s3cret-VALUE-9917"})
rec("a-secret", "PUT /api/secrets", 200, st, st == 200)
st, lst = call("GET", "/api/secrets")
rec("a-secret", "GET /api/secrets hides value", "no value", "sup3r" in json.dumps(lst), "sup3r" not in json.dumps(lst))
time.sleep(1)
grep = subprocess.run(["grep", "-r", "sup3r-s3cret", REPO], capture_output=True, text=True).stdout
rec("a-secret", "secret absent from grain/config", "absent", bool(grep), not grep)
st, s = call("GET", "/api/settings")
rec("a-secret", "secret absent from /api/settings", "absent", "sup3r" in json.dumps(s), "sup3r" not in json.dumps(s))

# executors: join token create/revoke (runtime, unguarded)
st, tok = call("POST", "/api/executors/tokens", {"name": "verif"})
rec("a-executor", "POST /api/executors/tokens", 200, st, st in (200, 201), str(tok)[:80])
tid = tok["record"]["id"]
rec("a-executor", "token secret not exported", "absent", tok["token"] in json.dumps(dict(r=open(REPO + "/tables/grain_config.json").read())), subprocess.run(["grep", "-r", tok["token"], REPO], capture_output=True).returncode != 0)
st2, _ = call("POST", f"/api/executors/tokens/{tid}/revoke")
rec("a-executor", "revoke token", 200, st2, st2 in (200, 204))
toks_in_repo = subprocess.run(["grep", "-rl", "executor_token", REPO + "/tables"], capture_output=True, text=True).stdout
rec("a-executor", "executor tokens not exported", "absent", toks_in_repo.strip(), not toks_in_repo.strip())

# (b) commit to grain/config, grain state check, sync into API.
d = exported()
row = d[0] if isinstance(d, list) else d
blob = json.dumps(d)
def setcol(obj, key, val):
    if isinstance(obj, dict):
        for k in list(obj):
            if k == key:
                obj[k] = val
                return True
            if setcol(obj[k], key, val):
                return True
    if isinstance(obj, list):
        return any(setcol(x, key, val) for x in obj)
    return False
B = {"poll_interval_ms": (40000, "pollInterval", "40s"),
     "merge_batch_size": (5, "mergeBatchSize", 5),
     "package_cache_size_gb": (20, "packageCacheSizeGb", 20),
     "sandbox_cpus": (5, "sandboxCpus", 5),
     "session_sandbox_idle_ttl_ms": (3000000, "sessionSandboxIdleTtl", "50m0s"),
     "gcp_project": ("proj-from-repo", "gcpProject", "proj-from-repo")}
for col, (val, _, _) in B.items():
    if not setcol(d, col, val):
        print("column not found:", col)
with open(REPO + "/tables/grain_config.json", "w") as fh:
    json.dump(d, fh, indent=2)
    fh.write("\n")
chk = subprocess.run([sys.argv[1], "state", "check", REPO], capture_output=True, text=True)
rec("b-repo", "grain state check", 0, chk.returncode, chk.returncode == 0, (chk.stdout + chk.stderr)[-200:].replace("\n", " "))
git("-c", "user.name=v", "-c", "user.email=v@example.com", "commit", "-qam", "verif: edit settings in grain/config")
st, _ = call("POST", "/api/state-repo/sync")
rec("b-repo", "POST /api/state-repo/sync", 200, st, st == 200)
time.sleep(2)
st, s = call("GET", "/api/settings")
for col, (val, api, want) in B.items():
    rec("b-repo-sync", api, want, s.get(api), s.get(api) == want)

# (c) lock
st, body = call("PUT", "/api/settings", {"settingsLocked": True})
rec("c-lock", "PUT settingsLocked=true", 200, st, st == 200)
st, cfg = call("GET", "/api/config")
rec("c-lock", "GET /api/config configRepoOnly", True, cfg.get("configRepoOnly"), cfg.get("configRepoOnly") is True)
for f, (v, boot) in FIELDS.items():
    nv = v
    if isinstance(v, bool):
        nv = not v
    elif isinstance(v, int):
        nv = v + 1
    elif isinstance(v, float):
        nv = round(v + 0.1, 2)
    elif f == "networkAccess":
        nv = "open"
    else:
        nv = (v + "x") if not v.endswith(("s", "m")) else ("61m" if v.endswith("m") else "26s")
    st, body = call("PUT", "/api/settings", {f: nv})
    want = 200 if boot else 409
    code = body.get("code") if isinstance(body, dict) else None
    rec("c-locked-put", f, want, st, st == want, f"code={code}")
st, _ = call("PUT", "/api/secrets/verif-secret/token", {"value": "another-s3cret"})
rec("c-locked", "PUT /api/secrets", 200, st, st == 200)
st, tok = call("POST", "/api/executors/tokens", {"name": "verif-locked"})
rec("c-locked", "POST /api/executors/tokens", "2xx", st, st in (200, 201))
st, _ = call("POST", "/api/state-repo/sync")
rec("c-locked", "POST /api/state-repo/sync", 200, st, st == 200)
st, _ = call("PUT", "/api/settings", {"settingsLocked": False})
rec("c-lock", "PUT settingsLocked=false (unlock)", 200, st, st == 200)
st, cfg = call("GET", "/api/config")
rec("c-lock", "unlocked configRepoOnly", False, bool(cfg.get("configRepoOnly")), not cfg.get("configRepoOnly"))
st, _ = call("PUT", "/api/settings", {"pollInterval": "33s"})
rec("c-lock", "pollInterval writable again", 200, st, st == 200)

# (d) external mode: a local bare clone as the external repo.
ext = H + "/ext.git"
subprocess.run(["rm", "-rf", ext])
subprocess.run(["git", "clone", "-q", "--bare", REPO, ext])
st, body = call("POST", "/api/state-repo/remote", {"remote": "file://" + ext, "branch": "main"})
rec("d-external", "POST /api/state-repo/remote", 200, st, st == 200, str(body)[:160])
st, cfg = call("GET", "/api/config")
rec("d-external", "configRepoOnly", True, cfg.get("configRepoOnly"), cfg.get("configRepoOnly") is True)
for f, (v, boot) in FIELDS.items():
    st, body = call("PUT", "/api/settings", {f: v})
    rec("d-external-put", f, 409, st, st == 409)
st, _ = call("PUT", "/api/secrets/verif-secret/token", {"value": "ext-s3cret"})
rec("d-external", "PUT /api/secrets", 200, st, st == 200)
st, _ = call("PUT", "/api/settings", {"settingsLocked": True})
rec("d-external", "PUT settingsLocked (bootstrap) in external", 409, st, st == 409)
st, body = call("POST", "/api/state-repo/remote", {"none": True})
rec("d-external", "back to internal", 200, st, st == 200)
st, cfg = call("GET", "/api/config")
rec("d-external", "internal again configRepoOnly", False, bool(cfg.get("configRepoOnly")), not cfg.get("configRepoOnly"))

json.dump(results, open(H + "/matrix.json", "w"), indent=1)
print("FAILS:", sum(not r["ok"] for r in results), "of", len(results))
