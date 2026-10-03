package main

import (
	"path/filepath"
	"runtime"
	"strings"
)

// exeSuffix is the executable suffix this OS's builds write: ".exe" on
// Windows, nothing elsewhere.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// withExeSuffix names the file a manifest-named program actually is here. A
// manifest spells a binary the way `go build` and cargo name it on macOS and
// Linux (`./snippets-plugin`); on Windows the same build writes
// `snippets-plugin.exe`, and CreateProcess given a full path does not add
// the suffix. The actuator resolves plugin programs and stage binaries by
// the same rule, so the CLI must find the same file it will run.
func withExeSuffix(path string) string {
	return withExeSuffixFor(path, exeSuffix())
}

// withExeSuffixFor is withExeSuffix for a given suffix, so a test on any OS
// holds the Windows rule. The suffixed file wins only when it exists;
// otherwise path is returned as given.
func withExeSuffixFor(path, suffix string) string {
	if suffix == "" || strings.HasSuffix(strings.ToLower(path), strings.ToLower(suffix)) {
		return path
	}
	if fileExists(path + suffix) {
		return path + suffix
	}
	return path
}

// relWithExeSuffix is withExeSuffix for a path relative to dir, keeping it
// relative: "snippets-plugin" in dir becomes "snippets-plugin.exe" when that
// is the file there.
func relWithExeSuffix(dir, rel string) string {
	return rel + strings.TrimPrefix(withExeSuffix(filepath.Join(dir, rel)), filepath.Join(dir, rel))
}
