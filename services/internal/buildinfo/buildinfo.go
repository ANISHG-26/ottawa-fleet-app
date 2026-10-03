package buildinfo

import (
	"os"
	"runtime"
)

// The release Dockerfile sets these variables through -ldflags. They remain
// explicit "unknown" in local builds instead of pretending to identify an artifact.
var Service = "unknown"
var Commit = "unknown"
var Architecture = "unknown"

type Info struct {
	Service      string `json:"service"`
	SourceCommit string `json:"source_commit"`
	Architecture string `json:"architecture"`
	ImageDigest  string `json:"image_digest"`
}

func Current() Info {
	architecture := Architecture
	if architecture == "unknown" {
		architecture = runtime.GOARCH
	}
	return Info{Service: Service, SourceCommit: Commit, Architecture: architecture, ImageDigest: os.Getenv("BUILD_DIGEST")}
}
