package version

var (
	// Version can also be set through tag release at build time
	semver   = "1.9.6"
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
