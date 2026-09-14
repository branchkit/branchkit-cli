package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddressFileResolvesTheUIPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	dir := filepath.Join(appSupportDir(), "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"v":1,"pid":` + itoa(os.Getpid()) + `,"started_at":1,"ui":{"port":55123},"dev":{"port":21551}}`
	if err := os.WriteFile(filepath.Join(dir, "address.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devBaseURL != "http://127.0.0.1:21551" {
		t.Fatalf("base = %q, want the dev listener while one is listed", devBaseURL)
	}
}

func TestAddressFileWithoutDevBlockUsesTheUIPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	dir := filepath.Join(appSupportDir(), "run")
	_ = os.MkdirAll(dir, 0o700)
	body := `{"v":1,"pid":` + itoa(os.Getpid()) + `,"ui":{"port":55123}}`
	_ = os.WriteFile(filepath.Join(dir, "address.json"), []byte(body), 0o600)
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devBaseURL != "http://127.0.0.1:55123" {
		t.Fatalf("base = %q, want the UI port on a production install", devBaseURL)
	}
}

func TestAddressFileFromADeadPidIsStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	dir := filepath.Join(appSupportDir(), "run")
	_ = os.MkdirAll(dir, 0o700)
	// pid 2^22-1 is above macOS's and Linux's default pid ceilings.
	_ = os.WriteFile(filepath.Join(dir, "address.json"), []byte(`{"v":1,"pid":4194303,"ui":{"port":1}}`), 0o600)
	if err := resolveDevBaseURL(); err == nil {
		t.Fatal("a dead writer must read as stale")
	}
}

func TestNoAddressFileIsAClearError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BRANCHKIT_DEV", "")
	if err := resolveDevBaseURL(); err != errNoAddress {
		t.Fatalf("got %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
