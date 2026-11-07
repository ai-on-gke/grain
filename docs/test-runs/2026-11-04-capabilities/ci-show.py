import json,sys
r=json.load(sys.stdin); print("run", r["id"], r["event"], "conclusion:", r.get("conclusion"), "warning:", r.get("warning"))
for j in r["jobs"]:
    print("job", j["name"], j.get("conclusion"), "| error:", j.get("error"), "| warning:", j.get("warning"))
    for s in j.get("steps",[]):
        print("  step", s.get("name","")[:40], s.get("conclusion"))
        for k in ("output","log","tail"):
            if s.get(k): print("   |", s[k][-1500:].replace("\n","\n   | "))
