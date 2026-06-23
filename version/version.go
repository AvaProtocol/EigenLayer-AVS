package version

var (
	// Default values stamped into the binary when -ldflags doesn't
	// override them. Both Makefile targets (`make build`, `make build-prod`)
	// and the Dockerfile now derive these from `git describe` / `git rev-parse`
	// at compile time, so these defaults only fire when the build path
	// bypasses both (e.g. a one-off `go build ./...` without ldflags).
	//
	// Keep `semver` here roughly in sync with the latest release tag as a
	// safety net — if a build slips through without ldflags, /health will
	// still report something plausible rather than ancient history.
	semver   = "3.11.0"
	revision = "unknown"
)

// Get return the version. This is injected at build time via ldflags when creating releases
func Get() string {
	return semver
}

// GetRevision returns the revision
func GetRevision() string {
	return revision
}