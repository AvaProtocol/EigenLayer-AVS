package version

var (
	// Version is set through tag release at build time
	semver = "0.0.0-unknow"
)

// Get return the version. Note that we're injecting this at build time when we tag release
func Get() string {
	return semver
}
