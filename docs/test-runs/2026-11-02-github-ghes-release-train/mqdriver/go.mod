module grain.local/mqdriver

go 1.26.6

require github.com/ai-on-gke/grain v0.0.0

require (
	github.com/bwsalmon/granule v0.0.0-20260921040231-1d6b7e1fad96 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ai-on-gke/grain => ../../../..

replace github.com/bwsalmon/granule => ../../../../third_party/granule
