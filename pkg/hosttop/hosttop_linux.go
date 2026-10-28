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

//go:build linux

package hosttop

// This is procps' top, the one in the deployment image. Its flags and its
// output are unchanged from before pkg/hosttop was split per platform --
// the split exists to stop macOS from being handed these flags, not to
// revisit them.

// iterations is how many samples `top` is asked for, and why it is not
// one: on its first iteration top has no previous sample to difference
// against, so every %CPU it prints is that process's share of CPU time
// since it *started*, which for a long-lived daemon is a number about
// the past week rather than about now. The second iteration is a real
// delta over the interval below, and is the one Read keeps.
const iterations = "2"

// interval is the delay between those two samples, in seconds as top's
// own -d spells it. It is what every %CPU in the output is measured
// over, and it is paid on every request -- so it is short enough not to
// be felt next to the pane's own refresh cadence, and long enough to
// average over more than a scheduler tick.
const interval = "0.5"

// width is top's -w: in batch mode top otherwise truncates each row to 80
// columns, which cuts the COMMAND column down to a few characters and
// loses exactly the part an operator is reading the pane for. The UI
// scrolls the block horizontally rather than wrapping it (.logs-view).
const width = "512"

// sampleHeader opens each of procps' iterations: "top - 10:11:31 up 3
// days, ...". lastSample looks for the last of these.
const sampleHeader = "top - "

// topArgs is the procps invocation. lines goes unused here: procps top
// prints every process and Read's trim does the capping, which is also
// what keeps the cap honest when a future caller wants the whole table.
//
// -b is batch mode: no cursor addressing, no tty required, plain lines on
// stdout. The + prefix on +%CPU explicitly selects descending order;
// naming the field alone can retain the direction from top's
// configuration (~/.toprc) and put idle processes before busy ones.
func topArgs(lines int) ([]string, error) {
	return []string{"-b", "-n", iterations, "-d", interval, "-w", width, "-o", "+%CPU"}, nil
}
