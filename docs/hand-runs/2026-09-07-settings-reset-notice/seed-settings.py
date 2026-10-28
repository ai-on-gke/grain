#!/usr/bin/env python3
"""Seed a state repository with enough settings to reach the reset
comment's 200-value cap, and then strip those settings back out.

`grain_config` has one row and eighteen omittable columns, so no merge
against it can ever reach resetCommentValues. A deployment that can is
one with many rows in a settings table: this writes seventy repo configs
carrying two non-default values each and twenty schedules carrying five,
which is 240 values for one merge to take away.

    seed-settings.py seed  <clone>   # rows with values worth losing
    seed-settings.py strip <clone>   # the same rows, columns removed

Run `seed` against a clone, commit and push it, wait for the deployment
to take it up and export it back, pull, then run `strip` on the result.
Two merges: the first is what an operator built up over months, the
second is the file branched before an upgrade that drops all of it.
"""

import json
import os
import sys

REPO_CONFIGS = 70
SCHEDULES = 20

# What a merged file leaving these columns out would cost. Both lists are
# columns with a usable default -- a NOT NULL column with none cannot be
# omitted at all, which `grain state check` refuses rather than warns
# about (see the write-up's section 1).
REPO_CONFIG_COLUMNS = ["prompt_extension", "setup_command"]
SCHEDULE_COLUMNS = ["base", "every_n_hours", "time_of_day_minutes", "last_run_at", "overlap"]

# Three values chosen to be hostile to Markdown rather than to be
# typical: the comment puts database values in a fenced block precisely
# so that an operator's asterisks, pipes and backticks cannot escape it,
# and the fence has never been read on a rendered page. The third one
# closes the fence itself, which is the one character sequence a fence
# cannot survive.
AWKWARD = {
    3: "Prefer **bold** headings | pipes | and `backticks` in what you write here.",
    7: "Run the checks with `make test | tee log` -- do *not* skip the ones marked [slow].",
    11: "End every summary with a fenced block:\n```\nok\n```\nso the reviewer can copy it.",
}


def repo_config_rows():
    rows = []
    for i in range(REPO_CONFIGS):
        rows.append({
            "owner": "grain-handrun",
            "name": "service-%02d" % i,
            "default_capabilities": "gcp-key" if i % 3 == 0 else "",
            "prompt_extension": AWKWARD.get(i, "This repository's runs answer in service-%02d's own "
                                            "voice, and say which columns they read." % i),
            "setup_command": "make deps && ./scripts/bootstrap.sh --service %02d" % i,
        })
    return rows


def schedule_rows(existing):
    rows = list(existing)
    for i in range(SCHEDULES):
        rows.append({
            "id": "handrun:nightly-%02d" % i,
            "title": "Nightly sweep %02d" % i,
            "body": "Look at what run %02d left behind and file what needs filing." % i,
            "target_owner": "grain-handrun",
            "target_name": "service-%02d" % i,
            "base": "release-2026-09",
            "auto_merge": 0,
            "template_id": None,
            "suite_id": None,
            "recurrence_kind": "everyNHours",
            "every_n_hours": 6 + i % 5,
            "time_of_day_minutes": 60 * (i % 12),
            "weekday": None,
            "day_of_month": None,
            "overlap": "queue",
            # enabled = 0 throughout: this is a hand run about settings,
            # and a schedule that fires would file tasks on a deployment
            # with no agent to run them.
            "enabled": 0,
            "next_run_at": "2027-01-01T00:00:00Z",
            "last_run_at": "2026-09-0%dT0%d:00:00Z" % (1 + i % 7, i % 10),
            "created_at": "2026-09-01T00:00:00Z",
        })
    return rows


def load(clone, table):
    with open(os.path.join(clone, "tables", table + ".json")) as f:
        return json.load(f)


def save(clone, table, rows):
    with open(os.path.join(clone, "tables", table + ".json"), "w") as f:
        json.dump(rows, f, indent=2)
        f.write("\n")


def main():
    mode, clone = sys.argv[1], sys.argv[2]
    if mode == "seed":
        save(clone, "repo_config", repo_config_rows())
        save(clone, "schedule", schedule_rows(load(clone, "schedule")))
        print("seeded %d repo configs and %d schedules: %d values a merge could take away"
              % (REPO_CONFIGS, SCHEDULES,
                 REPO_CONFIGS * len(REPO_CONFIG_COLUMNS) + SCHEDULES * len(SCHEDULE_COLUMNS)))
        return
    if mode == "strip":
        lost = 0
        for table, columns in (("repo_config", REPO_CONFIG_COLUMNS), ("schedule", SCHEDULE_COLUMNS)):
            rows = load(clone, table)
            for row in rows:
                if table == "schedule" and not row["id"].startswith("handrun:"):
                    continue
                for c in columns:
                    if row.pop(c, None) not in (None, "", "skip"):
                        lost += 1
            save(clone, table, rows)
        print("stripped those columns back out: %d values whose database side is not the default" % lost)
        return
    raise SystemExit(__doc__)


if __name__ == "__main__":
    main()
