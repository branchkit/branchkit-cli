package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultBlocklistURL = "https://raw.githubusercontent.com/branchkit/registry/main/blocklist.json"

type blocklist struct {
	Blocked   []blockedEntry `json:"blocked"`
	UpdatedAt string         `json:"updated_at"`
}

type blockedEntry struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

func cmdCheckBlocklist() {
	flagged := checkInstalledAgainstBlocklist()
	if len(flagged) == 0 {
		return
	}
	data, err := json.Marshal(flagged)
	if err != nil {
		return
	}
	fmt.Println(string(data))
	os.Exit(1)
}

func checkBlocklist(source string) {
	bl := fetchBlocklist()
	if bl == nil {
		return
	}
	for _, entry := range bl.Blocked {
		if matchesBlockedSource(source, entry.Source) {
			fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: This plugin has been blocklisted.\n")
			fmt.Fprintf(os.Stderr, "   Source: %s\n", entry.Source)
			if entry.Reason != "" {
				fmt.Fprintf(os.Stderr, "   Reason: %s\n", entry.Reason)
			}
			fmt.Fprintf(os.Stderr, "\n   Installation aborted. If you believe this is an error,\n")
			fmt.Fprintf(os.Stderr, "   use --force to override.\n\n")
			os.Exit(1)
		}
	}
}

func checkInstalledAgainstBlocklist() []blockedPlugin {
	bl := fetchBlocklist()
	if bl == nil {
		return nil
	}
	var flagged []blockedPlugin
	for _, dp := range discoverPlugins() {
		for _, entry := range bl.Blocked {
			if dp.Manifest.ID == pluginIDFromSource(entry.Source) {
				flagged = append(flagged, blockedPlugin{
					ID:     dp.Manifest.ID,
					Source: entry.Source,
					Reason: entry.Reason,
				})
			}
		}
	}
	return flagged
}

type blockedPlugin struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Reason string `json:"reason"`
}

func fetchBlocklist() *blocklist {
	cached := readCachedBlocklist()
	if cached != nil && !blocklistStale(cached) {
		return cached
	}

	url := os.Getenv("BRANCHKIT_BLOCKLIST_URL")
	if url == "" {
		url = defaultBlocklistURL
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		if cached != nil {
			return cached
		}
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		if cached != nil {
			return cached
		}
		return nil
	}

	var bl blocklist
	if err := json.NewDecoder(resp.Body).Decode(&bl); err != nil {
		if cached != nil {
			return cached
		}
		return nil
	}

	writeBlocklistCache(&bl)
	return &bl
}

func blocklistCachePath() string {
	return filepath.Join(appSupportDir(), "blocklist.json")
}

func readCachedBlocklist() *blocklist {
	data, err := os.ReadFile(blocklistCachePath())
	if err != nil {
		return nil
	}
	var bl blocklist
	if err := json.Unmarshal(data, &bl); err != nil {
		return nil
	}
	return &bl
}

func writeBlocklistCache(bl *blocklist) {
	data, err := json.Marshal(bl)
	if err != nil {
		return
	}
	_ = os.WriteFile(blocklistCachePath(), data, 0o644)
}

func blocklistStale(_ *blocklist) bool {
	info, err := os.Stat(blocklistCachePath())
	if err != nil {
		return true
	}
	ttl := 24 * time.Hour
	if v := os.Getenv("BRANCHKIT_BLOCKLIST_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}
	return time.Since(info.ModTime()) > ttl
}

func matchesBlockedSource(installSource, blockedSource string) bool {
	normalizedInstall := normalizeSource(installSource)
	normalizedBlocked := normalizeSource(blockedSource)
	return normalizedInstall == normalizedBlocked
}

func normalizeSource(source string) string {
	source = strings.TrimPrefix(source, "github:")
	source = strings.ToLower(source)
	if idx := strings.Index(source, "@"); idx != -1 {
		source = source[:idx]
	}
	return source
}

func pluginIDFromSource(source string) string {
	s := strings.TrimPrefix(source, "github:")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return ""
	}
	repo := parts[1]
	return strings.TrimPrefix(repo, "branchkit-plugin-")
}
