package main

// `branchkit-cli app …`: BranchKit's own releases, as opposed to plugins'.
// The app never fetches anything itself; when it wants an update it runs
// these commands, whose network requests are this open-source CLI's.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"
)

// appEnv is everything an app command reads from outside its arguments, so a
// test can run a whole command against a fake GitHub and a test Sigstore.
type appEnv struct {
	client     *http.Client
	stall      time.Duration // a response that sends nothing for this long fails (0: appStallTimeout)
	anchors    []trustAnchor
	appSupport string
	goos       string
	goarch     string
	getenv     func(string) string
	stdout     io.Writer
	stderr     io.Writer
}

// appStallTimeout is how long a download may go without a byte. A release
// file is tens to hundreds of megabytes, so nothing bounds a download's
// total time: a slow link that keeps sending finishes, one that stops fails.
const appStallTimeout = 60 * time.Second

// liveAppEnv is the real machine: GitHub, the embedded trusted roots and the
// app-support folder this CLI targets.
func liveAppEnv() (appEnv, error) {
	anchors, err := trustAnchors()
	if err != nil {
		return appEnv{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = appStallTimeout
	return appEnv{
		// No Client.Timeout: it would bound the whole body too (appGet
		// guards stalls instead).
		client:     &http.Client{Transport: transport},
		stall:      appStallTimeout,
		anchors:    anchors,
		appSupport: appSupportDir(),
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		getenv:     os.Getenv,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}, nil
}

// appCommands maps each `app` subcommand to its runner.
var appCommands = map[string]func(args []string, env appEnv) error{
	"download": runAppDownload,
}

func cmdApp(args []string) {
	if len(args) == 0 {
		printAppUsage()
		os.Exit(1)
	}
	run, ok := appCommands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown app command: %s\n", args[0])
		printAppUsage()
		os.Exit(1)
	}
	env, err := liveAppEnv()
	if err == nil {
		err = run(args[1:], env)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "app %s: %v\n", args[0], err)
		os.Exit(1)
	}
}

func printAppUsage() {
	fmt.Println("Usage: branchkit-cli app <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  download [--version vX.Y.Z] [--current vX.Y.Z] [--kind KIND] [--allow-downgrade]")
	fmt.Println("        Download a BranchKit release for this machine into the app's updates/")
	fmt.Println("        folder and verify it: GitHub's release attestation of branchkit/releases")
	fmt.Println("        must cover the release's SHA256SUMS, and the file must match its line")
	fmt.Println("        there. Prints JSON {version, tag, kind, path, sha256, identity}.")
	fmt.Println("        Installs nothing.")
	fmt.Println("        --version   the release to fetch (default: the latest)")
	fmt.Println("        --current   the installed app's version (or BRANCHKIT_APP_VERSION);")
	fmt.Println("                    a release that is not newer is refused")
	fmt.Println("        --kind      which file: the one the updater uses by default (macOS zip,")
	fmt.Println("                    Windows zip, Linux AppImage); also dmg, deb, rpm")
	fmt.Println("        --allow-downgrade  fetch an older or equal release, or with no --current")
}
