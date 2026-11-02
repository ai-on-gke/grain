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

// Command checkpost receives CI verdicts and serves them back.
//
// It is one half of taking grain's merge queue off the GitHub API
// (docs/leaving-the-github-api.md): instead of the queue asking GitHub
// every thirty seconds how a build ended, the build says so once, here.
//
// Two subcommands, deliberately in one binary:
//
//	checkpost serve    the service
//	checkpost report   a client that reports one verdict to it
//
// The client is here rather than being left to `curl` because the
// signing scheme would then have two implementations -- one in Go and
// one in shell -- which would drift the first time either was touched.
// A workflow step can still use curl if it prefers, and
// .github/workflows shows how; this is the version that cannot get the
// HMAC wrong.
//
// Nothing here knows what a task is, what a sandbox is, or what grain's
// store looks like, and it holds no GitHub credential -- it never calls
// GitHub, only receives from it. That is what makes running it somewhere
// other than the daemon a deployment decision rather than a refactor.
// See pkg/checkpost's own doc comment, and design/design/architecture.design.md.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/checkpost"
)

// pruneInterval is how often expired verdicts are swept. An hour against
// a week of retention: the sweep is cheap, and nothing depends on it
// being prompt.
const pruneInterval = time.Hour

// secretEnv is where the shared secret is read from, in both modes.
//
// The environment rather than a flag, because a flag is in the process
// table and in whatever shell history or CI log ran the command, and
// this secret is the whole of what stands between a public endpoint and
// anybody who wants to tell grain a build passed.
const secretEnv = "CHECKPOST_SECRET"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "report":
		err = report(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "checkpost: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkpost: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `checkpost receives CI verdicts and serves them back.

  checkpost serve  -addr :8080 -store /var/lib/checkpost/verdicts.jsonl
  checkpost report -url https://... -repo owner/name -commit <sha> \
                   -name go -conclusion success [-check-url https://...]

Both read the shared secret from `+secretEnv+`.
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	store := fs.String("store", "", "file to keep verdicts in; empty keeps them in memory only, "+
		"which loses them on restart and is not a way to deploy this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	secret := os.Getenv(secretEnv)
	if secret == "" {
		// Refused at startup rather than at the first request: a service
		// that came up and then rejected everything would look healthy to
		// whatever started it, and the operator would find out from a
		// workflow failing hours later.
		return fmt.Errorf("%s is not set, so nothing could be authenticated. Set it to the same "+
			"value the reporting side signs with", secretEnv)
	}
	if *store == "" {
		fmt.Fprintln(os.Stderr, "checkpost: no -store, so verdicts are kept in memory only and a "+
			"restart loses them. A pull request whose checks passed then reads as silence.")
	}

	st, err := checkpost.OpenStore(*store)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := checkpost.NewServer(st, secret)
	stop := make(chan struct{})
	go srv.PruneEvery(pruneInterval, stop)

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv,
		// This endpoint is reachable from the internet, so every phase of
		// a request is bounded: a client that opens a connection and
		// sends nothing must not be able to hold one open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		close(stop)
		httpSrv.Close()
	}()

	fmt.Fprintf(os.Stderr, "checkpost: listening on %s\n", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// reportTimeout bounds the client's whole attempt. It runs as the last
// step of a workflow, where a call that hangs holds a runner open and
// eventually fails the job it is reporting on.
const reportTimeout = 30 * time.Second

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	url := fs.String("url", "", "base url of the checkpost service")
	repo := fs.String("repo", "", "owner/name")
	commit := fs.String("commit", "", "the full 40-character sha the checks ran against")
	name := fs.String("name", "", "the check's name, as a person would see it")
	conclusion := fs.String("conclusion", "", "success, failure, cancelled, timed_out, skipped, "+
		"neutral, action_required or startup_failure")
	checkURL := fs.String("check-url", "", "where a person goes to look; optional")
	attempt := fs.Int("attempt", 0, "the workflow run attempt (GitHub's run_attempt); "+
		"a re-run's success replaces a failure only when this is higher than the failure's")
	if err := fs.Parse(args); err != nil {
		return err
	}

	secret := os.Getenv(secretEnv)
	if secret == "" {
		return fmt.Errorf("%s is not set", secretEnv)
	}
	if *url == "" {
		return fmt.Errorf("-url is required")
	}

	v := checkpost.Verdict{
		Repo:       *repo,
		Commit:     *commit,
		Name:       *name,
		Conclusion: *conclusion,
		URL:        *checkURL,
		Attempt:    *attempt,
	}
	// Validated here as well as by the server, so a workflow that has
	// its arguments wrong is told by the step that ran rather than by a
	// 400 whose body somebody has to go and read.
	if err := v.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}

	ts := time.Now()
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimSuffix(*url, "/")+"/v1/verdicts", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(checkpost.TimestampHeader, fmt.Sprint(ts.Unix()))
	req.Header.Set(checkpost.SignatureHeader, checkpost.Sign(secret, ts, http.MethodPost, "/v1/verdicts", body))

	resp, err := (&http.Client{Timeout: reportTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// The server's own sentence, which for a 400 names the field that
		// is wrong. Bounded because this is somebody else's process
		// writing into this one's stderr.
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("the service answered %d: %s", resp.StatusCode, strings.TrimSpace(string(said)))
	}
	return nil
}
