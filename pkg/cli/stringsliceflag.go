package cli

import "strings"

// stringSliceFlag collects every occurrence of a repeatable flag, in
// order, into a []string -- flag.String only ever keeps the last one,
// which -granule-create-arg (an ordered sequence of flag/value pairs
// passed straight through to `granule vm create`) can't use.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, " ") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}
