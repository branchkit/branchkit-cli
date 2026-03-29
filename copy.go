package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
