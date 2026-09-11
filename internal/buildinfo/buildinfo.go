// Package buildinfo exposes the small, non-sensitive product metadata used by
// the Dashboard About and Overview read models. Release builds may inject
// values through -ldflags; local builds fall back to Go's VCS build settings.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

const (
	ProductName      = "DDBOT-AI"
	SourceRepository = "https://github.com/Oumainory/DDBOT-AI"
	License          = "AGPL-3.0"
	LicenseName      = "GNU AGPL-3.0"
)

// These variables are intentionally small ldflags targets. They are not
// secrets and must never be populated from environment dumps or config files.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	ProductName      string `json:"product_name"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	BuildTime        string `json:"build_time"`
	SourceRepository string `json:"source_repository"`
	License          string `json:"license"`
	LicenseName      string `json:"license_name"`
}

// Current returns explicit ldflags values when present and otherwise uses the
// reproducible VCS settings emitted by the Go toolchain. It never reads or
// exposes process environment variables.
func Current() Info {
	version, commit, buildTime := strings.TrimSpace(Version), strings.TrimSpace(Commit), strings.TrimSpace(BuildTime)
	if version == "" {
		version = "dev"
	}
	if commit == "" || commit == "unknown" {
		commit = "unknown"
	}
	if buildTime == "" || buildTime == "unknown" {
		buildTime = "unknown"
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "unknown" && strings.TrimSpace(setting.Value) != "" {
					commit = strings.TrimSpace(setting.Value)
				}
			case "vcs.time":
				if buildTime == "unknown" && strings.TrimSpace(setting.Value) != "" {
					buildTime = strings.TrimSpace(setting.Value)
				}
			}
		}
	}
	return Info{
		ProductName:      ProductName,
		Version:          version,
		Commit:           commit,
		BuildTime:        buildTime,
		SourceRepository: SourceRepository,
		License:          License,
		LicenseName:      LicenseName,
	}
}

func (i Info) CommitURL() string {
	commit := strings.TrimSpace(i.Commit)
	if len(commit) < 7 || len(commit) > 64 {
		return ""
	}
	for _, r := range commit {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return SourceRepository + "/commit/" + commit
}
