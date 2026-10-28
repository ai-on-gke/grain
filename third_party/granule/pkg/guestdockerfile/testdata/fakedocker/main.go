// Command fakedocker stands in for the docker CLI in tests of the
// guestdockerfile package: it records the invocation (argv, one line, to
// the file named by FAKEDOCKER_LOG) and the Dockerfile it was handed on
// stdin (to FAKEDOCKER_LOG + ".stdin"), so the whole validate/supply the
// base/build sequence can be asserted without a docker daemon.
//
// It builds nothing and answers no queries, because there is nothing
// else guestdockerfile asks docker for: one `docker build`, with the
// file granule generated on its standard input.
//
//   - FAKEDOCKER_FAIL_CONTAINS fails the call when its argv (joined with
//     spaces) contains it, which is what a build whose RUN exited
//     non-zero looks like from out here.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]

	logPath := os.Getenv("FAKEDOCKER_LOG")
	if logPath != "" {
		// \x1f between arguments so one containing spaces is still one
		// argument when the test reads the line back.
		if err := os.WriteFile(logPath, []byte(strings.Join(args, "\x1f")), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		stdin, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(logPath+".stdin", stdin, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	if want := os.Getenv("FAKEDOCKER_FAIL_CONTAINS"); want != "" && strings.Contains(strings.Join(args, " "), want) {
		fmt.Fprintf(os.Stderr, "fakedocker: failing on request: %s\n", want)
		os.Exit(1)
	}
}
