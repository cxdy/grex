package buildinfo_test

import (
	"testing"

	"github.com/dennisme/grex/internal/buildinfo"
)

func TestDefaults(t *testing.T) {
	t.Parallel()
	// Defaults are set without ldflags; CI/build may override via -X.
	if buildinfo.Version == "" {
		t.Error("Version should be non-empty")
	}
	if buildinfo.Commit == "" {
		t.Error("Commit should be non-empty")
	}
	if buildinfo.Date == "" {
		t.Error("Date should be non-empty")
	}
}
