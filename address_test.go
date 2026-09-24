package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddressFileResolvesTheUIPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	t.Setenv("BRANCHKIT_APP_SUPPORT", "")
	dir := filepath.Join(appSupportDir(), "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"v":1,"pid":` + itoa(os.Getpid()) + `,"started_at":1,"ui":{"port":55123}}`
	if err := os.WriteFile(filepath.Join(dir, "address.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devBaseURL != "http://127.0.0.1:55123" {
		t.Fatalf("base = %q, want the UI port", devBaseURL)
	}
}

func TestAddressFileFromADeadPidIsStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	t.Setenv("BRANCHKIT_APP_SUPPORT", "")
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
	t.Setenv("BRANCHKIT_APP_SUPPORT", "")
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

func TestOperatorSocketIsPreferredAndDialed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BRANCHKIT_DEV", "")
	t.Setenv("BRANCHKIT_APP_SUPPORT", "")
	dir := filepath.Join(appSupportDir(), "run")
	_ = os.MkdirAll(dir, 0o700)
	// t.TempDir() nests deep under /var/folders on macOS and can exceed the
	// 104-byte socket path cap; a short /tmp dir keeps the path legal.
	sdir, err := os.MkdirTemp("/tmp", "bk-op")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sdir)
	sock := filepath.Join(sdir, "op.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "via-socket "+r.URL.Path)
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	body := `{"v":1,"pid":` + itoa(os.Getpid()) + `,"ui":{"port":1},"operator":{"socket":"` + sock + `"}}`
	_ = os.WriteFile(filepath.Join(dir, "address.json"), []byte(body), 0o600)
	if err := resolveDevBaseURL(); err != nil {
		t.Fatal(err)
	}
	if devUnixSocket != sock {
		t.Fatalf("socket not preferred over the dev port: %q", devUnixSocket)
	}
	resp, err := devClient(2 * time.Second).Get(devBaseURL + "/v1/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "via-socket /v1/plugins" {
		t.Fatalf("got %q", got)
	}
}
