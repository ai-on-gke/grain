package ui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// databaseConfirmTTL is how long a confirmation token handed out by
// POST /api/database/upgrade stays good for.
const databaseConfirmTTL = 10 * time.Minute

// databaseConfirmMax bounds how many unconfirmed upgrade requests are
// remembered at once, so that a stream of first requests cannot grow the
// map without limit. The oldest is dropped to make room.
const databaseConfirmMax = 16

// databaseConfirmations holds the confirmation tokens POST
// /api/database/upgrade has handed out and not yet seen back.
//
// The route copies the whole store to a caller-named DSN, writes that
// DSN into db-config.json and can restart the daemon onto it. Being a
// system administrator is necessary but not enough: a stolen session, or
// a request forged through an admin's browser, is also an admin request.
// So the first request does nothing but mint a token and write it to the
// daemon's log -- a channel only an operator with access to the daemon
// host reads -- and only a second request carrying that token, from the
// same principal and for exactly the same DSN and options, does the work.
//
// The zero value is ready to use.
type databaseConfirmations struct {
	mu      sync.Mutex
	pending map[string]databaseConfirmation
}

type databaseConfirmation struct {
	binding [sha256.Size]byte
	expires time.Time
}

// databaseConfirmBinding is what a token is bound to: who asked, and
// precisely what they asked for.
func databaseConfirmBinding(principal, dsn string, migrateData, restart bool) [sha256.Size]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%q\x00%q\x00%t\x00%t", principal, dsn, migrateData, restart)))
}

// issue mints a single-use token for binding, good until now+TTL.
func (d *databaseConfirmations) issue(binding [sha256.Size]byte, now time.Time) (string, time.Time, error) {
	var raw [15]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generating confirmation token: %w", err)
	}
	token := strings.ToLower(base32.StdEncoding.EncodeToString(raw[:]))
	expires := now.Add(databaseConfirmTTL)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending == nil {
		d.pending = make(map[string]databaseConfirmation)
	}
	for k, p := range d.pending {
		if !now.Before(p.expires) {
			delete(d.pending, k)
		}
	}
	for len(d.pending) >= databaseConfirmMax {
		oldest := ""
		for k, p := range d.pending {
			if oldest == "" || p.expires.Before(d.pending[oldest].expires) {
				oldest = k
			}
		}
		delete(d.pending, oldest)
	}
	d.pending[token] = databaseConfirmation{binding: binding, expires: expires}
	return token, expires, nil
}

// redeem reports whether token was issued for binding and has not
// expired. A token is spent by any attempt to redeem it, right or
// wrong, so a mistyped or mismatched one has to be asked for again.
func (d *databaseConfirmations) redeem(token string, binding [sha256.Size]byte, now time.Time) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.pending[token]
	if !ok {
		return false
	}
	delete(d.pending, token)
	if !now.Before(p.expires) {
		return false
	}
	return subtle.ConstantTimeCompare(p.binding[:], binding[:]) == 1
}

// announceDatabaseConfirmation puts a freshly issued token where an
// operator, and not the HTTP caller, can read it: Config.DatabaseConfirm
// if set, otherwise the daemon's log.
func (s *Server) announceDatabaseConfirmation(token, summary string) {
	if f := s.tasks.Config.DatabaseConfirm; f != nil {
		f(token, summary)
		return
	}
	log.Print(databaseConfirmLogLine(token, summary))
}

// databaseConfirmLogLine is the line announceDatabaseConfirmation logs;
// databaseConfirmTokenInLog must keep matching it.
func databaseConfirmLogLine(token, summary string) string {
	return fmt.Sprintf("ui: %s; if you made this request, resend it with \"confirmationToken\": %q to proceed", summary, token)
}

// databaseConfirmTokenInLog matches the token in the line
// announceDatabaseConfirmation logs.
var databaseConfirmTokenInLog = regexp.MustCompile(`("confirmationToken": )"[^"]*"`)

// redactDatabaseConfirmTokens blanks confirmation tokens out of log
// lines GET /api/logs/{source} is about to serve. That route shows the
// daemon's own log to the same administrators the token is meant to be
// confirmed past, so the token is read only where the log is read on
// the daemon's host.
func redactDatabaseConfirmTokens(lines []string) []string {
	for i, line := range lines {
		lines[i] = databaseConfirmTokenInLog.ReplaceAllString(line, `$1"[redacted: read it on the daemon host]"`)
	}
	return lines
}
