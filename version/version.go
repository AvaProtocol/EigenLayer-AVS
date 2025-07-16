package version

var (
	// Version is set at build time via ldflags by go-semantic-release
	// Default to "dev" for development builds
	semver   = "dev"
	revision = "unknown"
)

// Get return the version. This is injected at build time via ldflags when creating releases
func Get() string {
	return semver
}

// Commit returns the git commit hash, injected at build time
func Commit() string {
	return revision
}
