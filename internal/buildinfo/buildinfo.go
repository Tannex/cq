// Package buildinfo reports the version shared by cq executables.
package buildinfo

import "runtime/debug"

// Version is replaced with the release tag through -ldflags -X. It is exported
// so every executable in this module can use the same linker injection target.
var Version = "dev"

// Reported returns the linker-injected version, then the Go module build-info
// version for go install builds, and finally dev.
func Reported() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
