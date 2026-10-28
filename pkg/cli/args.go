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
	return fmt.Errorf("%s takes %s, so %q would be ignored -- a value goes with the flag it belongs to",
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
