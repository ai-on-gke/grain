module github.com/ai-on-gke/grain/grain-plc

go 1.26.6

toolchain go1.26.11

require (
	github.com/ai-on-gke/grain v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

replace (
	github.com/ai-on-gke/grain => ../
	github.com/gke-labs/granule => ../third_party/granule
)
