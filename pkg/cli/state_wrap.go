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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// stateKeyWrap is `grain state key wrap -kms <key>` and `unwrap`: the
// secrets key file goes to, or back from, the form wrapped by Cloud KMS
// (design/design/secrets-key-protection.design.md §6.2).
//
// It runs in the daemon when one answers, since the daemon holds the key
// in memory and keeps it throughout, so nothing restarts. With the
// daemon stopped it opens the store itself, which for unwrap means one
// KMS call from this process. Either way the store wraps, unwraps the
// result and compares before it writes, and the file is replaced by a
// rename.
func stateKeyWrap(ctx context.Context, dataDir, verb string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain state key unwrap", flag.ContinueOnError)
	var kmsKey *string
	if verb == "wrap" {
		fs = flag.NewFlagSet("grain state key wrap", flag.ContinueOnError)
		kmsKey = fs.String("kms", "", "the Cloud KMS key to wrap with: projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k> (required)")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	name := ""
	if kmsKey != nil {
		name = strings.TrimSpace(*kmsKey)
		if name == "" {
			return errors.New("usage: grain state key wrap -kms projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>")
		}
		if err := secrets.ValidateKMSKeyName(name); err != nil {
			return err
		}
	}
	res, viaDaemon, err := stateKeyWrapThroughDaemon(ctx, dataDir, verb, name)
	if err != nil {
		return err
	}
	if !viaDaemon {
		store := daemon.OpenSecrets(dataDir)
		var rep secrets.KeyWrapReport
		if verb == "wrap" {
			rep, err = store.WrapKeyWithKMS(ctx, name, nil)
		} else {
			rep, err = store.UnwrapKeyFromKMS(ctx, nil)
		}
		if err != nil {
			return err
		}
		res = ui.SecretsKeyWrapResult{
			KeyFile: rep.KeyFile, PublicKey: rep.Public, Protection: rep.Protection.Mode, KMSKey: rep.Protection.KMSKey,
			PlaintextCopies: rep.PlaintextCopies, Notes: rep.Notes,
		}
	}
	if res.Protection == secrets.ProtectionKMS {
		fmt.Fprintf(out, "wrapped %s (%s) with Cloud KMS key %s\n", res.KeyFile, res.PublicKey, res.KMSKey)
	} else {
		fmt.Fprintf(out, "unwrapped %s (%s): it is a plaintext key file again\n", res.KeyFile, res.PublicKey)
	}
	if !viaDaemon {
		fmt.Fprintln(out, "(no daemon answered, so this command changed the key file itself)")
	}
	if len(res.PlaintextCopies) > 0 {
		fmt.Fprintln(out, "plaintext copies of a private key this host can still see -- destroy them once the key is escrowed:")
		for _, p := range res.PlaintextCopies {
			fmt.Fprintf(out, "  %s\n", p)
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	return nil
}

// stateKeyWrapThroughDaemon asks this host's daemon to wrap or unwrap,
// reporting viaDaemon false -- and no error -- when no daemon is
// listening, so the caller can do it in this process instead.
func stateKeyWrapThroughDaemon(ctx context.Context, dataDir, verb, kmsKey string) (res ui.SecretsKeyWrapResult, viaDaemon bool, err error) {
	if os.Getenv(dataDirEnvVar) == "" {
		os.Setenv(dataDirEnvVar, dataDir)
	}
	server, _ := serverSource()
	c := ui.NewHTTPClient(server)
	c.HTTP = cliHTTPClient(5*time.Second, 2*time.Minute)
	// One attempt: a daemon that is not listening means this process
	// does the work, not that it waits out a restart.
	c.RetryPolicy = &ui.RetryPolicy{}
	if verb == "wrap" {
		res, err = c.WrapSecretsKey(ctx, kmsKey)
	} else {
		res, err = c.UnwrapSecretsKey(ctx)
	}
	if err != nil && daemonRefused(err) {
		return ui.SecretsKeyWrapResult{}, false, nil
	}
	if err != nil {
		return ui.SecretsKeyWrapResult{}, true, err
	}
	return res, true, nil
}
