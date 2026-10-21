package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// cmdReport saves a problem report: a zip of BranchKit's recent logs that
// the person can open, read and send. The app builds it (`--report`), so the
// report is the same whether it comes from here or from Settings → Help →
// Report a problem…; the CLI only finds the app and hands it the folder.
// The app need not be running, which is the point: it is what someone runs
// when the app will not start. Nothing is uploaded.
func cmdReport(args []string) {
	app := ""
	pass := []string{"--report"}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--app":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "report: --app needs a path")
				os.Exit(2)
			}
			i++
			app = args[i]
		case "--audit":
			pass = append(pass, a)
		case "--out", "--hours", "--plugin-log", "--note":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "report: %s needs a value\n", a)
				os.Exit(2)
			}
			i++
			pass = append(pass, a, args[i])
		default:
			fmt.Fprintf(os.Stderr, "report: unknown argument %q\n", a)
			printReportUsage()
			os.Exit(2)
		}
	}
	if app == "" {
		app = findAppBinary()
	}
	if app == "" {
		fmt.Fprintln(os.Stderr, "report: BranchKit's app was not found. Pass its path with --app")
		fmt.Fprintln(os.Stderr, "  (macOS: /Applications/BranchKit.app/Contents/MacOS/BranchKit;")
		fmt.Fprintln(os.Stderr, "   Linux: branchkit; Windows: branchkit.exe in the install folder).")
		os.Exit(1)
	}
	// The folder this CLI targets (--dev, or the one the app handed it),
	// so the report reads that install whichever binary builds it.
	pass = append(pass, "--app-support", appSupportDir())
	cmd := exec.Command(app, pass...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "report: could not run %s: %v\n", app, err)
		os.Exit(1)
	}
}

// appBinaryCandidates lists where the app's binary may be, most specific
// first: beside this CLI (it ships inside the app on every OS), then the
// usual install locations.
func appBinaryCandidates(exeDir, home, goos string) []string {
	var c []string
	switch goos {
	case "darwin":
		c = append(c,
			// The CLI ships in the bundle's Contents/MacOS or Contents/Resources.
			filepath.Join(exeDir, "BranchKit"),
			filepath.Join(exeDir, "..", "MacOS", "BranchKit"),
			"/Applications/BranchKit.app/Contents/MacOS/BranchKit",
			filepath.Join(home, "Applications", "BranchKit.app", "Contents", "MacOS", "BranchKit"))
	case "windows":
		c = append(c, filepath.Join(exeDir, "branchkit.exe"))
	default:
		c = append(c,
			filepath.Join(exeDir, "branchkit"),
			"/usr/bin/branchkit",
			"/usr/lib/branchkit/branchkit",
			"/usr/local/bin/branchkit")
	}
	return c
}

func findAppBinary() string {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		exeDir = filepath.Dir(exe)
	}
	home, _ := os.UserHomeDir()
	for _, c := range appBinaryCandidates(exeDir, home, runtime.GOOS) {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	if runtime.GOOS == "darwin" {
		return ""
	}
	if p, err := exec.LookPath("branchkit"); err == nil {
		return p
	}
	return ""
}

func printReportUsage() {
	fmt.Println("Usage: branchkit-cli report [flags]")
	fmt.Println()
	fmt.Println("Save a problem report: a zip of BranchKit's recent logs, cleaned of what you")
	fmt.Println("said and of tokens, for you to read and send. Nothing is uploaded, and the app")
	fmt.Println("does not need to be running.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --hours <n>         How far back, 1 to 168 hours (default 24)")
	fmt.Println("  --out <folder>      Where to save it (default: Downloads)")
	fmt.Println("  --note <text>       What happened, in your words")
	fmt.Println("  --audit             Include the audit log (names the app in front on each record)")
	fmt.Println("  --plugin-log <id>   Include a plugin's own log, as it wrote it (repeatable)")
	fmt.Println("  --app <path>        The app's binary, when it is not found on its own")
}
