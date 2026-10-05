package main

import (
	"fmt"
	"runtime/debug"
)

// version is stamped by the release build (-ldflags "-X main.version=v1.2.3").
// Left empty, the version comes from the module (go install …@v1.2.3), or,
// for a build from a checkout, the commit it was built from.
var version string

func cliVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "devel"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return "devel " + rev
}

func cmdVersion() {
	fmt.Println("branchkit-cli " + cliVersion())
}
