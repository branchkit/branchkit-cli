//go:build !windows

package main

import "os"

// linkPluginDir installs dir at link the way a dev checkout is installed:
// by symlink, so edits in place are live.
func linkPluginDir(dir, link string) error {
	return os.Symlink(dir, link)
}
