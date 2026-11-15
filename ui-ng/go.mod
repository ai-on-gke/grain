// ui-ng holds no Go. This file makes it a module of its own so that the
// root module's `go list ./...`, `go vet ./...` and `go test ./...` do not
// descend into ui-ng/node_modules, where some npm packages (flatted, via
// eslint) ship Go sources of their own.
module github.com/ai-on-gke/grain/ui-ng

go 1.24
