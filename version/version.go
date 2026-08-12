package version

var (
	// Defaults for builds that bypass -ldflags. Every real build path stamps
	// these instead:
	//
	//	make build / build-prod   SEMVER from `git describe --tags`
	//	Dockerfile               ARG SEMVER, falling back to the git tag
	//	publish-prod-docker.yml  SEMVER from the published release tag
	//
	// so these values only surface on a bare `go build ./...`, a `go run`, or
	// a test binary.
	//
	// semver is deliberately "dev" rather than the latest release number. A
	// stale release number here is worse than no number: an unstamped binary
	// reporting "4.7.1" on /health and in Sentry's release tag is
	// indistinguishable from a real 4.7.1, so a locally-built binary running
	// somewhere it shouldn't looks like a legitimate deploy. "dev" says
	// exactly what it is.
	//
	// This also removes a manual chore. Keeping the literal in step with the
	// tag meant a `chore: bump version.go` commit on main after every release,
	// which then had to be synced back to staging — upkeep for a value nothing
	// reads in production.
	semver   = "4.13.0"
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