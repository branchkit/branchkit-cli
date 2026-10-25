//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runningCopy copies a program Windows ships into dir and starts it, so dir
// holds a running image the way a plugin's folder does. It is stopped when
// the test ends.
func runningCopy(t *testing.T, dir string) string {
	t.Helper()
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
	exe := filepath.Join(dir, "plugin.exe")
	data, err := os.ReadFile(sys)
	if err != nil {
		t.Fatalf("reading %s: %v", sys, err)
	}
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the copy: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	time.Sleep(300 * time.Millisecond)
	// A running image cannot be rewritten in place: the reason copyFile and
	// replaceFile exist. If it could, the test would prove nothing.
	if f, err := os.OpenFile(exe, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
		f.Close()
		t.Fatal("a running program's file opened for writing; the test needs a locked image")
	}
	return exe
}

// replaceFile puts a new build over a program that is running: the old one
// is moved aside, the new one is in place, and the running process keeps
// its image.
func TestReplaceFileOverARunningProgram(t *testing.T) {
	dir := tmpDir(t, "replace-running")
	exe := runningCopy(t, dir)
	next := filepath.Join(dir, "plugin.exe.new")
	if err := os.WriteFile(next, []byte("the next build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(next, exe); err != nil {
		t.Fatalf("replaceFile over a running program: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, []byte("the next build")) {
		t.Fatalf("the target holds %d bytes, not the new build", len(got))
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatal("the source is still there after the rename")
	}
}

// copyFile, which `dev watch` and a local install use, takes the same path.
func TestCopyFileOverARunningProgram(t *testing.T) {
	dir := tmpDir(t, "copy-running")
	exe := runningCopy(t, dir)
	src := filepath.Join(tmpDir(t, "copy-running-src"), "plugin.exe")
	if err := os.WriteFile(src, []byte("rebuilt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, exe, 0o755); err != nil {
		t.Fatalf("copyFile over a running program: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "rebuilt" {
		t.Fatalf("the target holds %q", got)
	}
}

// replaceDir over a folder whose program is running either swaps it or
// refuses with the fix, and never leaves neither version installed.
func TestReplaceDirOverARunningPlugin(t *testing.T) {
	root := tmpDir(t, "replace-dir-running")
	target := filepath.Join(root, "plugin")
	os.MkdirAll(target, 0o755)
	runningCopy(t, target)
	stage := filepath.Join(root, "stage")
	os.MkdirAll(stage, 0o755)
	os.WriteFile(filepath.Join(stage, "plugin.json"), []byte(`{"id":"new"}`), 0o644)

	err := replaceDir(stage, target)
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(target, "plugin.json"))
		if string(got) != `{"id":"new"}` {
			t.Fatalf("swapped, but the target holds %q", got)
		}
		t.Log("a folder holding a running program was moved aside and replaced")
		return
	}
	if !strings.Contains(err.Error(), "stop the plugin") {
		t.Fatalf("refused without the fix: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(target, "plugin.exe")); serr != nil {
		t.Fatalf("refused, and the previous install is gone: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(stage, "plugin.json")); serr != nil {
		t.Fatalf("refused, and the staged version is gone: %v", serr)
	}
	t.Logf("refused, previous install intact: %v", err)
}

// linkPluginDir links a folder whether or not the account may make
// symlinks: run under `runas /trustlevel:0x20000` (no symlink privilege)
// it takes the junction fallback.
func TestLinkPluginDirLinks(t *testing.T) {
	root := tmpDir(t, "link-dir")
	dir := filepath.Join(root, "src")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("{}"), 0o644)
	link := filepath.Join(root, "link")
	if err := linkPluginDir(dir, link); err != nil {
		t.Fatalf("linkPluginDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(link, "plugin.json")); err != nil {
		t.Fatalf("the link does not reach the folder: %v", err)
	}
	fi, _ := os.Lstat(link)
	kind := "symlink"
	if fi.Mode()&os.ModeSymlink == 0 {
		kind = "junction"
	}
	if target, err := os.Readlink(link); err == nil {
		t.Logf("linked by %s (mode %v) to %s", kind, fi.Mode(), target)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("removing the link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatal("removing the link touched the folder it pointed at")
	}
}
