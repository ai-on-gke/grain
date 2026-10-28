// Command defaultsserver is a small browser-test deployment with a persistent
// store. It exists to exercise deployment task-list policy through the real
// HTTP server and CLI while Playwright stops and restarts the process.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

func main() {
	var (
		addr       = flag.String("addr", "127.0.0.1:0", "address to listen on")
		dataDir    = flag.String("data-dir", "", "persistent deployment data directory")
		defaultSet bool
		defaults   string
	)
	flag.Func("default-filters", "deployment task-list default query (empty clears it)", func(value string) error {
		defaultSet = true
		defaults = value
		return nil
	})
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "defaultsserver: -data-dir is required")
		os.Exit(2)
	}

	db, err := sqlite.Open(sqlite.DefaultConfig(*dataDir))
	if err != nil {
		log.Fatalf("opening store: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		log.Fatalf("initializing store: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		log.Fatalf("reading config: %v", err)
	}
	if cfg == nil {
		fresh := model.DefaultConfig()
		cfg = &fresh
	}
	if defaultSet {
		cfg.DefaultFilters = defaults
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		log.Fatalf("persisting config: %v", err)
	}

	serverConfig := ui.Config{
		Actor:        ui.DefaultActor("browser-test"),
		Capabilities: ui.OfferedCapabilities(),
		// The run's access code (e2e/global-setup.js): every caller,
		// loopback included, shows it, as on every grain daemon.
		LocalAPIToken: os.Getenv("GRAIN_E2E_ACCESS_CODE"),
	}
	client := ui.NewClient(serverConfig, store)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		log.Fatalf("listing seed tasks: %v", err)
	}
	if len(tasks) == 0 {
		seed(ctx, client)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listening: %v", err)
	}
	log.Printf("defaults server at http://%s", listener.Addr())
	log.Fatal(http.Serve(listener, ui.NewServerWithClient(client)))
}

func seed(ctx context.Context, client *ui.Client) {
	create := func(req ui.CreateTaskRequest) ui.Task {
		task, err := client.CreateTask(ctx, req)
		if err != nil {
			log.Fatalf("creating %q: %v", req.Title, err)
		}
		return task
	}
	create(ui.CreateTaskRequest{Title: "Queued background", Approved: true, NoRepo: true})
	create(ui.CreateTaskRequest{Title: "Queued live chat", Approved: true, Interactive: true, NoRepo: true})
	create(ui.CreateTaskRequest{Title: "Proposed background", NoRepo: true})
	closed := create(ui.CreateTaskRequest{Title: "Closed background", NoRepo: true})
	if err := client.Close(ctx, closed.ID, ui.CloseOptions{}); err != nil {
		log.Fatalf("closing seed task: %v", err)
	}
	deferred := create(ui.CreateTaskRequest{Title: "Deferred background", Approved: true, NoRepo: true})
	if err := client.Defer(ctx, deferred.ID, ui.DeferOptions{}); err != nil {
		log.Fatalf("deferring seed task: %v", err)
	}
}
