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

// mqdriver asks the merge queue of a repository on a GitHub Enterprise
// Server the way the daemon's forge router does for any repository off
// -github-host: the credential's host record (secrets/github/<name>.host.json)
// picks the transport through gitproxy.CredentialSet.ForgeTransportOn, and
// github.RESTClient sends MergeQueueEntryOf then EnqueuePullRequest over it.
// A separate module (replace ../../../..) so ./... never builds it.
//
//	go run . -secrets DIR -credential ghes -host 127.0.0.1:8443 -repo acme/widgets -pr 7 -sha HEAD
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

func main() {
	secrets := flag.String("secrets", "", "secrets/github directory")
	cred := flag.String("credential", "ghes", "credential name")
	host := flag.String("host", "", "git host of the repository")
	repo := flag.String("repo", "acme/widgets", "owner/name")
	pr := flag.Int("pr", 7, "pull request")
	sha := flag.String("sha", "", "expected head sha")
	flag.Parse()
	set, err := gitproxy.LoadCredentialSet(*secrets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tr, err := set.ForgeTransportOn(*cred, *host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	token := strings.TrimSpace(mustRead(*secrets + "/" + *cred + ".token"))
	client := github.NewClient(tr, github.StaticToken{Token: &token})
	owner, name, _ := strings.Cut(*repo, "/")
	entry, err := client.MergeQueueEntryOf(owner, name, *pr)
	fmt.Printf("MergeQueueEntryOf: %+v err=%v\n", entry, err)
	queued, err := client.EnqueuePullRequest(owner, name, *pr, *sha)
	fmt.Printf("EnqueuePullRequest: %+v err=%v\n", queued, err)
	entry, err = client.MergeQueueEntryOf(owner, name, *pr)
	fmt.Printf("MergeQueueEntryOf: %+v err=%v\n", entry, err)
	threads, err := client.ListReviewThreads(owner, name, *pr)
	fmt.Printf("ListReviewThreads: %+v err=%v\n", threads, err)
}

func mustRead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return string(b)
}
