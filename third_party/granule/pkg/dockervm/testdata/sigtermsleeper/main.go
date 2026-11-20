// Command sigtermsleeper is the whole of the image realdocker_test.go
// runs: it ignores SIGTERM and sleeps until SIGKILLed, so a "docker stop"
// of it takes its full grace period and a concurrent "docker rm -f" has a
// removal in flight for others to race against -- the slow teardown a
// real VM container has.
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Received and dropped rather than signal.Ignore'd: Notify is what
	// keeps a goroutine running, without which a bare block here would be
	// reported as a deadlock and the process would exit at once.
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	for range c {
	}
}
