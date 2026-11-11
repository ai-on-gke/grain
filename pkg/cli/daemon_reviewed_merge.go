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
	"flag"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

// reviewedMergeFlags are the daemon's -upgrade-reviewed-merge-* flags:
// whether a reviewed commit went into an upgrade's image
// (pkg/upgrade/reviewed.go, pkg/reviewedmerge). The signature
// -upgrade-verify-cmd checks says only that CI built it.
type reviewedMergeFlags struct {
	repoFlag, loginsFlag, tokenFileFlag *string
}

// registerReviewedMergeFlags declares the -upgrade-reviewed-merge-* flags
// on fs; read them once fs has parsed.
func registerReviewedMergeFlags(fs *flag.FlagSet) reviewedMergeFlags {
	return reviewedMergeFlags{
		repoFlag: fs.String("upgrade-reviewed-merge-repo", "",
			"owner/name of the repository -upgrade-image is built from: refuse an upgrade whose image's commit did not reach "+
				"its main or release/v* through a reviewed merge -- one grain made (-upgrade-reviewed-merge-logins) or one the "+
				"forge's review record covers -- with every first-parent commit since the running build's. Needs -upgrade-verify-cmd. "+
				"Omitted, nothing is asked"),
		loginsFlag: fs.String("upgrade-reviewed-merge-logins", "",
			"comma-separated forge logins the -upgrade-reviewed-merge-repo repository's grain merges as; a merge by one of them counts as reviewed"),
		tokenFileFlag: fs.String("upgrade-reviewed-merge-token-file", "",
			"file holding a read token for the forge API -upgrade-reviewed-merge-repo is asked through (omitted, it is read anonymously)"),
	}
}

func (f reviewedMergeFlags) repo() string      { return *f.repoFlag }
func (f reviewedMergeFlags) logins() []string  { return daemon.SplitBranchPatterns(*f.loginsFlag) }
func (f reviewedMergeFlags) tokenFile() string { return *f.tokenFileFlag }
