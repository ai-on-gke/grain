package main

import (
	"github.com/ai-on-gke/grain/pkg/registry"
	_ "github.com/ai-on-gke/grain/pkg/registry/providers/gcp"
)

func main() {
	registry.Main()
}
