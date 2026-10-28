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

package staterepo

// workflow_history.go holds the renderings of the CI workflow that
// earlier grains wrote, so that a file one of them installed is still
// recognisable as grain's own text rather than as somebody's.
//
// RenderedImage decides what grain may touch by byte equality with a
// template, which is the only test that can tell an operator's runner,
// trigger or added step from grain's own file without deciding what a
// workflow is allowed to say. The cost of that test is that it is a
// test against *this build's* template: edit one comment in it and every
// file grain has already installed stops being recognised, keeps
// whatever image it was installed with, and is never maintained again --
// which is not a broken deployment, but it is a deployment the next fix
// to this file does not reach without somebody running
// `grain state ci -force` on it by hand.
//
// So the templates grain has rendered before are kept here, and a file
// that matches one of them word for word is adopted: grain rewrites it
// to this build's template, once, and maintains its image line from then
// on. Nothing is loosened by this -- each entry is still an exact match
// against text grain itself wrote, and a file with one character of
// anybody else's in it matches none of them.
//
// The rule for editing the template in format.go: add its previous text
// here, unchanged, as a new entry. TestTheCurrentTemplateIsRecorded
// fails until you do, because an edit that skips this step is silent --
// it strands every repository already on the old text and nothing says
// so. The list only ever grows: an entry deleted is a repository that
// can never be adopted, and there is no way to find out from here
// whether one is still out there.

// earlierWorkflows are the templates earlier builds rendered this file
// from, newest first. Each is the whole template, `@IMAGE@` included, as
// the commit named in its comment held it -- so matching one is the same
// prefix/suffix comparison RenderedImage makes against the current one.
var earlierWorkflows = []string{
	earlierWorkflow5,
	earlierWorkflow4,
	earlierWorkflow3,
	earlierWorkflow2,
	earlierWorkflow1,
}

// earlierWorkflow5 is the template as commit 959905e0 left it: the last
// one whose header described a run that fails before any job of it starts
// without naming the symptom GitHub's own page for that run shows, which
// is a job with no steps under it. Every state repository seeded or
// adopted between that commit and this one carries it.
const earlierWorkflow5 = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and afterwards maintains it only for as long as it is
# still word for word grain's own: it keeps IMAGE below pointed at the
# build the deployment is actually running, and replaces the whole file
# when a newer grain words it differently. So an edit to it is safe:
# change the runner, add a step of your own, and grain leaves the file
# alone from then on, image included. To pin the image and keep the
# rest of this file grain's, set "checkImage" in the deployment's
# state-repo.json -- that is what IMAGE is written from. To stop grain
# offering the file at all, set "noWorkflow": true there instead.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. The deployment writes its own image reference here to keep them
# together -- ` + "`grain image`" + ` on its host prints the tag it runs, and
# ` + "`grain state status`" + ` prints both schema numbers.
#
# The one failure this file cannot answer for is the one where nothing
# in it runs: a workflow run GitHub concludes before any job of it
# starts. It reads as every job of the run failing within seconds of the
# push, with no log under any of them -- a job whose only step is
# ` + "`echo`" + ` fails alongside the rest -- and the run's own page on
# github.com carries GitHub's reason for it. No commit and no edit here
# changes that, because it is a fact about the repository rather than
# about the change: Actions disabled or restricted for this repository,
# a runner it may not use, or, for a private one, an account with no
# Actions minutes left or a spending limit it has reached. A check that
# has never once passed on any tree is that, and not a dump that will
# not load. Raise the account's limit; or point ` + "`runs-on`" + ` below at a
# runner the repository may use, which costs a private repository no
# minutes if it is self-hosted; or set "noWorkflow": true in the
# deployment's state-repo.json to stop grain offering this check at all,
# which unblocks every pull request here at the cost of the validation.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// earlierWorkflow4 is the template as commit b993d10b left it: the last
// one whose header said nothing about a run that fails before any job of
// it starts. Every state repository seeded or adopted between that
// commit and this one carries it.
const earlierWorkflow4 = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and afterwards maintains it only for as long as it is
# still word for word grain's own: it keeps IMAGE below pointed at the
# build the deployment is actually running, and replaces the whole file
# when a newer grain words it differently. So an edit to it is safe:
# change the runner, add a step of your own, and grain leaves the file
# alone from then on, image included. To pin the image and keep the
# rest of this file grain's, set "checkImage" in the deployment's
# state-repo.json -- that is what IMAGE is written from. To stop grain
# offering the file at all, set "noWorkflow": true there instead.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. The deployment writes its own image reference here to keep them
# together -- ` + "`grain image`" + ` on its host prints the tag it runs, and
# ` + "`grain state status`" + ` prints both schema numbers.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// earlierWorkflow3 is the template as commit b898473e wrote it: the
// first one whose IMAGE line grain maintained, and so the first whose
// comment block had to explain that it did. Every state repository a
// deployment tracking main seeded between that commit and this one
// carries it.
const earlierWorkflow3 = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and afterwards changes nothing in it but IMAGE below,
# which it keeps pointed at the build the deployment is actually
# running. So an edit to it is safe: change the runner, add a step of
# your own, and grain leaves the file alone from then on, image
# included. To pin the image and keep the rest of this file grain's,
# set "checkImage" in the deployment's state-repo.json -- that is what
# IMAGE is written from. To stop grain offering the file at all, set
# "noWorkflow": true there instead.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. The deployment writes its own image reference here to keep them
# together -- ` + "`grain image`" + ` on its host prints the tag it runs, and
# ` + "`grain state status`" + ` prints both schema numbers.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// earlierWorkflow2 is the template as commit d7bf153e wrote it: the
// first one a deployment installed by itself, and the one every state
// repository formatted or seeded before commit b898473e carries. It
// says grain "never rewrites" a workflow that is there, which was true
// when it was written and is what this file exists to correct.
const earlierWorkflow2 = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and never rewrites one that is. So an edit to it is
# safe: pin the image below, change the runner, add a step of your own,
# and grain leaves what you wrote alone. To have the check run somewhere
# else instead, put your own file here; to stop grain offering it at
# all, set "noWorkflow": true in the deployment's state-repo.json.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. Point IMAGE at the tag that deployment runs --
# ` + "`grain state status`" + ` on its host prints both numbers.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// earlierWorkflow1 is the template as commit 68fbdc37 wrote it, the
// first there was. Only `grain state format` and `grain state ci` ever
// rendered it -- no deployment installed the file until d7bf153e -- so
// the repositories carrying it are the ones somebody formatted by hand
// in that window. Few, possibly none, and kept for the same reason as
// the other: the only way to find out that there were some is to break
// them.
const earlierWorkflow1 = `# Written by ` + "`grain state format`" + `. Checks that grain can still load
# this repository.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. Point IMAGE at the tag that deployment runs --
# ` + "`grain state status`" + ` on its host prints both numbers.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`
