package main

// `branchkit-cli app check-updates`: whether a BranchKit release newer than
// the installed one is out. The app runs it for its update check, so the
// request is this open-source CLI's, never the app's. It reads one thing,
// the latest release of the contract's repository, and downloads nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// appUpdateCheck is what `app check-updates` prints.
type appUpdateCheck struct {
	Current string `json:"current"`
	// Latest is the newest release's version, or "" when none is known: the
	// repository has no release yet, the check could not reach GitHub, or
	// the newest is a pre-release.
	Latest string `json:"latest"`
	Newer  bool   `json:"newer"`
	// NotesURL is the release's page on GitHub ("" with no Latest).
	NotesURL string `json:"notes_url"`
	// Apply says who installs an update here: "self" (this CLI's updater)
	// or "package-manager" (apt or dnf owns the files).
	Apply string `json:"apply"`
}

const (
	applySelf           = "self"
	applyPackageManager = "package-manager"
)

// appApplyMode is who installs an update on this machine. On Linux a .deb or
// .rpm package owns the app's files, so its package manager updates them;
// the AppImage's runtime sets $APPIMAGE (packaging/linux/AppRun reads it
// too), and no package manager owns an AppImage. macOS and Windows update
// themselves.
func appApplyMode(goos string, getenv func(string) string) string {
	if goos == "linux" && getenv("APPIMAGE") == "" {
		return applyPackageManager
	}
	return applySelf
}

func parseAppCheckArgs(args []string, env appEnv) (string, error) {
	current := ""
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		if name != "--current" {
			return "", fmt.Errorf("unknown argument %q", args[i])
		}
		if !hasValue {
			if i+1 >= len(args) {
				return "", fmt.Errorf("--current needs a value")
			}
			i++
			value = args[i]
		}
		if value == "" {
			return "", fmt.Errorf("--current needs a value")
		}
		current = value
	}
	if current == "" {
		current = env.getenv("BRANCHKIT_APP_VERSION")
	}
	if current == "" {
		return "", fmt.Errorf("the installed version is unknown: pass --current (or set BRANCHKIT_APP_VERSION)")
	}
	current = strings.TrimPrefix(current, "v")
	if !appVersionPattern.MatchString(current) {
		return "", fmt.Errorf("version %q is not X.Y.Z", current)
	}
	return current, nil
}

// runAppCheckUpdates prints an appUpdateCheck on stdout and ends stderr with
// `CHECKED reached=N unreached=M`, the summary line `plugin check-updates`
// ends with too: the app counts a check that reached GitHub, and retries one
// that did not (offline) sooner. A repository with no release yet answers
// 404, which is an answer: reached, nothing newer. A check that could not
// reach GitHub still exits 0 and prints its JSON (Latest ""), so the app
// reads one shape; the summary says it did not count.
func runAppCheckUpdates(args []string, env appEnv) error {
	current, err := parseAppCheckArgs(args, env)
	if err != nil {
		return err
	}
	contract, err := loadReleaseContract()
	if err != nil {
		return err
	}
	out := appUpdateCheck{Current: current, Apply: appApplyMode(env.goos, env.getenv)}
	reached, unreached := 1, 0
	latest, err := latestAppRelease(env, contract)
	switch {
	case err != nil:
		fmt.Fprintf(env.stderr, "WARN: could not check %s: %v\n", contract.Repository, err)
		reached, unreached = 0, 1
	case latest != "":
		out.Latest = latest
		// Built from the repository and the checked tag rather than taken
		// from the answer's html_url, so the app links only to GitHub.
		out.NotesURL = "https://github.com/" + contract.Repository + "/releases/tag/" + contract.tag(latest)
		have, _ := parseSemver(current)
		next, _ := parseSemver(latest)
		out.Newer = have.Less(next)
	}
	data, _ := json.Marshal(out)
	fmt.Fprintln(env.stdout, string(data))
	fmt.Fprintf(env.stderr, "CHECKED reached=%d unreached=%d\n", reached, unreached)
	return nil
}

// latestAppRelease is the version of the contract repository's latest
// release, "" when it has none (404) or the latest is not a release the
// updater takes (a pre-release, a draft, a tag that is not vX.Y.Z), and an
// error when GitHub could not be asked or gave no readable answer. A body
// that is not the release JSON counts as unreached: a captive portal's page
// arrives with a 200 too.
//
// The request is always anonymous. The repository is public, and the
// person's GitHub token (githubDo's retry for a private repository) would
// tie a daily check to their account. A spent anonymous rate limit is an
// unreached check, retried later.
func latestAppRelease(env appEnv, contract *releaseContract) (string, error) {
	url := "https://api.github.com/repos/" + contract.Repository + "/releases/latest"
	req, err := newGitHubRequest(url)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := env.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub: %w", err)
	}
	idle := env.stall
	if idle == 0 {
		idle = appStallTimeout
	}
	body := guardStalls(resp.Body, idle)
	defer body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return "", fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	var release struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxReleaseJSONBytes)).Decode(&release); err != nil {
		return "", fmt.Errorf("read release: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("read release: no tag_name")
	}
	version := strings.TrimPrefix(release.TagName, contract.TagPrefix)
	if release.Prerelease || release.Draft || release.TagName != contract.tag(version) || !appVersionPattern.MatchString(version) {
		fmt.Fprintf(env.stderr, "The latest release, %s, is not a release the updater takes; ignored.\n", release.TagName)
		return "", nil
	}
	return version, nil
}
