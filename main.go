package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "plugin":
		if len(os.Args) < 3 {
			printPluginUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "list":
			cmdList()
		case "info":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli plugin info <plugin-id>")
				os.Exit(1)
			}
			cmdInfo(os.Args[3])
		case "remove":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli plugin remove <plugin-id>")
				os.Exit(1)
			}
			cmdRemove(os.Args[3])
		case "install":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli plugin install <source> [--build] [--force] [--preview]")
				os.Exit(1)
			}
			source := os.Args[3]
			var build, force, preview bool
			for _, arg := range os.Args[4:] {
				switch arg {
				case "--build":
					build = true
				case "--force":
					force = true
				case "--preview":
					preview = true
				}
			}
			if preview {
				cmdPreview(source)
			} else {
				cmdInstall(source, build, force)
			}
		case "package":
			cmdPluginPackage(os.Args[3:])
		case "check-updates":
			cmdCheckUpdates()
		case "check-blocklist":
			cmdCheckBlocklist()
		case "update":
			pluginID := ""
			if len(os.Args) >= 4 {
				pluginID = os.Args[3]
			}
			cmdUpdate(pluginID)
		default:
			fmt.Fprintf(os.Stderr, "Unknown plugin command: %s\n", os.Args[2])
			printPluginUsage()
			os.Exit(1)
		}
	case "model":
		if len(os.Args) < 3 {
			printModelUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "download":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli model download <engine/model-name>")
				os.Exit(1)
			}
			cmdModelDownload(os.Args[3])
		case "list":
			cmdModelList()
		default:
			fmt.Fprintf(os.Stderr, "Unknown model command: %s\n", os.Args[2])
			printModelUsage()
			os.Exit(1)
		}
	case "dev":
		if len(os.Args) < 3 {
			printDevUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "init":
			cmdDevInit(os.Args[3:])
		case "build":
			cmdDevBuild(os.Args[3:])
		case "test":
			cmdDevTest(os.Args[3:])
		case "watch":
			cmdDevWatch(os.Args[3:])
		case "logs":
			cmdDevLogs(os.Args[3:])
		case "smoke":
			cmdDevSmoke(os.Args[3:])
		case "say":
			cmdDevSay(os.Args[3:])
		case "chain":
			cmdDevChain(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "Unknown dev command: %s\n", os.Args[2])
			printDevUsage()
			os.Exit(1)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("branchkit-cli — BranchKit plugin manager")
	fmt.Println()
	fmt.Println("Usage: branchkit-cli <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  plugin install <source> [--build] [--force]  Install a plugin")
	fmt.Println("  plugin list                        List installed plugins")
	fmt.Println("  plugin remove <plugin-id>          Remove a user-installed plugin")
	fmt.Println("  plugin info <plugin-id>            Show plugin details")
	fmt.Println("  plugin check-updates               Check for available updates (JSON)")
	fmt.Println("  plugin update [plugin-id]          Update one or all plugins")
	fmt.Println("  model download <engine/model>      Download a speech model")
	fmt.Println("  model list                         List downloaded models")
	fmt.Println("  dev init [flags]                   Scaffold a new plugin from template")
	fmt.Println("  dev build [path]                   Build a plugin from source")
	fmt.Println("  dev test [path] [flags]            Run static analysis on a plugin")
	fmt.Println("  dev watch [path]                   Watch + rebuild + reload on changes")
	fmt.Println("  dev logs [plugin-id] [flags]       Tail actuator log")
	fmt.Println("  help                               Show this help")
}

func printModelUsage() {
	fmt.Println("Usage: branchkit-cli model <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  download <engine/model>  Download a speech model (e.g., whisperkit/openai_whisper-large-v3-v20240930)")
	fmt.Println("  list                     List downloaded models")
}

func printDevUsage() {
	fmt.Println("Usage: branchkit-cli dev <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init [--name NAME] [--template go] [--description DESC]")
	fmt.Println("        Scaffold a new plugin from template")
	fmt.Println("  build [path]")
	fmt.Println("        Detect build system and build plugin binary")
	fmt.Println("  test [path] [--static-only] [--json]")
	fmt.Println("        Run static analysis on a plugin")
	fmt.Println("  watch [path]")
	fmt.Println("        Watch for changes, rebuild, and reload via actuator")
	fmt.Println("  logs [plugin-id] [--source TAG] [--json]")
	fmt.Println("        Tail actuator log, optionally filtered")
	fmt.Println("  smoke [--json]")
	fmt.Println("        Side-effect-free health sweep of the running app (preview resolves,")
	fmt.Println("        vocab lag, transcript transport) — executes nothing")
	fmt.Println("  say <text> [--pipeline NAME]")
	fmt.Println("        Inject a synthetic transcript — matched commands REALLY execute")
	fmt.Println("  chain [tr_id] [--limit N] [--json]")
	fmt.Println("        Query correlated event chains (no id = recent-chains index)")
}

func printPluginUsage() {
	fmt.Println("Usage: branchkit-cli plugin <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  install <source> [--build] [--force]  Install from GitHub (github:owner/repo) or local path")
	fmt.Println("  list                        List installed plugins")
	fmt.Println("  remove <plugin-id>          Remove a user-installed plugin")
	fmt.Println("  info <plugin-id>            Show plugin details")
	fmt.Println("  package [dir] [--binary P] [--os O] [--arch A] [--name N] [--out D]")
	fmt.Println("        Build the release tarball + checksum (language/CI-agnostic; sign & upload next)")
	fmt.Println("  check-updates               Check for available updates (JSON output)")
	fmt.Println("  update [plugin-id]          Update one or all plugins")
}
