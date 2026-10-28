// daemon_unknown_flags.go is how `grain daemon` reads a command line
// written by a *different build of itself* -- which, on every deployment
// scripts/setup.sh installs, is the only kind of command line it ever
// gets.
//
// The unit and the image move independently. setup.sh writes
// grain-daemon.service once, at install time, with this build's flags
// baked into its ExecStart; from then on an upgrade only rewrites the
// image ref file the unit interpolates (pkg/upgrade/image.go), so the
// flags stay frozen at whatever the installer wrote while the binary
// behind them moves on with every upgrade and every rollback. A flag
// removed from the daemon therefore meets a unit that still passes it,
// and a flag added to the unit by a newer setup.sh meets an image rolled
// back past it.
//
// Before grain/task-233 that meeting was fatal. flag.ExitOnError met
// "flag provided but not defined: -root-shell-control-dir", dumped the
// whole ~200-line usage text and exited 2; systemd restarted the service
// five seconds later and it did it again. That is what took this
// deployment down for eight minutes and 52 restarts on 2026-09-18,
// removed -root-shell-control-dir from the image (grain/task-193) having
// been the entire change -- and it buried the journal under ~10,000
// lines of flag help while it was at it, so the one place an operator
// would look for the reason was the place the reason had scrolled out
// of.
//
// So an unknown flag is read here as what it is: a configuration error,
// in a configuration this process cannot edit, about a setting it does
// not have. It is named once, loudly, with the fix beside it, and the
// daemon comes up. A deployment that is up with one ignored setting can
// be looked at, logged into and repaired; one that is in a restart loop
// can do nothing at all, and neither can anything it was supposed to be
// running.
//
// What is *not* tolerated is a bad value for a flag this build does
// define (-max-workers=banana): that is this build's own setting, said
// wrongly, and there is nothing to fall back on. It still exits -- but
// with one line rather than the usage dump, for the same journal reason.
package cli

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// maxReportedUnknownFlags bounds the "ignoring these" line. A unit and
// an image that disagree about one flag is the case this was written
// for; a unit and an image from wildly different eras could disagree
// about dozens, and a service journal is exactly where a long line per
// restart must not go (the crash dump this file exists to stop). The
// count is always exact; it is the list that is cut short.
const maxReportedUnknownFlags = 12

// readDaemonFlags parses args into fs, and reports the flags in it that
// fs does not define instead of failing over them.
//
// fs must have been created with flag.ContinueOnError: the point of this
// is that the caller decides what a parse failure means, and
// ExitOnError takes that decision away by printing the usage text and
// exiting before anything here runs.
//
// The unknown flags are removed before fs sees the line at all rather
// than by re-parsing what is left after each failure. Re-parsing would
// be simpler and wrong: flag.Value.Set is called again on every flag
// that was already set, which for a repeatable flag (stringSliceFlag,
// -granule-create-arg) appends its values a second time -- so a unit
// naming one unknown flag after its create args would have started a
// daemon that passed every `granule vm create` argument twice.
func readDaemonFlags(fs *flag.FlagSet, args []string) (unknown []string, err error) {
	known, unknown := splitDaemonFlags(fs, args)
	// Nothing this package prints comes out of the flag package itself
	// any more: fs.Parse's own failure path is PrintDefaults, and one
	// flag dump per restart is half of what made the outage this file is
	// named after so expensive. daemon() prints the error on one line.
	fs.SetOutput(io.Discard)
	return unknown, fs.Parse(known)
}

// splitDaemonFlags divides args into the ones fs defines (and their
// values) and the names of the ones it does not.
//
// It walks the line the way flag.Parse does -- so that "known" is parsed
// identically to the original -- and differs only in what it does with a
// name fs has never heard of: that name, and the argument after it when
// that argument cannot itself be a flag, are dropped. "when that
// argument cannot itself be a flag" is a guess, and it has to be: the
// only thing that knows whether an unknown flag took a value is the
// build that defined it, which is not this one. It is the right guess
// for every shape a unit file actually holds -- `-flag value` and
// `-flag=value` are dropped whole, and a boolean `-flag` followed by
// another flag drops only itself.
func splitDaemonFlags(fs *flag.FlagSet, args []string) (known, unknown []string) {
	for i := 0; i < len(args); {
		arg := args[i]
		// Everything from the first non-flag argument on is the flag
		// package's business, not this function's: flag.Parse stops
		// there too, and "--" ends the flags for both of us.
		if len(arg) < 2 || arg[0] != '-' {
			return append(known, args[i:]...), unknown
		}
		name := arg[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		// Syntax flag.Parse refuses outright ("--", "-=x", "---x"). Hand
		// it, and the rest of the line, to the parser that has the error
		// message for it.
		if name == "" || name[0] == '-' || name[0] == '=' {
			return append(known, args[i:]...), unknown
		}
		value := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value = name[:eq], true
		}
		i++
		// -h and -help are never "defined" and are not unknown either:
		// the flag package answers them itself, and a human at a
		// terminal asking for the flag list is the one caller that
		// should get the whole thing (daemon()).
		if f := fs.Lookup(name); f == nil && name != "h" && name != "help" {
			unknown = append(unknown, "-"+name)
			// Its value goes with it, when it has one that is not
			// already attached. Anything beginning with "-" is left
			// alone: dropping it would silently swallow the next real
			// flag, which is a far worse failure than reporting one
			// stray argument the parser then complains about.
			if !value && i < len(args) && !strings.HasPrefix(args[i], "-") {
				i++
			}
			continue
		}
		known = append(known, arg)
		// A defined non-boolean flag takes the next argument whatever it
		// looks like -- `-reboot-cmd touch -reboot-cmd /path/to/reboot`
		// is two values, not a value and a flag -- so it is copied
		// across without inspection, exactly as flag.Parse would consume
		// it. A missing one (a value flag at the end of the line) is
		// left for fs.Parse to report.
		if f := fs.Lookup(name); f != nil && !value && !isBoolFlag(f) && i < len(args) {
			known = append(known, args[i])
			i++
		}
	}
	return known, unknown
}

// isBoolFlag is the flag package's own test for a flag that takes no
// separate argument (`-ci` rather than `-ci true`), which it keeps to
// itself: any Value with an IsBoolFlag() bool method returning true.
func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// unknownFlagReport is what a start whose command line named flags this
// build does not define says about it: the lines written to the journal,
// and the same fact carried to the UI (ui.DaemonHealth.UnknownFlags), so
// that "this host's unit and this image disagree" is legible somewhere
// other than the first few seconds of a log.
//
// The first line names the flags and the second says what to do about
// them, in that order, because the first is what a grep for a flag name
// finds and the second is what the person who ran that grep needs next.
func unknownFlagReport(unknown []string) []string {
	if len(unknown) == 0 {
		return nil
	}
	shown := unknown
	more := ""
	if len(shown) > maxReportedUnknownFlags {
		more = fmt.Sprintf(" (and %d more)", len(shown)-maxReportedUnknownFlags)
		shown = shown[:maxReportedUnknownFlags]
	}
	return []string{
		fmt.Sprintf("WARNING: starting anyway, ignoring %d flag(s) this build does not define: %s%s",
			len(unknown), strings.Join(shown, " "), more),
		"this host's grain-daemon.service and this image disagree -- an upgrade or a rollback moved one " +
			"without the other. Re-run scripts/setup.sh on the host to rewrite the unit, or run " +
			"`grain daemon -h` for the flags this build accepts. Each ignored flag is a setting this " +
			"deployment asked for and is not getting.",
	}
}

// reportUnknownDaemonFlags writes that report to the journal, once per
// start. Two lines, whatever the disagreement -- see this file's own
// doc comment for why the number of lines is a property worth naming.
func reportUnknownDaemonFlags(unknown []string) {
	for _, line := range unknownFlagReport(unknown) {
		log.Printf("grain daemon: %s", line)
	}
}

// printDaemonFlagNames prints every flag fs defines, one per line, in
// the order flag.VisitAll gives them (lexical): the name, a space, and
// "bool" or "value" for whether it takes an argument of its own.
//
// It backs `grain daemon -flag-names`, which exists so that something
// *outside* this process can answer "would this build accept the command
// line that host's unit passes?" while there is still something to be
// done about the answer: pkg/upgrade asks the image it has just pulled
// and has not yet cut over to (checkImageFlags), and scripts/setup.sh
// asks the image it is about to write a unit for.
//
// The second column is what makes a command line readable by something
// that did not define the flags in it. `-granule-create-arg
// -disk-mode=overlay` and `-reboot-cmd touch` pass flag-shaped things as
// values, so a reader that knew only the names would report granule's
// own arguments as grain flags that do not exist -- a false alarm on
// exactly the check nobody can afford to learn to ignore.
func printDaemonFlagNames(fs *flag.FlagSet, w io.Writer) {
	fs.VisitAll(func(f *flag.Flag) {
		kind := "value"
		if isBoolFlag(f) {
			kind = "bool"
		}
		fmt.Fprintf(w, "%s %s\n", f.Name, kind)
	})
}

// daemonUsage prints the flag list a human asked for -- `grain daemon
// -h`. This is the one path that still prints the whole thing, and the
// difference between it and a failed parse is the difference between a
// terminal and a service journal.
func daemonUsage(fs *flag.FlagSet) {
	writeDaemonUsage(fs, os.Stderr)
}

// writeDaemonUsage is daemonUsage's text, written to w: what
// TestDaemonHelpUnchanged compares with testdata/daemon_help.golden.
func writeDaemonUsage(fs *flag.FlagSet, w io.Writer) {
	fs.SetOutput(w)
	fmt.Fprintf(w, "Usage of %s:\n", fs.Name())
	fs.PrintDefaults()
}
