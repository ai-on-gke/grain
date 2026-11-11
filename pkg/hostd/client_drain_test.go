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

package hostd

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// fakeHostd is a Client.Dial whose hostd reads one request and then does
// what answer says with the connection.
func fakeHostd(answer func(net.Conn)) func(context.Context) (net.Conn, error) {
	return func(context.Context) (net.Conn, error) {
		mine, theirs := net.Pipe()
		go func() {
			if _, _, err := readFrame(theirs); err != nil {
				_ = theirs.Close()
				return
			}
			answer(theirs)
		}()
		return mine, nil
	}
}

// A released hostd with nothing running exits as it starts draining,
// which can beat its OK onto the wire (the 2026-11-11 run host failure
// against grain-agentd v3.3.0): the request reached it, so a drain or a
// cutover whose connection ends unanswered has taken effect.
func TestADrainAnsweredByHostdExitingIsNotAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gone := Client{Dial: fakeHostd(func(c net.Conn) { _ = c.Close() })}
	if err := gone.Drain(ctx); err != nil {
		t.Errorf("drain answered by hostd exiting: %v, want nil", err)
	}
	if err := gone.Cutover(ctx); err != nil {
		t.Errorf("cutover answered by hostd exiting: %v, want nil", err)
	}

	// Anything else hostd says is still read as before.
	refused := Client{Dial: fakeHostd(func(c net.Conn) {
		_ = writeFrame(c, frameRefused, []byte(`{"reason":"no"}`))
		_ = c.Close()
	})}
	var r *RefusedError
	if err := refused.Drain(ctx); !errors.As(err, &r) {
		t.Errorf("a refused drain: err = %v, want a refusal", err)
	}
	ok := Client{Dial: fakeHostd(func(c net.Conn) {
		_ = writeFrame(c, frameOK, nil)
		_ = c.Close()
	})}
	if err := ok.Drain(ctx); err != nil {
		t.Errorf("an answered drain: %v", err)
	}

	// Other one-shots still report a connection that ends unanswered.
	if _, err := gone.Processes(ctx); err == nil {
		t.Error("a listing whose connection ended unanswered reported no error")
	}
}
