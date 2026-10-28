package gitproxy

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/bwsalmon/grain/pkg/authfix"
)

// LoginRedirectError is a request to a forge that was answered with a
// login page rather than by the forge: a front end in the way wants a
// login of the workstation's own (one authfix.RegisterRedirect knows),
// and what fixes it is what that classifier says, run as the account
// grain runs as -- nothing about the git credential.
type LoginRedirectError struct {
	// Host is the host the request was for.
	Host string
	// Location is where the front end sent it.
	Location string
	// Fix is authfix.ClassifyRedirect's verdict on Location.
	Fix authfix.Fix
}

func (e *LoginRedirectError) Error() string {
	msg := e.Fix.Summary + ": " + e.Fix.Action
	if e.Host != "" {
		return msg + " (" + e.Host + " answered: " + e.Fix.Reason + ")"
	}
	return msg + " (" + e.Fix.Reason + ")"
}

// NewLoginRedirect is the LoginRedirectError for a request to host sent
// to location, when location is a login page a registered classifier
// knows (authfix.ClassifyRedirect); ok is false for any other address,
// which is an ordinary redirect.
func NewLoginRedirect(host, location string) (*LoginRedirectError, bool) {
	u, err := url.Parse(strings.TrimSpace(location))
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	fix, ok := authfix.ClassifyRedirect(u)
	if !ok {
		return nil, false
	}
	return &LoginRedirectError{Host: host, Location: u.String(), Fix: fix}, true
}

// AsLoginRedirect is err's LoginRedirectError, if it carries one.
func AsLoginRedirect(err error) (*LoginRedirectError, bool) {
	var login *LoginRedirectError
	if errors.As(err, &login) {
		return login, true
	}
	return nil, false
}

// loginRedirectCheck is a CheckRedirect refusing to follow a redirect to
// a login page, answering LoginRedirectError instead -- what an
// http.Client for a forge gets so that a lapsed login reads as one,
// rather than as the login page's HTML failing to parse as JSON. Any
// other redirect is followed as net/http would (up to ten).
func loginRedirectCheck(req *http.Request, via []*http.Request) error {
	host := ""
	if len(via) > 0 {
		host = via[0].URL.Host
	}
	if login, ok := NewLoginRedirect(host, req.URL.String()); ok {
		return login
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}
