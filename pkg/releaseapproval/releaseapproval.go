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

// Package releaseapproval is the proof a person pressed Approve (or
// Reject) in grain's Inbox, carried to a GitHub Actions
// promote-release.yml run (design/playbook/releases.playbook.md §0.2,
// gap 4).
//
// The workflow's person check refuses a `*[bot]` actor, and the
// deployment dispatches as its own forge credential -- often a GitHub
// App, so a bot. So the dispatch carries a Record: the repository, the
// version, the decision, the exact commit its -pending tag stages, the
// person who decided and a short expiry, signed with an ECDSA P-256 key
// only the deployment holds (its secrets store or a Cloud KMS key
// version, kms.go -- never a sandbox's). The workflow verifies it
// against the public key the operator put in the repository variable
// GRAIN_RELEASE_APPROVAL_KEY (scripts/verify-release-approval.sh), with
// openssl alone.
//
// The deployment signs a Record only inside ui.Client.DecideRelease,
// after it has refused every caller but a person: an agent's token, an
// executor, a steward and a schedule never get one, and without the
// private key nobody can make one.
package releaseapproval

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
)

// Header is the first line of every record: what it is and its format.
const Header = "grain-release-approval/v1"

// Lifetime is how long a record is good for. A dispatch starts its run
// in seconds; a record found later than this is refused rather than
// trusted, and the person presses Approve again.
const Lifetime = 15 * time.Minute

// KeyFile is the deployment's signing key, under its data directory.
const KeyFile = "release-approval-key.pem"

// Record is one person's decision about one release.
type Record struct {
	Repo        string
	Version     string
	Decision    string // "approve", "reject", or "rollback"
	Commit      string // the commit vX.Y.Z-pending (or vX.Y.Z, for rollback) tags
	Approver    string // the person's principal id
	ShortSoakOK bool
	// AllowBreaking is the person's acceptance of breaking changes
	// outside a RELEASE_MAJOR release (the allow_breaking input).
	AllowBreaking bool
	// RollbackTo is the earlier release (vA.B.C) a rollback re-points
	// latest to, or "" for automatic selection.
	RollbackTo string
	// Reason is why a rollback retracts Version, bound into the signed
	// record as its SHA-256 hex digest so free text never breaks the
	// line format.
	Reason  string
	Issued  time.Time
	Expires time.Time
}

// safeValue is what a field may hold: one line, nothing a shell reading
// `key: value` lines could mistake for more.
var safeValue = regexp.MustCompile(`^[A-Za-z0-9@._+/:-]+$`)

// releaseTagPattern is a vX.Y.Z release tag.
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Text is the record as signed: one `key: value` line per field, in a
// fixed order, so scripts/verify-release-approval.sh reads exactly what
// was signed.
func (r Record) Text() (string, error) {
	fields := [][2]string{
		{"repo", r.Repo},
		{"version", r.Version},
		{"decision", r.Decision},
		{"commit", r.Commit},
		{"approver", r.Approver},
		{"short_soak_ok", strconv.FormatBool(r.ShortSoakOK)},
		{"allow_breaking", strconv.FormatBool(r.AllowBreaking)},
	}
	if r.Decision == "rollback" {
		if r.ShortSoakOK || r.AllowBreaking {
			return "", fmt.Errorf("release approval: rollback carries neither short_soak_ok nor allow_breaking")
		}
		rollbackTo := "-"
		if r.RollbackTo != "" {
			if !releaseTagPattern.MatchString(r.RollbackTo) {
				return "", fmt.Errorf("release approval: rollback_to %q is not vX.Y.Z", r.RollbackTo)
			}
			rollbackTo = r.RollbackTo
		}
		reasonSum := sha256.Sum256([]byte(r.Reason))
		fields = append(fields,
			[2]string{"rollback_to", rollbackTo},
			[2]string{"reason_sha256", hex.EncodeToString(reasonSum[:])},
		)
	} else if r.RollbackTo != "" || r.Reason != "" {
		return "", fmt.Errorf("release approval: %s carries neither rollback_to nor reason", r.Decision)
	}
	fields = append(fields,
		[2]string{"issued", strconv.FormatInt(r.Issued.Unix(), 10)},
		[2]string{"expires", strconv.FormatInt(r.Expires.Unix(), 10)},
	)
	var b strings.Builder
	b.WriteString(Header + "\n")
	for _, f := range fields {
		if !safeValue.MatchString(f[1]) {
			return "", fmt.Errorf("release approval: %s %q is not a value a record can carry", f[0], f[1])
		}
		b.WriteString(f[0] + ": " + f[1] + "\n")
	}
	if r.Decision != "approve" && r.Decision != "reject" && r.Decision != "rollback" {
		return "", fmt.Errorf("release approval: decision %q is not approve, reject or rollback", r.Decision)
	}
	return b.String(), nil
}

// Signer signs records with the deployment's key.
type Signer struct {
	key signingKey
}

// signingKey is a key held in the secrets store or a file
// (attest.FileSigner), or in Cloud KMS (KMSSigner).
type signingKey interface {
	SignDigest(ctx context.Context, digest [sha256.Size]byte) ([]byte, error)
	Public() *ecdsa.PublicKey
}

// loadMu serialises LoadSigner: the first load creates the key file, and
// a second load racing it -- an Approve beside a `grain release
// approval-key` -- would otherwise fail on the file the first is still
// writing instead of reading the key it holds.
var loadMu sync.Mutex

// LoadSigner reads the deployment's key under dataDir, making one the
// first time: from the secrets store beside it when there is one
// (keystore.go), else from <dataDir>/release-approval-key.pem.
func LoadSigner(dataDir string) (*Signer, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("release approval: this deployment has no data directory to keep its signing key in")
	}
	loadMu.Lock()
	defer loadMu.Unlock()
	var key *attest.FileSigner
	var err error
	if store := keyStore(dataDir); store != nil {
		key, err = loadStoredSigner(store, dataDir)
	} else {
		key, err = attest.LoadOrCreateFileSigner(filepath.Join(dataDir, KeyFile))
	}
	if err != nil {
		return nil, fmt.Errorf("release approval: signing key: %w", err)
	}
	return &Signer{key: key}, nil
}

// Signed is a record ready for the workflow's inputs: the record's text
// and its ASN.1 DER signature, each base64.
type Signed struct {
	Record    string
	Signature string
}

// Sign signs r.
func (s *Signer) Sign(ctx context.Context, r Record) (Signed, error) {
	text, err := r.Text()
	if err != nil {
		return Signed{}, err
	}
	sig, err := s.key.SignDigest(ctx, sha256.Sum256([]byte(text)))
	if err != nil {
		return Signed{}, fmt.Errorf("release approval: signing: %w", err)
	}
	return Signed{
		Record:    base64.StdEncoding.EncodeToString([]byte(text)),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// PublicKeyPEM is the key the workflow verifies with: the value of the
// repository variable GRAIN_RELEASE_APPROVAL_KEY.
func (s *Signer) PublicKeyPEM() (string, error) {
	return PublicKeyPEM(s.key.Public())
}

// PublicKeyPEM encodes pub as a PEM PUBLIC KEY block.
func PublicKeyPEM(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
