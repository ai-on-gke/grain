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

// args.go is one rule, applied to every command in this CLI: a word the
// command has no use for is an error, not something to drop on the
// floor.
//
// Go's flag package stops at the first word that is not a flag and
// leaves the rest in fs.Args(), and until this existed most commands
// here simply never looked at what was left. `grain pause -set "the
// queue is wedged, back in an hour"` is what that cost: -set is a
// boolean and the reason belongs to -reason, so the sentence the
// operator typed was discarded, the pause went into the store as "paused
// by hand", and neither the UI's banner nor the next reader of `grain
// pause` had any idea why this deployment had stopped. The natural
// spelling of the command was the one that lost the message (found by
// hand, task 209). `grain list junk`, `grain config junk` and `grain get
// 1 extra` were the same silence, more cheaply.
//
// So each command below says how many positional words it reads, and
// refuses whatever is past that. The two kinds of leftovers are the same
// question: rest is fs.Args() for a command that calls Parse itself, and
// parseArgs' own return for one whose flags may come after its
// arguments.
//
// The commands that take an unbounded tail on purpose say nothing here,
// because every word after the id is theirs: `grain comment <id>
// <body...>` is the one, and misplacedFlag in main.go is what that
// command does instead of counting.
package cli

import (
	"flag"
	"fmt"
	"strings"
)

// noArgs refuses every leftover word on a command that reads none of
// them -- the flag-only commands, which is most of this CLI.
func noArgs(fs *flag.FlagSet, rest []string) error {
	return atMostArgs(fs, rest, 0)
}

// atMostArgs refuses the words past the want'th on a command that reads
// a fixed number of them, so `grain get 1 extra` says so rather than
// showing task 1 as though "extra" had not been typed.
//
// It counts rather than looks: a command whose argument is required
// still rejects too few for itself (taskID and its siblings), with a
// message about the thing that is missing rather than about arithmetic.
func atMostArgs(fs *flag.FlagSet, rest []string, want int) error {
	return atMostArgsFor(fs.Name(), rest, want)
}

// noArgsFor and atMostArgsFor are the same two checks for the handful of
// commands that have no flag set of their own to take a name from:
// `grain state status` and its siblings, dispatched straight off the
// word after `grain state` and handed nothing else.
func noArgsFor(name string, rest []string) error {
	return atMostArgsFor(name, rest, 0)
}

func atMostArgsFor(name string, rest []string, want int) error {
	if len(rest) <= want {
		return nil
	}
	return usageErrorf("%s takes %s, so %q would be ignored -- a value goes with the flag it belongs to",
		name, argumentCount(want), rest[want])
}

// argumentCount spells how many arguments a command takes, for that
// message. Written out in words up to the largest count any command here
// has, because "grain capability takes 3 arguments" reads like a
// diagnostic and "takes three arguments" reads like a sentence.
func argumentCount(n int) string {
	switch n {
	case 0:
		return "no arguments"
	case 1:
		return "one argument"
	case 2:
		return "two arguments"
	case 3:
		return "three arguments"
	default:
		return fmt.Sprintf("%d arguments", n)
	}
}

// globalFlagNames are runCLI's own flags, the ones #XC-11 lets a person
// write after the command as readily as before it: `grain list -json`
// and `grain ci show ci-42 --json` as well as `grain -json list`.
var globalFlagNames = map[string]bool{
	"json": true, "server": true, "token": true, "audience": true, "as": true,
	"connect-timeout": true, "timeout": true, "context": true,
}

// freeTextCommands take a body of free text after their id, where a word
// like "-as" is the sentence's and not a flag (`grain comment 5 file
// this -as a bug`). A global flag is lifted from them only before the
// first word that is not a flag.
var freeTextCommands = map[string]bool{"comment": true, "reply": true, "broadcast": true, "notify": true}

// splitFlag reads one argument as a flag: its name without the dashes
// (one or two, the same either way), and the value written after an "="
// if there was one. name is "" for a word that is not flag-shaped.
func splitFlag(arg string) (name, value string, hasValue bool) {
	if len(arg) < 2 || arg[0] != '-' || arg == "--" {
		return "", "", false
	}
	name = strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	if name == "" || name[0] == '-' || name[0] == '=' {
		return "", "", false
	}
	name, value, hasValue = strings.Cut(name, "=")
	return name, value, hasValue
}

// liftGlobalFlags sets fs's global flags from wherever they were written
// among a command's own arguments, and returns those arguments without
// them -- so no subcommand has to define -json, -server and the rest a
// second time to accept them after its name. Everything after a bare
// "--" is left alone, as it is everywhere else.
func liftGlobalFlags(fs *flag.FlagSet, cmd string, args []string) ([]string, error) {
	kept := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			kept = append(kept, args[i:]...)
			break
		}
		name, value, hasValue := splitFlag(arg)
		if name == "" && freeTextCommands[cmd] {
			kept = append(kept, args[i:]...)
			break
		}
		if !globalFlagNames[name] {
			kept = append(kept, arg)
			continue
		}
		if !hasValue {
			if bf, ok := fs.Lookup(name).Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				value = "true"
			} else if i+1 < len(args) {
				i++
				value = args[i]
			} else {
				return nil, usageErrorf("flag needs an argument: -%s", name)
			}
		}
		if err := fs.Set(name, value); err != nil {
			return nil, usageErrorf("invalid value %q for flag -%s: %v", value, name, err)
		}
	}
	return kept, nil
}

// parseArgs is flag.FlagSet.Parse for a command that takes positional
// arguments as well as flags, and it exists because Go's own Parse stops
// at the first word that is not a flag. Every usage line here is written
// the way people type these commands -- `grain update <id> [flags]`,
// `grain repo capabilities [-set a,b] <owner/name>` -- and with a plain
// Parse the first of those forms silently did nothing at all: the id
// ended parsing, -title was left among the leftover arguments, fs.Visit
// saw no flag set, and the command sent an empty request and printed the
// unchanged task as though it had worked. `grain repo capabilities
// acme/widgets -set self-debug` was the same shape of quiet failure --
// it printed the repo's capabilities instead of replacing them.
//
// So flags are parsed wherever they appear: Parse is called again on
// what is left after each positional word, which is what a GNU-style
// getopt does with permutation, and the positional words are returned in
// the order they were written. A bare "--" still ends the flags, and
// everything after it is positional verbatim -- which is how a value
// that starts with a dash is passed to a command that takes one.
//
// `grain comment <id> <body...>` deliberately does not use this: its
// body is free text, and a body word that happens to name a flag has to
// stay a body word. misplacedFlag below is what that command says
// instead of guessing.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	renamed := defineHubSubcommandRenames(fs)
	var terminated []string
	for i, arg := range args {
		if arg == "--" {
			terminated = args[i+1:]
			args = args[:i]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if err := renamed.resolve(); err != nil {
		return nil, err
	}
	return append(positional, terminated...), nil
}

// first is args[0] or "", so a command missing its one positional
// argument reports that through taskID's own error rather than panicking
// on an empty slice -- flag.FlagSet.Arg's behaviour, which parseArgs'
// callers no longer go through.
func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// misplacedFlag reports the first word in args that names a flag defined
// on fs -- a flag written after the positional arguments, where
// flag.Parse has already stopped looking at them.
func misplacedFlag(fs *flag.FlagSet, args []string) (string, bool) {
	for _, arg := range args {
		name := strings.TrimLeft(arg, "-")
		if name == arg || name == "" {
			continue // not a flag-shaped word, or a bare "--"
		}
		name, _, _ = strings.Cut(name, "=")
		if fs.Lookup(name) != nil {
			return name, true
		}
	}
	return "", false
}
