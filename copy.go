package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const maxCopyDepth = 10

// safeCopyDir copies directory contents, rejecting symlinks and limiting depth.
func safeCopyDir(src, dest string, depth int) error {
	if depth > maxCopyDepth {
		return fmt.Errorf("directory nesting exceeds maximum depth (%d)", maxCopyDepth)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		// Reject symlinks
		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in plugin archives: %s", srcPath)
		}

		if info.IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return err
			}
			if err := safeCopyDir(srcPath, destPath, depth+1); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, destPath, info.Mode()); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies src to dest by writing a file beside dest and renaming it
// over. Opening dest itself and truncating it fails when dest is a running
// program (ETXTBSY on Linux, a sharing violation on Windows) and, where it
// succeeds, rewrites the bytes under a process executing them. The rename
// leaves a running copy its own file.
func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := fmt.Sprintf("%s.copying-%d", dest, os.Getpid())
	os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Close()
	} else {
		out.Close()
	}
	if err == nil {
		err = replaceFile(tmp, dest)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// replaceFile renames src over dest. Windows refuses to replace a program
// that is running but lets it be renamed, so there the old dest is moved
// aside first (dest + ".old") and removed once it is no longer running —
// by the next replace, if not now.
func replaceFile(src, dest string) error {
	err := os.Rename(src, dest)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	aside := dest + ".old"
	os.Remove(aside)
	if aerr := os.Rename(dest, aside); aerr != nil {
		return err
	}
	if rerr := os.Rename(src, dest); rerr != nil {
		os.Rename(aside, dest)
		return rerr
	}
	os.Remove(aside)
	return nil
}

// replaceDir puts the directory stage in place of target. The previous
// target is moved aside before the swap and deleted after it, rather than
// deleted first: deleting a folder whose program is running fails partway
// on Windows, leaving neither version installed. If target cannot be moved
// aside, nothing is touched and the error says so.
func replaceDir(stage, target string) error {
	prev := target + ".previous"
	os.RemoveAll(prev)
	hadPrevious := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, prev); err != nil {
			return fmt.Errorf("moving the previous install aside (stop the plugin and try again): %w", err)
		}
		hadPrevious = true
	}
	if err := os.Rename(stage, target); err != nil {
		if hadPrevious {
			os.Rename(prev, target)
		}
		return err
	}
	// Best effort: a program still running from it keeps it on Windows
	// until it exits, and the next install clears it.
	os.RemoveAll(prev)
	return nil
}
