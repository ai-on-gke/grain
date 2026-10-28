package protocol

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// methodPattern is the grammar of a method name (§2.1.3), and of an MCP
// server name an mcp_endpoint reports (§5.2.3).
var methodPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// attemptIDPattern is the grammar of an attempt_id (§1.2).
var attemptIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidMethod reports whether name is a conforming <method> (§2.1.3).
func ValidMethod(name string) bool { return methodPattern.MatchString(name) }

// ValidAttemptID reports whether id is a conforming attempt_id (§1.2).
func ValidAttemptID(id string) bool { return attemptIDPattern.MatchString(id) }

// Ref is a parsed capability reference, <capability_id>[#<method>].
// Method is empty for the shorthand form until Server.Resolve fills it in.
type Ref struct {
	CapabilityID string
	Method       string
}

// String is the reference in its written form: the canonical form (§2.1.5)
// once Method is set.
func (r Ref) String() string {
	if r.Method == "" {
		return r.CapabilityID
	}
	return r.CapabilityID + "#" + r.Method
}

// ParseRef parses a capability reference against the grammar of §2.1.1 and
// the rules of §2.1.2-§2.1.3. It never resolves or contacts anything: the
// capability_id is an opaque lookup key into mcpServers.
func ParseRef(s string) (Ref, error) {
	id, method, hasMethod := strings.Cut(s, "#")
	if hasMethod && !ValidMethod(method) {
		return Ref{}, fmt.Errorf("capability reference %q: method %q does not match %s", s, method, methodPattern)
	}
	if err := ValidateCapabilityID(id); err != nil {
		return Ref{}, err
	}
	return Ref{CapabilityID: id, Method: method}, nil
}

// ValidateCapabilityID checks a capability_id against §2.1.2: an https://
// URL with a non-empty host and at least one non-empty path segment, with
// no query, fragment, userinfo, dot segments or trailing slash.
func ValidateCapabilityID(id string) error {
	if !strings.HasPrefix(id, "https://") {
		return fmt.Errorf("capability id %q: must be an https:// URL", id)
	}
	if strings.ContainsAny(id, "?#") {
		return fmt.Errorf("capability id %q: must not carry a query string or fragment", id)
	}
	if strings.HasSuffix(id, "/") {
		return fmt.Errorf("capability id %q: must not end with /", id)
	}
	u, err := url.Parse(id)
	if err != nil {
		return fmt.Errorf("capability id %q: %v", id, err)
	}
	if u.User != nil {
		return fmt.Errorf("capability id %q: must not carry userinfo", id)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("capability id %q: has no host", id)
	}
	rest := strings.TrimPrefix(id, "https://"+u.Host)
	if rest == "" {
		return fmt.Errorf("capability id %q: needs at least one path segment", id)
	}
	for _, seg := range strings.Split(strings.TrimPrefix(rest, "/"), "/") {
		switch seg {
		case "":
			return fmt.Errorf("capability id %q: has an empty path segment", id)
		case ".", "..":
			return fmt.Errorf("capability id %q: must not contain %q segments", id, seg)
		}
	}
	return nil
}

// lastSegment is the final path segment of a valid capability_id.
func lastSegment(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// resolveMethod applies the single-method shorthand of §2.1.4 to a
// reference with no #<method>, given the methods its server advertises.
func resolveMethod(r Ref, methods map[string]*MethodInfo) (Ref, error) {
	if r.Method != "" {
		if _, ok := methods[r.Method]; !ok {
			return Ref{}, fmt.Errorf("capability %s: server advertises no mint_%s (it offers %s)", r, r.Method, methodList(methods))
		}
		return r, nil
	}
	if seg := lastSegment(r.CapabilityID); methods[seg] != nil {
		return Ref{CapabilityID: r.CapabilityID, Method: seg}, nil
	}
	if len(methods) == 1 {
		for name := range methods {
			return Ref{CapabilityID: r.CapabilityID, Method: name}, nil
		}
	}
	return Ref{}, fmt.Errorf("capability %s names no method and its server offers %s: write one as %s#<method>", r, methodList(methods), r.CapabilityID)
}

func methodList(methods map[string]*MethodInfo) string {
	if len(methods) == 0 {
		return "no methods"
	}
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, "#"+name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
