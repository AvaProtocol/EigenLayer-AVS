package version

var (
	// Bumped manually on each release to match the current main tag — see
	// docs/Release.md. ldflags overrides this for properly-built Docker
	// images (publish-prod-docker.yml), but Railway's source-build path
	// does not pass -ldflags, so this default is what telemetry/health
	// endpoints report for Railway-deployed services.
	semver   = "3.0.0"
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
