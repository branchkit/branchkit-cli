package main

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type downloadProgress struct {
	Model  string `json:"model"`
	Status string `json:"status"`
	Pct    int    `json:"pct,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Total  int64  `json:"total,omitempty"`
	Error  string `json:"error,omitempty"`
}

func emitProgress(p downloadProgress) {
	data, _ := json.Marshal(p)
	fmt.Println(string(data))
}

func modelsDir() string {
	return filepath.Join(appSupportDir(), "models")
}

func cmdModelDownload(ref string) {
	m, ok := declaredModels()[ref]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown model: %s\n", ref)
		printDeclaredModels(os.Stderr)
		os.Exit(1)
	}
	provisionDeclaredModel(m)
}

type hfTreeEntry struct {
	Type string `json:"type"` // "file" | "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// hfListFiles recursively lists every file under repo/folder via the HF tree API.
// (WhisperKit model folders are small — tens of files — so no pagination needed.)
func hfListFiles(repo, folder string) ([]hfTreeEntry, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	var out []hfTreeEntry
	var walk func(path string) error
	walk = func(path string) error {
		url := fmt.Sprintf("https://huggingface.co/api/models/%s/tree/main/%s", repo, path)
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
		}
		var entries []hfTreeEntry
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type == "directory" {
				if err := walk(e.Path); err != nil {
					return err
				}
			} else {
				out = append(out, e)
			}
		}
		return nil
	}
	if err := walk(folder); err != nil {
		return nil, err
	}
	return out, nil
}

// httpGetToFile downloads url to dest (overwriting).
func httpGetToFile(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadModelFile(ref, url, destPath string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 256*1024)
	lastPct := -1

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := outFile.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)

			if total > 0 {
				pct := int(written * 100 / total)
				if pct != lastPct && pct%5 == 0 {
					lastPct = pct
					emitProgress(downloadProgress{
						Model:  ref,
						Status: "downloading",
						Pct:    pct,
						Bytes:  written,
						Total:  total,
					})
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
}

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(path), got, expected)
	}
	return nil
}

// extractTarBz2Members writes the named members (matched by basename) from a
// .tar.bz2 into destDir, flattening any leading archive directories.
func extractTarBz2Members(archivePath, destDir string, members []string) error {
	want := make(map[string]bool, len(members))
	for _, m := range members {
		want[m] = true
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(bzip2.NewReader(f))
	found := make(map[string]bool, len(members))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if !want[base] {
			continue
		}
		out, err := os.Create(filepath.Join(destDir, base))
		if err != nil {
			return err
		}
		// hdr.Size is bounded by the archive; copy the declared length.
		if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
			out.Close()
			return err
		}
		out.Close()
		found[base] = true
	}
	for m := range want {
		if !found[m] {
			return fmt.Errorf("archive missing expected member %s", m)
		}
	}
	return nil
}
