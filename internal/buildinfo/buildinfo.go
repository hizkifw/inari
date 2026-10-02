// Package buildinfo is inari's version and the User-Agent it sends.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// version is set at release by
// -ldflags "-X github.com/hizkifw/inari/internal/buildinfo.version=vX.Y.Z".
var version string

const projectURL = "https://github.com/hizkifw/inari"

// Version is the release's version, or the module version go install
// records, or "dev" for a build from a checkout.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// UserAgent identifies inari to the servers it downloads from, as GitHub's
// API asks.
func UserAgent() string {
	return "inari/" + Version() + " (" + runtime.GOOS + "; " + runtime.GOARCH + "; +" + projectURL + ")"
}
