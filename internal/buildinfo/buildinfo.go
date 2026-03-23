// Package buildinfo exposes build-time metadata injected via -ldflags.
// Every binary in the release sets this at build time:
//
//	-X helpdesk/internal/buildinfo.Version=<version>-<git-sha>
package buildinfo

// Version is the release version string, set at build time.
// Falls back to "dev" when running from source without ldflags.
var Version = "dev"
