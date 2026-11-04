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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/nsclient"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fsUsage is grain fs's help. A sandbox's grain ls, read and grep are
// the same commands over its proxy (pkg/sandboxcli); here they sit under
// fs because the host CLI's top-level verbs are already its own
// (design/spec/external-cli/external-cli.spec.md, #XC-25).
const fsUsage = `usage: grain fs <command> <uri> [flags]

Read grain's namespace (ci://, task://, deploy://, config://, memory://,
src://) over the daemon's GET /api/fs/, as a sandbox's grain ls, read and
grep read it over its proxy. A URI may carry a query of its own:
grain fs cat 'ci://acme/widgets/<run>/log?tail=4096'.

  grain fs ls <uri> [--glob <pattern>] [--json]
  grain fs cat <uri> [--tail N] [--offset N --limit N] [--lines N] [--follow]
  grain fs read <uri> ...           the same as cat
  grain fs grep <regex> <uri> [-i] [-n] [-C N] [--glob <pattern>] [--max N] [--json]

--tail, --offset and --limit count bytes; --lines N prints the last N
lines of what was read.
`

func cmdFS(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	return runFS(ctx, c, args, out.json, os.Stdout, os.Stderr)
}

// fsClient is the namespace client over c's /api/fs/: the one a
// sandbox's grain ls, read and grep speak over its proxy's /fs/
// (pkg/sandboxcli), with c's credential and its errors.
func fsClient(c *ui.HTTPClient, stdout, stderr io.Writer) *nsclient.Client {
	return &nsclient.Client{
		Base: c.BaseURL, Prefix: nsclient.APIPrefix,
		HTTP: &http.Client{Transport: c},
		Out:  stdout, Err: stderr,
		Refused: c.ResponseError,
	}
}

func runFS(ctx context.Context, c *ui.HTTPClient, args []string, asJSON bool, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, fsUsage)
		if len(args) == 0 {
			return usageErrorf("grain fs needs a command: ls, cat, read or grep")
		}
		return nil
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("grain fs "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, fsUsage)
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "print JSON")
	ns := fsClient(c, stdout, stderr)
	switch sub {
	case "ls":
		glob := fs.String("glob", "", "list only the entries matching this pattern")
		pos, err := parseArgs(fs, args)
		if err != nil {
			return err
		}
		uri, err := fsArg(pos, 0, "<uri>", 1)
		if err != nil {
			return err
		}
		q := nsclient.ListQuery(*glob)
		if *jsonOut || asJSON {
			q.Set("format", "json")
		}
		return fsResult(ns.Get(uri, q))
	case "cat", "read":
		tail := fs.String("tail", "", "only the last N bytes")
		offset := fs.String("offset", "", "start N bytes in")
		limit := fs.String("limit", "", "at most N bytes")
		lines := fs.Int("lines", 0, "only the last N lines")
		follow := fs.Bool("follow", false, "keep reading a file that is still growing")
		pos, err := parseArgs(fs, args)
		if err != nil {
			return err
		}
		uri, err := fsArg(pos, 0, "<uri>", 1)
		if err != nil {
			return err
		}
		q := url.Values{}
		for name, v := range map[string]string{"tail": *tail, "offset": *offset, "limit": *limit} {
			if v == "" {
				continue
			}
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return usageErrorf("grain fs %s: --%s %q is not a count: it takes a non-negative integer", sub, name, v)
			}
			q.Set(name, v)
		}
		if *follow {
			q.Set("follow", "1")
		}
		return fsResult(ns.Read(uri, q, nsclient.ReadOptions{LastLines: *lines}))
	case "grep":
		ignoreCase := fs.Bool("i", false, "match case-insensitively")
		fs.Bool("n", true, "print line numbers (always on)")
		contextLines := fs.String("C", "", "lines of context around each match")
		glob := fs.String("glob", "", "search only the files matching this pattern")
		maxMatches := fs.String("max", "", "stop after N matches")
		pos, err := parseArgs(fs, args)
		if err != nil {
			return err
		}
		pattern, err := fsArg(pos, 0, "<regex>", 2)
		if err != nil {
			return err
		}
		uri, err := fsArg(pos, 1, "<uri>", 2)
		if err != nil {
			return err
		}
		for name, v := range map[string]string{"C": *contextLines, "max": *maxMatches} {
			if v == "" {
				continue
			}
			if n, err := strconv.Atoi(v); err != nil || n < 0 || (name == "max" && n == 0) {
				return usageErrorf("grain fs grep: -%s %q is not a count", name, v)
			}
		}
		q := nsclient.GrepQuery(pattern, nsclient.GrepOptions{
			IgnoreCase: *ignoreCase, Context: *contextLines, Glob: *glob, Max: *maxMatches,
		})
		if !*jsonOut && !asJSON {
			return fsResult(ns.Grep(uri, q))
		}
		return fsResult(ns.GrepTo(uri, q, func(res nsclient.GrepResult) error {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}))
	}
	fmt.Fprint(stderr, fsUsage)
	return usageErrorf("grain fs: unknown command %q: it takes ls, cat, read or grep", sub)
}

// fsArg is the i'th of want positional arguments, named name.
func fsArg(pos []string, i int, name string, want int) (string, error) {
	if len(pos) > want {
		return "", usageErrorf("grain fs: unexpected argument %q", pos[want])
	}
	if i >= len(pos) || pos[i] == "" {
		return "", usageErrorf("grain fs: missing %s", name)
	}
	return pos[i], nil
}

// fsResult is err as this CLI reports it: a grep that searched only part
// of what it was asked to is a failed answer (exit 1), as a sandbox's is.
func fsResult(err error) error {
	var notSearched *nsclient.NotSearchedError
	if errors.As(err, &notSearched) {
		return targetFailed(err)
	}
	return err
}
