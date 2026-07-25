package ui

import "github.com/dennisme/grex/internal/buildinfo"

func buildVersion() (version, commit string) {
	return buildinfo.Version, buildinfo.Commit
}
