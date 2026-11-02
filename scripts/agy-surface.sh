#!/usr/bin/env bash
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

# agy-surface.sh -- read agy's surface off the installed binary and write
# it down as Markdown: every flag, subcommand, changelog entry and model
# name it will admit to, what it does with a HOME it has never seen, which
# of several candidate paths it actually reads its customizations from,
# and the config keys sitting in its string table.
#
# Why this exists. agy is a ~200MB stripped Go binary whose only
# documentation is what it prints. The Dockerfile pins its immutable
# release artifact, and pkg/agent/antigravity depends on a dozen facts
# about it
# -- three config paths, a hook contract, a flag set, a model catalog --
# that nothing in this repository can check for itself. Every task that
# has had to re-answer "what can agy be configured to do?" has re-derived
# it by hand, and a grain sandbox cannot: no network beyond the git proxy,
# and no agy in the image. So the derivation lives here instead, as a
# script CI runs on demand (.grain/workflows/agy-surface.yml), and its
# output is committed to docs/agy-surface.md -- where the next question
# costs one dispatch, and the next agy release shows up as a diff.
#
# Usage:
#   scripts/agy-surface.sh [-o docs/agy-surface.md]
#
# The binary is $AGY or $GRAIN_AGY_PATH, defaulting to `agy` on $PATH --
# the same resolution order the daemon's own -agy-path flag has.
#
# Two rules shape what goes in the output.
#
# Nothing here may fail the run. Every probe is a question this script
# does not know the answer to -- that is the point of asking the binary --
# so a command that exits non-zero, times out or prints nothing records
# exactly that and the script carries on. A probe that stopped answering
# (`agy -p /permissions` wanting a credential, say) is a finding, and it
# belongs in the diff rather than in a red job.
#
# And the output has to be stable enough that a diff means drift. Nothing
# run-specific goes in it -- no dates, no run numbers, no temporary paths:
# every capture is scrubbed of the throwaway HOMEs it was taken in and
# every extracted list is sorted, so that two runs against the same agy
# produce the same bytes and a run against a different agy produces a diff
# that is entirely about agy.

# File-wide rather than on any one line: this script's output is Markdown,
# and most of it is emitted by `printf` with a single-quoted format string
# full of the prose that document is made of -- `$PROBE`, `$UUID`,
# `${guide#./}`, backticked identifiers. Every one of those looks to the
# linter like an expansion someone forgot to double-quote (SC2016), and
# there are twenty-odd of them; the sed scrubbers below write the same
# `$NAME` placeholders into the captures on purpose. Suppressing them one
# at a time would put a directive over every second printf in the file and
# still redden CI the next time a sentence gains a backtick. The cost is
# that a genuinely wrong '$var' in this one file goes unreported -- worth
# it here, where the single-quoted strings are documents rather than
# commands, and not something to copy into a script that runs anything.
# shellcheck disable=SC2016

# Not -e: see the first rule above. A probe's failure is an answer.
set -uo pipefail

AGY="${AGY:-${GRAIN_AGY_PATH:-agy}}"

# Every agy invocation is wrapped in this. A binary that decides to wait
# for a terminal, open a browser or serve something (agy has both
# `mic-serve` and `remote-control`) must not hang a CI job.
TIMEOUT="${AGY_SURFACE_TIMEOUT:-60}"

# Per-capture caps, so one verbose probe cannot turn a reviewable document
# into a megabyte nobody reads. A capture that hits either says so in
# place, rather than ending mid-answer.
MAX_LINES="${AGY_SURFACE_MAX_LINES:-400}"
MAX_BYTES="${AGY_SURFACE_MAX_BYTES:-65536}"

# Six backticks, because much of what is captured here is itself Markdown
# -- agy unpacks its own customization guides, fenced code blocks and all,
# into any fresh HOME -- and a three-backtick fence would be closed by the
# first fence inside one of them.
FENCE='``````'

# Byte-wise sorting and matching everywhere, so the document does not
# depend on the locale of whoever ran it.
export LC_ALL=C

# A placeholder where agy expects a Gemini API key, and the single change
# that turned four dead probes into answers. agy gates `models`,
# `-p /permissions`, `-p /hooks` and the stream-json session behind having
# *some* credential: with no settings at all it says "authentication
# required. Run 'agy' to log in", and with `modelProvider: gemini` in
# settings.json but no key in the environment it refuses before it reads
# anything. It does not validate what it is given, though -- the first
# capture established that a key of `not-a-real-key` gets the full model
# catalog, the loaded permission rules, the loaded hooks and the whole
# `init` event, and only the model call that follows them fails. So the
# credential-shaped configuration is planted rather than a credential: it
# is the same shape `writeAgyHome` builds for an API-key run, which is
# also what makes those probes evidence about how grain runs agy.
#
# Not a secret, and deliberately not read from the environment: a real key
# reaching this script would put a real conversation in the document.
API_KEY_PLACEHOLDER='agy-surface-not-a-real-key'

# The settings.json that goes with it, at the one path agy reads settings
# from (`cliSettingsRelPath` in pkg/agent/antigravity).
CLI_SETTINGS_REL='.gemini/antigravity-cli/settings.json'

# agy updates its own binary during an ordinary run, and this document
# would otherwise be the first casualty: the "## The binary" section
# records a size and a sha256, several probes below exec agy again
# afterwards, and one of them (`agy models`, and the stream-json session
# at the end) is enough to have the file replaced underneath the
# capture -- making a document that names one release out of two
# different binaries. Measured on 1.1.27, which became 1.2.5 seven
# seconds after one such exec where the file was writable:
# the 2026-09-17 agy-self-update hand run.
#
# .grain/workflows/agy-surface.yml unpacks the release as root and runs
# this as the runner's own user, so the captures committed so far were
# consistent by permission rather than by intent. A local run against
# `agy` in one's own ~/.local/bin had no such protection. The value has
# to be exactly "true"; a bare 1 was measured not to work. Exported, so
# that every invocation below carries it, including the two that build
# their own environment.
#
# The corollary for a reader: this document does not track agy releases
# on its own any more than the Dockerfile's pin does. A new agy arrives
# by bumping that pin and dispatching this workflow.
export AGY_CLI_DISABLE_AUTO_UPDATE=true

# Defaulted rather than assumed: scrub below rewrites $HOME out of every
# capture, and a HOME-less environment would abort the script under -u
# instead.
HOME="${HOME:-/root}"

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output) out="${2:-}"; shift 2 ;;
    -h|--help)   sed -n '2,26p' "$0"; exit 0 ;;
    *)
      echo "agy-surface.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if ! command -v "$AGY" >/dev/null 2>&1 && [[ ! -x "$AGY" ]]; then
  echo "agy-surface.sh: no agy at '$AGY'. Set AGY or GRAIN_AGY_PATH, or install it the way the Dockerfile does (antigravity.google/cli/install.sh)." >&2
  exit 2
fi

probe_root="$(mktemp -d "${TMPDIR:-/tmp}/agy-surface.XXXXXX")"
trap 'rm -rf "$probe_root"' EXIT

if [[ -n "$out" ]]; then
  exec >"$out"
fi

# ---------------------------------------------------------------- helpers

# scrub removes everything about *this* run from a capture: terminal
# colour, the throwaway HOME the probe ran in, the real HOME agy was
# installed into, and the checkout it was run from. What is left should be
# the same bytes on a laptop and on a runner. It also drops the vendor's
# LINT.IfChange / LINT.ThenChange markers its bundled guides carry, and
# replaces the internal workspace path one of them mentions: both point
# into Google's internal source tree, which this repository may not name
# (tests/hygiene's TestCoreSaysNoGoogleTokens), and say nothing about agy.
# The bracket in that pattern keeps this file from naming it either.
scrub() {
  sed -e '/<!-- LINT\.\(IfChange\|ThenChange\)(.*-->/d' \
      -e 's#/google/[s]rc/cloud/#$INTERNAL_WORKSPACE/#g' \
      -e 's/\x1b\[[0-9;?]*[a-zA-Z]//g' \
      -e 's/\r$//' \
      -e "s#${probe_root}#\$PROBE#g" \
      -e "s#${PWD}#\$PWD#g" \
      -e "s#${HOME}#\$HOME#g" \
      -e "s/${API_KEY_PLACEHOLDER}/\$GEMINI_API_KEY/g" \
      -e 's/[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}/$UUID/g' \
      -e 's/"duration_seconds":[0-9.e-]*/"duration_seconds":$SECONDS/g' \
      -e 's/cli-[0-9]\{8\}_[0-9]\{6\}\.log/cli-$TIMESTAMP.log/g' \
      -e 's/crash_[0-9]\{1,\}_/crash_$PID_/g'
}

# limit applies the two caps, announcing either in the document.
limit() {
  awk -v max="$MAX_LINES" -v maxb="$MAX_BYTES" '
    NR > max { print "... [truncated at " max " lines by scripts/agy-surface.sh]"; exit }
    {
      bytes += length($0) + 1
      if (bytes > maxb) { print "... [truncated at " maxb " bytes by scripts/agy-surface.sh]"; exit }
      print
    }'
}

# block writes one capture: the command as a reader would type it, its
# output, and its exit status when that status was not zero. An empty
# capture says so, because "printed nothing" and "was never run" have to
# look different in a document read as evidence.
block() {
  local label="$1" status="$2" output="$3"
  # Trailing blank lines only ever come from a summary this script built
  # itself; dropping them keeps a regenerated document from differing in
  # whitespace nobody changed.
  while [[ "$output" == *$'\n' ]]; do output="${output%$'\n'}"; done
  printf '%sconsole\n$ %s\n' "$FENCE" "$label"
  if [[ -n "$output" ]]; then
    printf '%s\n' "$output" | scrub | limit
  else
    printf '[no output]\n'
  fi
  if [[ "$status" -ne 0 ]]; then
    printf '[exit %s]\n' "$status"
  fi
  printf '%s\n\n' "$FENCE"
}

# agy_run runs agy in a given HOME and leaves its combined output in
# agy_output and its status in agy_status. Globals rather than a captured
# stdout because `x="$(f)"` runs f in a subshell, where the exit status of
# the binary -- half of what this script is recording -- would be lost.
agy_output=""
agy_status=0
agy_run() {
  local home="$1"; shift
  agy_output="$(HOME="$home" NO_COLOR=1 TERM=dumb CI=1 \
    GEMINI_API_KEY="$API_KEY_PLACEHOLDER" \
    timeout "$TIMEOUT" "$AGY" "$@" 2>&1)"
  agy_status=$?
}

# agy_capture is the pair of them: run, and write the block.
agy_capture() {
  local home="$1"; shift
  agy_run "$home" "$@"
  block "agy $*" "$agy_status" "$agy_output"
}

# through_version drops the changelog entries for releases newer than $1,
# the binary's own version: an entry is a `1.4.11:` header line and
# everything up to the next one. A $1 that is not a dotted version (agy
# --version printed something else) drops nothing.
through_version() {
  awk -v want="$1" '
    function newer(a, b,   x, y, n, i) {
      n = split(a, x, "."); split(b, y, ".")
      for (i = 1; i <= n || i in y; i++) {
        if (x[i] + 0 != y[i] + 0) return x[i] + 0 > y[i] + 0
      }
      return 0
    }
    /^[0-9]+(\.[0-9]+)*:$/ {
      skip = want ~ /^[0-9]+(\.[0-9]+)*$/ && newer(substr($0, 1, length($0) - 1), want)
    }
    !skip'
}

# fresh_home is a HOME agy has never seen, which is the only condition
# under which several of these questions have an answer at all: what it
# unpacks, what it discovers, and what it reads when nothing else is
# there.
fresh_home() {
  mktemp -d "$probe_root/home.XXXXXX"
}

# configured_home is a fresh HOME plus the one setting that makes agy
# willing to answer at all: see API_KEY_PLACEHOLDER. Anything that would
# otherwise come back "authentication required" is asked in one of these.
configured_home() {
  local home
  home="$(fresh_home)"
  plant "$home/$CLI_SETTINGS_REL" '{"modelProvider":"gemini"}'
  printf '%s' "$home"
}

# plant writes a file and every directory above it.
plant() {
  local path="$1" content="$2"
  mkdir -p "$(dirname "$path")"
  printf '%s\n' "$content" >"$path"
}

# subcommands_of pulls command names out of a --help listing: the indented
# rows under a subcommand heading, with `a, b` and `a/b` aliases split
# apart. Deliberately generous about the heading, since the shape of that
# listing is agy's to change -- and a discovery that finds nothing is
# reported where it happens rather than leaving an empty section that
# reads like "agy has no subcommands".
#
# The generosity was not generous enough, and the first capture is what
# said so: 1.1.26 heads its listing "Available subcommands:", which a
# pattern anchored on a capitalised "Commands:" does not match, so that
# capture recorded its own failure ("No subcommands could be read out of
# that listing") and held not one `agy <sub> --help`. Matching either
# spelling of the word, in either case, is the fix.
subcommands_of() {
  awk '
    /^[A-Za-z ]*[Ss]ub[Cc]ommands:/ { inside = 1; next }
    /^[A-Za-z ]*[Cc]ommands:/       { inside = 1; next }
    /^[^[:space:]]/        { inside = 0 }
    inside && /^[[:space:]]+[a-z][a-z0-9-]*/ {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      split(line, fields, /[[:space:]]+/)
      names = fields[1]
      gsub(/[,\/]/, " ", names)
      split(names, each, / /)
      for (i in each) if (each[i] != "") print each[i]
    }' | sort -u
}

# in_binary greps the binary's own string table. It is how the config
# schema is read: agy is stripped, but a Go binary still carries every
# struct tag, every JSON-schema description and every literal path its own
# code names, and those are the keys a settings file may use.
#
# The table is read once into a file rather than once per pattern: it is
# 1.8 million lines off a 200MB binary, and a dozen patterns each paying
# for their own pass is most of this script's runtime.
binary_path=""
strings_file=""
in_binary() {
  [[ -n "$binary_path" ]] || return 0
  if [[ -z "$strings_file" ]]; then
    strings_file="$probe_root/strings.txt"
    strings -n 4 -- "$binary_path" >"$strings_file" 2>/dev/null
  fi
  grep -aoE "$1" "$strings_file" | sort -u
}

# extract writes one string-table finding as its own block, under what it
# is looking for in words and above the command that looked -- which is
# the real pattern, so that a reader who doubts a finding can run it.
#
# The count is reported when a pattern matched more than the block can
# hold, because the two ways a string-table read goes wrong look identical
# in the output otherwise. A pattern that is too narrow shows a short list;
# a pattern that is too broad shows a list truncated in alphabetical order,
# which is not a sample of anything -- the first capture's `json:"..."`
# block was 400 lines of the AWS SDK and a JSON-schema library, cut off in
# the A's, under a heading promising the keys a settings file may use.
extract() {
  local label="$1" pattern="$2" found total
  found="$(in_binary "$pattern")"
  total="$(printf '%s' "$found" | grep -c . || true)"
  printf '**%s**\n\n' "$label"
  if [[ "$total" -gt "$MAX_LINES" ]]; then
    printf '%s distinct matches, of which the first %s are below. A list this long is a pattern that has caught the binary'"'"'s vendored dependencies as well as agy'"'"'s own types; narrow it in `scripts/agy-surface.sh` rather than reading it.\n\n' \
      "$total" "$MAX_LINES"
  fi
  block "strings -n 4 agy | grep -aoE '$pattern' | sort -u" 0 "$found"
}

# ----------------------------------------------------------------- header

cat <<'MARKDOWN'
<!-- Generated by scripts/agy-surface.sh. Do not edit by hand: the next
     dispatch of .grain/workflows/agy-surface.yml overwrites it. -->

# agy's surface, read off the binary

Everything the installed `agy` will say about itself, captured by
`scripts/agy-surface.sh` and regenerated on demand by
`.grain/workflows/agy-surface.yml` (`workflow_dispatch` only, no
schedule, no credential). `pkg/agent/antigravity` depends on a dozen facts
about a binary nothing in this repository can check -- its flags, its
config paths, its hook contract, its model catalog. The Dockerfile pins it,
so this file is the binary's own answer,
in the tree, where a re-derivation costs one dispatch and a new agy
release arrives as a diff.

Every probe below is run with `AGY_CLI_DISABLE_AUTO_UPDATE=true`, so the
whole document is one binary: agy replaces its own file in the background
during an ordinary run, and the digest recorded above would otherwise
belong to a release that some of the captures below were not taken from.
That is also why nothing here changes on its own -- a new agy arrives by
bumping the Dockerfile's pin and dispatching this workflow, and
`pkg/agent/antigravity/selfupdate.go` is the decision behind that.

Read it as evidence rather than as documentation: every section below is a
command and its output, the commands that failed included. A probe that
stopped answering is itself a finding. What was made of the first such
capture is README's "agy 1.1.26 has no denylist for its own native tools",
and the caveat recorded there governs everything here -- agy ignores an
unknown key in silence, so a key appearing below is not proof that writing
it does anything.

MARKDOWN

# ------------------------------------------------------------ the binary

printf '## The binary\n\n'

agy_run "$(fresh_home)" --version
block "agy --version" "$agy_status" "$agy_output"
agy_version="$(printf '%s\n' "$agy_output" | head -n1)"

# The install symlinks agy onto $PATH from wherever its installer put it
# (the Dockerfile and live-agent.yml both *find* it rather than assuming a
# path), so resolve it: the string-table reads below need the real file,
# and its digest is what says "the same binary as last time" on a day the
# version string alone would not.
resolved="$(command -v "$AGY" 2>/dev/null || printf '%s' "$AGY")"
binary_path="$(readlink -f "$resolved" 2>/dev/null || printf '%s' "$resolved")"
[[ -r "$binary_path" ]] || binary_path=""

printf '%sconsole\n' "$FENCE"
if [[ -n "$binary_path" ]]; then
  printf 'path   %s\n' "$(printf '%s' "$binary_path" | scrub)"
  printf 'size   %s bytes\n' "$(stat -c %s "$binary_path" 2>/dev/null || echo unknown)"
  printf 'sha256 %s\n' "$(sha256sum "$binary_path" 2>/dev/null | cut -d' ' -f1)"
else
  printf '[the binary could not be resolved or read; the string-table section below is empty]\n'
fi
printf '%s\n\n' "$FENCE"

# ---------------------------------------------------- commands and flags

printf '## Commands and flags\n\n'

help_home="$(fresh_home)"
agy_run "$help_home" --help
help_out="$agy_output"
block "agy --help" "$agy_status" "$help_out"

subs="$(printf '%s\n' "$help_out" | scrub | subcommands_of)"
if [[ -z "$subs" ]]; then
  printf 'No subcommands could be read out of that listing: its shape has changed, and `subcommands_of` in `scripts/agy-surface.sh` is what needs updating.\n\n'
fi

for sub in $subs; do
  # `help` is the listing above under another name, and `install`/`update`
  # would replace the very binary this document is about.
  case "$sub" in
    help|install|update) continue ;;
  esac
  printf '### `agy %s`\n\n' "$sub"
  agy_run "$help_home" "$sub" --help
  sub_out="$agy_output"
  block "agy $sub --help" "$agy_status" "$sub_out"

  # One level deeper, which is where the interesting ones live: `agy mcp`
  # is a group, and its add/list/remove are what write and read the file
  # pkg/agent/antigravity depends on.
  for inner in $(printf '%s\n' "$sub_out" | scrub | subcommands_of); do
    [[ "$inner" != "help" ]] || continue
    agy_capture "$help_home" "$sub" "$inner" --help
  done
done

# ------------------------------------------------------ interactive mode

printf '## Interactive mode\n\n'

printf 'The listing above is one flag set, and `pkg/agent/antigravity` builds two command lines out of it: `agyArgs` for an unattended run, and `TerminalCommand` for a session somebody types into. The second was derived from the first by removing the flags that make a run headless -- the right derivation, and until this section existed, one asserted against no binary at all. What is below is agy'"'"'s own answer to each half of it.\n\n'

printf 'First, the flags themselves, in agy'"'"'s own words. Every flag this repository passes in either command line, with the line `agy --help` prints for it: a flag that stops appearing here is one a session or a run is about to be refused over.\n\n'

# Named rather than discovered, and this is the one list in this file
# that is: it is what pkg/agent/antigravity passes, so it is maintained
# beside that package. surface_capture_test.go is what keeps it honest --
# a flag that package passes and this list does not name fails there.
grain_flags=(
  --dangerously-skip-permissions
  --add-dir
  --model
  --effort
  --input-format
  --output-format
  --print-timeout
  --disable-slash-commands
  --conversation
)
summary=""
for flag in "${grain_flags[@]}"; do
  line="$(printf '%s\n' "$help_out" | scrub | grep -m1 -E "^[[:space:]]+${flag}([[:space:]]|$)" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  summary+="${line:-$flag  [not in agy --help]}"$'\n'
done
block "the flags pkg/agent/antigravity passes, as agy --help describes them" 0 "$summary"

printf 'Every flag a session drops says "print mode" in its own description, which is the whole argument for dropping it -- `--input-format`, `--output-format` and `--print-timeout` exist to make a headless run parseable and to bound one nobody is watching. `--disable-slash-commands` says it too, and is the one that is a judgement rather than a mechanical removal: what it disables is half of what somebody sits down at this CLI for, and `/exit` is one of the three ways a session ends at all (`pkg/console`).\n\n'

printf 'So: what does agy do with none of those flags? It opens its own terminal interface, and the probe below is what that looks like to something that is not a terminal -- stdin is `/dev/null` and `TERM=dumb`, which is as close as a capture can get to the thing without one.\n\n'

int_home="$(configured_home)"
agy_run "$int_home" </dev/null
block "agy" "$agy_status" "$agy_output"

printf 'And the slash commands, which is what dropping `--disable-slash-commands` is *for*. Print mode answers a few of them without starting a turn, refuses the interactive-only ones by name, and stops resolving any of them at all under the flag a run passes. Three probes, in that order:\n\n'

agy_capture "$(configured_home)" -p "/help"
agy_capture "$(configured_home)" -p "/exit"
agy_capture "$(configured_home)" --disable-slash-commands -p "/help"

printf 'The third of those is the flag a run passes, over the first one'"'"'s command: with slash expansion off, `/help` is not a command any more but literal text for the model, so what it buys is a model turn -- which fails here for want of a real credential, and that failure is the measurement.\n\n'

printf 'A command print mode refuses *by name* is a command that exists and belongs to the session, which is the evidence `TerminalCommand` needs about `/exit` and the only place it can be got from: a session'"'"'s slash commands are typed into that terminal interface rather than printed by any probe.\n\n'

# --------------------------------------------- what agy documents

printf '## What agy documents about itself\n\n'

printf 'The changelog is the closest thing to release notes there is: a key or a flag appearing here is the announcement of it. It used to ship *in* the binary, but since 1.4.9 `agy changelog` fetches the vendor'"'"'s current notes when it has a network and falls back to its own copy when it has none -- so a 1.4.9 binary online lists releases newer than itself. Those entries are cut here, so that the section describes the binary the image pins and does not change every time agy publishes a release.\n\n'
agy_run "$(fresh_home)" changelog
block "agy changelog" "$agy_status" "$(printf '%s\n' "$agy_output" | through_version "$agy_version")"

printf 'The model catalog. Every name here carries a reasoning effort, but that is the catalog'"'"'s spelling rather than the only one agy accepts: `--model` also takes the bare family name (`gemini-3.1-pro`) as long as `--effort` names the other half, which is the pair `antigravity.DefaultModel` and `antigravity.DefaultEffort` ship as. So a deployment may put either form in Settings -- a name from this list, or a family name from it with an effort beside it -- and agy refuses the two mixed.\n\n'
agy_capture "$(configured_home)" models

# ----------------------------------------------------------- a fresh HOME

printf '## A HOME agy has never seen\n\n'

printf 'What the binary unpacks into an empty `HOME` the first time it runs there -- the layout `writeAgyHome` builds against, and where agy'"'"'s own customization guides come from.\n\n'

printf 'The probe is `agy agents` rather than `agy --version`, because *which* command is run decides whether there is anything to look at: on 1.1.26 neither `--version` nor `--help` nor `mcp list` touches the filesystem at all -- the first capture ran `--version` and found an empty directory, and reported that agy unpacks nothing -- while `agy agents` writes the whole tree below and `agy changelog` writes four entries of it. So the answer this section gives is "what a command that reads its customizations unpacks", which is the case `writeAgyHome` is building for.\n\n'

layout_home="$(fresh_home)"
agy_run "$layout_home" agents
layout="$(cd "$layout_home" && find . -mindepth 1 -printf '%y %P\n' 2>/dev/null | sort)"
block "agy agents; find \$HOME -mindepth 1 -printf '%y %P' | sort" 0 "$layout"

# Those guides are the only documentation agy publishes for the things
# this repository configures -- hooks.md is where hookConfigJSON's
# contract is quoted from -- so they are carried here in full rather than
# linked to, since a sandbox that needs them can fetch nothing.
printf '### The guides it unpacks\n\n'
guides="$(cd "$layout_home" && find . -type f -name '*.md' -size -128k 2>/dev/null | sort)"
if [[ -z "$guides" ]]; then
  printf 'It unpacked no Markdown at all into a fresh HOME.\n\n'
else
  while IFS= read -r guide; do
    [[ -n "$guide" ]] || continue
    printf '#### `%s`\n\n' "${guide#./}"
    printf '%smarkdown\n' "$FENCE"
    scrub <"$layout_home/${guide#./}" | limit
    printf '%s\n\n' "$FENCE"
  done <<<"$guides"
fi

# --------------------------------------------------- what it reads back

printf '## What it reads back\n\n'

printf 'Print mode answers `/permissions` and `/hooks` without an agent turn, which is the only way this repository can check that the files `pkg/agent/antigravity` writes are files agy actually loads. All three are planted below in the shape that package writes them, in a HOME configured the way `writeAgyHome` configures an API-key run -- which is what it takes to get an answer at all: 1.1.26 asks for a credential before it will report what it loaded, and takes the placeholder one (see `API_KEY_PLACEHOLDER`).\n\n'

read_home="$(fresh_home)"
plant "$read_home/$CLI_SETTINGS_REL" \
  '{"permissions":{"allow":["mcp_grain-sandbox_run_command"],"deny":["run_command","write_to_file"]},"modelProvider":"gemini"}'
plant "$read_home/.gemini/config/hooks.json" \
  '{"agy-surface-probe":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/bin/true","timeout":30}]}]}}'
# `tools` is an object keyed by tool name, not a list -- the shape
# eagerToolsConfig builds. The first capture planted `"tools": []` here and
# `agy mcp list` answered "No MCP servers configured", which is not a
# missing server but a *dropped* one: an entry whose `tools` is the wrong
# JSON type takes the whole server down without a word. That silence is
# recorded as a finding of its own further down.
plant "$read_home/.gemini/config/mcp_config.json" \
  '{"mcpServers":{"agy-surface-probe":{"command":"/bin/true","args":[],"timeoutSeconds":7200,"tools":{"run_command":{"eager":true}}}}}'

printf '### The permission rules it loaded\n\n'
agy_capture "$read_home" -p /permissions

printf '### The hooks it loaded\n\n'
agy_capture "$read_home" -p /hooks

printf '### The MCP servers it loaded\n\n'
agy_capture "$read_home" mcp list

printf '### The session it opens\n\n'
printf 'The same argv `Framework.Run` builds, against the same private-HOME shape, with a throwaway prompt. The placeholder key is not a key, so the model call fails and the run ends in an `error_message` step -- what is wanted is the `init` event agy emits before any of that, which carries the permission mode and the *native* tool roster that `withheldNativeTools` has to keep up with.\n\n'

init_raw="$(printf '%s\n' '{"event":"user","message":{"role":"user","content":"hello"}}' |
  HOME="$read_home" NO_COLOR=1 TERM=dumb CI=1 \
  GEMINI_API_KEY="$API_KEY_PLACEHOLDER" timeout "$TIMEOUT" "$AGY" \
    --input-format stream-json --output-format stream-json \
    --dangerously-skip-permissions --disable-slash-commands \
    --print-timeout 20s 2>&1)"
init_status=$?
block "agy --input-format stream-json --output-format stream-json --dangerously-skip-permissions --disable-slash-commands --print-timeout 20s" \
  "$init_status" "$init_raw"

if command -v jq >/dev/null 2>&1; then
  # The count is printed as well as the names because it is the number
  # this repository quotes in prose -- pkg/agent/antigravity's
  # withheldNativeTools comment and the README both state it -- and a
  # count in a document is checkable in a way a wall of names is not.
  roster="$(printf '%s\n' "$init_raw" | jq -r 'select(.event=="init") | "permission_mode: " + (.init.permission_mode // "-"), "native tools: " + (.init.tools // [] | length | tostring), (.init.tools // [] | sort | .[])' 2>/dev/null)"
  block "that init event's own roster, sorted" 0 "$roster"
fi

# ------------------------------------------------------ which paths win

printf '## Which paths it reads\n\n'

printf 'Candidate locations, planted all at once in one HOME, each holding a marker named after the directory it sits in: whichever markers come back are the paths agy reads, and the rest are places this repository must not write. The mistake this catches is `~/.gemini/settings.json`, which Gemini CLI read and agy ignores in silence.\n\n'

agent_dirs=(
  ".gemini/antigravity-cli/agents"
  ".gemini/agents"
  ".gemini/config/agents"
  ".agy/agents"
  ".config/agy/agents"
  ".antigravity/agents"
)
paths_home="$(fresh_home)"
agent_names=()
for dir in "${agent_dirs[@]}"; do
  name="probe-$(printf '%s' "$dir" | tr './' '--')"
  agent_names+=("$name")
  plant "$paths_home/$dir/$name.md" "---
name: $name
description: a marker planted by scripts/agy-surface.sh in $dir
---

Nothing. This agent exists so that \`agy agents\` listing it proves the directory is read."
done

printf '### Custom agents\n\n'
agy_run "$paths_home" agents
agents_out="$agy_output"
block "agy agents" "$agy_status" "$agents_out"

summary=""
for i in "${!agent_dirs[@]}"; do
  verdict="not listed"
  [[ "$agents_out" == *"${agent_names[$i]}"* ]] && verdict="READ"
  summary+="$(printf '%-10s ~/%s' "$verdict" "${agent_dirs[$i]}")"$'\n'
done
block "which planted agent directories were read" 0 "$summary"

mcp_files=(
  ".gemini/config/mcp_config.json"
  ".gemini/settings.json"
  ".gemini/antigravity-cli/mcp_config.json"
  ".gemini/antigravity-cli/settings.json"
  ".config/agy/mcp_config.json"
  ".agy/mcp_config.json"
)
mcp_home="$(fresh_home)"
mcp_names=()
for file in "${mcp_files[@]}"; do
  name="probe-$(printf '%s' "$file" | tr './' '--')"
  mcp_names+=("$name")
  plant "$mcp_home/$file" "{\"mcpServers\":{\"$name\":{\"command\":\"/bin/true\",\"args\":[]}}}"
done

printf '### MCP servers\n\n'
agy_run "$mcp_home" mcp list
mcp_out="$agy_output"
block "agy mcp list" "$agy_status" "$mcp_out"

summary=""
for i in "${!mcp_files[@]}"; do
  verdict="not listed"
  [[ "$mcp_out" == *"${mcp_names[$i]}"* ]] && verdict="READ"
  summary+="$(printf '%-10s ~/%s' "$verdict" "${mcp_files[$i]}")"$'\n'
done
block "which planted MCP config files were read" 0 "$summary"

# Hooks were the section this document promised and did not have, and
# they are the one where planting the candidates together and planting
# them one at a time give *different* answers -- so both are asked. The
# difference is the finding: hooks.json at the antigravity-cli path
# replaces the one at the config path rather than merging with it, so a
# file grain does not write, in a HOME grain does not own, would take
# grain's hook out of a run in silence. (Agents and MCP servers were
# checked the same way and answer identically either way, which is why
# they are asked once.)
hooks_files=(
  ".gemini/config/hooks.json"
  ".gemini/antigravity-cli/hooks.json"
  ".gemini/hooks.json"
  ".config/agy/hooks.json"
  ".agy/hooks.json"
)
hook_json() {
  printf '{"%s":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/bin/true","timeout":30}]}]}}' "$1"
}

printf '### Hooks\n\n'
printf 'The file `hookConfigJSON` writes, and the one denial in this whole document that agy documents as a denial. Planted in a HOME that also carries the settings agy needs before `-p /hooks` will answer.\n\n'

hooks_home="$(configured_home)"
hook_names=()
for file in "${hooks_files[@]}"; do
  name="probe-$(printf '%s' "$file" | tr './' '--')"
  hook_names+=("$name")
  plant "$hooks_home/$file" "$(hook_json "$name")"
done

agy_run "$hooks_home" -p /hooks
hooks_out="$agy_output"
block "agy -p /hooks" "$agy_status" "$hooks_out"

summary=""
for i in "${!hooks_files[@]}"; do
  verdict="not listed"
  [[ "$hooks_out" == *"${hook_names[$i]}"* ]] && verdict="READ"
  summary+="$(printf '%-10s ~/%s' "$verdict" "${hooks_files[$i]}")"$'\n'
done
block "which planted hooks files were read, all planted together" 0 "$summary"

printf 'And the same candidates one at a time, each in a HOME of its own. A path that reads `READ` here and `not listed` above is not a path agy ignores -- it is a path something else suppressed.\n\n'

summary=""
for file in "${hooks_files[@]}"; do
  name="probe-$(printf '%s' "$file" | tr './' '--')"
  alone_home="$(configured_home)"
  plant "$alone_home/$file" "$(hook_json "$name")"
  agy_run "$alone_home" -p /hooks
  verdict="not listed"
  [[ "$agy_output" == *"$name"* ]] && verdict="READ"
  summary+="$(printf '%-10s ~/%s' "$verdict" "$file")"$'\n'
done
block "which planted hooks files were read, each on its own" 0 "$summary"

# Settings is the shortest of the four and the one with the most history:
# ~/.gemini/settings.json is where Gemini CLI kept this file, and a
# deployment that writes there gets no rules, no modelProvider and no
# complaint. The marker is a deny rule named after its own path, so
# -p /permissions reporting it is what proves the file was read.
printf '### Settings\n\n'
printf 'The file `settingsJSON` writes. Each candidate holds a `permissions.deny` rule named after its own path *and* the `modelProvider` that lets agy answer at all -- so a path that is not read fails twice over, with neither its rule nor its credential setting taking effect.\n\n'

settings_files=(
  ".gemini/antigravity-cli/settings.json"
  ".gemini/settings.json"
  ".gemini/config/settings.json"
  ".config/agy/settings.json"
  ".agy/settings.json"
)
settings_home="$(fresh_home)"
settings_names=()
for file in "${settings_files[@]}"; do
  name="probe-$(printf '%s' "$file" | tr './' '--')"
  settings_names+=("$name")
  plant "$settings_home/$file" "{\"permissions\":{\"deny\":[\"$name\"]},\"modelProvider\":\"gemini\"}"
done

agy_run "$settings_home" -p /permissions
settings_out="$agy_output"
block "agy -p /permissions" "$agy_status" "$settings_out"

summary=""
for i in "${!settings_files[@]}"; do
  verdict="not listed"
  [[ "$settings_out" == *"${settings_names[$i]}"* ]] && verdict="READ"
  summary+="$(printf '%-10s ~/%s' "$verdict" "${settings_files[$i]}")"$'\n'
done
block "which planted settings files were read" 0 "$summary"

# ------------------------------------------- what it drops without a word

printf '## What it drops without a word\n\n'

printf 'The failure mode this file opens with, as a measurement rather than a warning. Each row is one `mcp_config.json` differing from the one above it in a single key, and the question is whether `agy mcp list` still has a server to show: a *known* key given a value of the wrong JSON type does not produce an error, a warning or a partial load -- it takes the whole server entry with it. `eagerToolsConfig` writing `tools` as an object rather than a list is the difference between grain'"'"'s eleven MCP tools being there and a run having no tools at all, and nothing but this probe would say so.\n\n'

summary=""
drop_probe() {
  local label="$1" entry="$2" home verdict
  home="$(fresh_home)"
  plant "$home/.gemini/config/mcp_config.json" "{\"mcpServers\":{\"agy-surface-probe\":$entry}}"
  agy_run "$home" mcp list
  verdict="DROPPED"
  [[ "$agy_output" == *"agy-surface-probe"* ]] && verdict="loaded"
  summary+="$(printf '%-8s %s' "$verdict" "$label")"$'\n'
}
drop_probe 'the minimum: command, args'                  '{"command":"/bin/true","args":[]}'
drop_probe 'plus timeoutSeconds, as grain writes it'     '{"command":"/bin/true","args":[],"timeoutSeconds":7200}'
drop_probe 'plus tools as an object, as grain writes it' '{"command":"/bin/true","args":[],"timeoutSeconds":7200,"tools":{"run_command":{"eager":true}}}'
drop_probe 'but tools as an empty list'                  '{"command":"/bin/true","args":[],"tools":[]}'
drop_probe 'but tools as a list of names'                '{"command":"/bin/true","args":[],"tools":["run_command"]}'
drop_probe 'but timeoutSeconds as a string'              '{"command":"/bin/true","args":[],"timeoutSeconds":"7200"}'
drop_probe 'plus a key agy has never heard of'           '{"command":"/bin/true","args":[],"noSuchKeyAtAll":true}'
block "does agy mcp list still show the server" 0 "$summary"

# ---------------------------------------------------- the string table

printf '## The config schema, out of the string table\n\n'

printf 'agy is stripped, but a Go binary carries every struct tag, every JSON-schema description and every literal path its own code names. This is where the keys a settings file may use come from -- with the caveat this file opens with: an unknown key is ignored in silence, so a key appearing here is not proof that writing it does anything, and a *known* key given a value that does not parse can drop a whole agent without a word.\n\n'

printf 'Two things shape the patterns below, and the first capture is what taught them both.\n\n'

printf 'The `json:"..."` tags are not dumped whole. There are ten thousand distinct ones in this binary, because a 200MB Go program that bundles an AWS SDK, a JSON-schema library and most of a browser carries their struct tags too; dumped whole they truncate in alphabetical order, and the first capture spent four hundred lines on `AbsoluteKeywordLocation` and `AccessKeyID` under a heading promising the keys a settings file may use. They are asked for by subject instead -- tools, permissions, hooks, MCP, agents, sandboxing -- which is both shorter and the actual question.\n\n'

printf 'And the camelCase probes are anchored to whole strings (`^...$`). A Go string table has no delimiters between its entries, so an unanchored pattern reads straight across the join and invents names: the first capture'"'"'s roster section offered `allowedAmountfeeParametersgetFeeBalanc` and `hookStubInputBlockingCharzend`, neither of which is anything. Anchoring costs the names that appear only inside a longer literal and buys a list where every entry is a name some Go code actually uses.\n\n'

extract 'json tags naming a tool'       'json:"[A-Za-z0-9_.,-]*[Tt]ool[A-Za-z0-9_.,-]*"'
extract 'json tags naming a permission' 'json:"[A-Za-z0-9_.,-]*([Pp]ermission|[Dd]eny|[Aa]llow)[A-Za-z0-9_.,-]*"'
extract 'json tags naming a hook'       'json:"[A-Za-z0-9_.,-]*[Hh]ook[A-Za-z0-9_.,-]*"'
extract 'json tags naming an MCP server' 'json:"[A-Za-z0-9_.,-]*([Mm]cp|MCP)[A-Za-z0-9_.,-]*"'
extract 'json tags naming an agent or a skill' 'json:"[A-Za-z0-9_.,-]*([Aa]gent|[Ss]kill)[A-Za-z0-9_.,-]*"'
extract 'json tags naming a sandbox'    'json:"[A-Za-z0-9_.,-]*[Ss]andbox[A-Za-z0-9_.,-]*"'
extract 'yaml:"..."'                   'yaml:"[A-Za-z0-9_.,-]+"'
extract 'mapstructure:"..."'           'mapstructure:"[A-Za-z0-9_.,-]+"'
extract 'jsonschema_description:"..."' 'jsonschema_description:"[^"]{1,120}"'
extract 'paths under .gemini'          '\.gemini/[A-Za-z0-9_./-]+'
extract 'settings, config and hook file names' '/[a-z0-9_-]{0,30}(settings|config|hook|agent|permission|skill|plugin|rule|workflow)[a-z0-9_-]{0,30}\.(json|yaml|yml|md|toml)'
extract 'whole strings that read like a tool roster' '^(enabled|disabled|allowed|denied|withheld)[A-Z][A-Za-z]{2,30}$'
extract 'whole strings that read like a permission'  '^(permission|decision|hook|sandbox)[A-Z][A-Za-z]{2,30}$'
