// Package buildinfo holds values set at build time via -ldflags.
package buildinfo

var (
	// Version is the grex release version, e.g. a semver tag from svu.
	Version = "dev"
	// Commit is the short git commit SHA the binary was built from.
	Commit = "none"
	// Date is the UTC build timestamp in RFC 3339.
	Date = "unknown"
)
