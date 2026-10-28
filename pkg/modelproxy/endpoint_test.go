package modelproxy

// The Endpoint value itself, apart from the handler in front of it: what
// a listener publishes into one while everything it serves reads it.

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// An endpoint is written by whatever binds the listener and read by
// every request that listener serves, plus the capability provider
// minting tokens for it -- all at once, in the same process. So both
// halves of one are behind the same lock, and neither is a field a
// caller assigns.
//
// This is that claim, and it only fails under -race: a credential
// resolver kept in an exported func field (which is how this started)
// reads perfectly well right up until two goroutines touch it, and then
// fails whichever test happened to be running when the detector fired.
func TestAnEndpointIsPublishedAndReadFromSeveralGoroutinesAtOnce(t *testing.T) {
	t.Parallel()
	endpoint := NewEndpoint()

	var wg sync.WaitGroup
	for writer := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				endpoint.SetURL(fmt.Sprintf("http://127.0.0.1:%d", 40000+writer))
				endpoint.SetCredential(func(context.Context, Connection) (string, error) {
					return "a-credential", nil
				})
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = endpoint.URL()
				// Whichever resolver is in force, there is one answer
				// an endpoint may never give while being republished:
				// an error nobody wired up.
				if _, err := endpoint.ResolveCredential(t.Context(), testConnection); err != nil {
					t.Errorf("ResolveCredential while the endpoint was being published: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// An endpoint nothing has published a credential into resolves to the
// empty string rather than failing: that is a deployment with no
// credential configured for this vendor, which the handler refuses with
// a sentence naming what to set (TestAConnectionWithNoCredentialSaysSo).
// Nil-safe in the same breath, since "there is no proxy" reaches here too.
func TestAnEndpointWithNoCredentialResolvesToNothing(t *testing.T) {
	t.Parallel()
	for name, endpoint := range map[string]*Endpoint{"unpublished": NewEndpoint(), "nil": nil} {
		credential, err := endpoint.ResolveCredential(t.Context(), testConnection)
		if err != nil || credential != "" {
			t.Errorf("a %s endpoint resolved %q, %v, want \"\" and no error", name, credential, err)
		}
	}
}
