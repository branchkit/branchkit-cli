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
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli plugin install <source> [--build] [--force] [--preview] [--yes]")
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
				case "--yes", "-y":
					installAssumeYes = true
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
	case "runtime":
		if len(os.Args) < 3 {
			printRuntimeUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "install":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli runtime install <name>")
				os.Exit(1)
			}
			cmdRuntimeInstall(os.Args[3])
		case "list":
			cmdRuntimeList()
		default:
			fmt.Fprintf(os.Stderr, "Unknown runtime command: %s\n", os.Args[2])
			printRuntimeUsage()
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
		case "plog":
			cmdDevPlog(os.Args[3:])
		case "smoke":
			cmdDevSmoke(os.Args[3:])
		case "say":
			cmdDevSay(os.Args[3:])
		case "chain":
			cmdDevChain(os.Args[3:])
		case "margins":
			cmdDevMargins(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "Unknown dev command: %s\n", os.Args[2])
			printDevUsage()
			os.Exit(1)
		}
	case "docs":
		cmdDocs(os.Args[2:])
	case "registry":
		if len(os.Args) < 3 {
			printRegistryUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "keygen":
			cmdRegistryKeygen(os.Args[3:])
		case "sign":
			cmdRegistrySign(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "Unknown registry command: %s\n", os.Args[2])
			printRegistryUsage()
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
	fmt.Println("  runtime install <name>             Install a managed language runtime (python)")
	fmt.Println("  runtime list                       List installed managed runtimes")
	fmt.Println("  docs path                          Print the platform docs directory (markdown — grep it)")
	fmt.Println("  docs sync                          Copy bundled docs to a stable path")
	fmt.Println()
	fmt.Println("  dev init [flags]                   Scaffold a new plugin from template")
	fmt.Println("  dev build [path]                   Build a plugin from source")
	fmt.Println("  dev test [path] [flags]            Run static analysis on a plugin")
	fmt.Println("  dev watch [path]                   Watch + rebuild + reload on changes")
	fmt.Println()
	fmt.Println("  Diagnose a plugin against the running app:")
	fmt.Println("  dev smoke [--json]                 Side-effect-free health sweep — executes nothing")
	fmt.Println("  dev logs [plugin-id] [flags]       Tail actuator log")
	fmt.Println("  dev plog <plugin-id> [flags]       Query a plugin's debug log (--since/--tag/--exclude)")
	fmt.Println("  dev chain [tr_id] [--json]         Follow one command's correlated event chain")
	fmt.Println("  dev say <text>                     Inject a transcript — matched commands REALLY execute")
	fmt.Println("  dev margins [--collection NAME]    Recognition-margin distribution")
	fmt.Println()
	fmt.Println("  help                               Show this help")
}

func printRuntimeUsage() {
	fmt.Println("Usage: branchkit-cli runtime <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  install <name>  Install a managed language runtime (available: python)")
	fmt.Println("  list            List installed managed runtimes")
}

func printModelUsage() {
	fmt.Println("Usage: branchkit-cli model <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  download <plugin/model>  Provision a model a plugin declares")
	fmt.Println("  list                     List installed models and what plugins declare")
	printDeclaredModels(os.Stdout)
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
	fmt.Println("  plog <plugin-id> [--since 30s] [--tag GLOB[,GLOB]] [--exclude GLOB[,GLOB]] [--level warn] [--limit N] [--json]")
	fmt.Println("        One-shot query of plugin-logs/<id>.log — server does the time math")
	fmt.Println("  smoke [--json]")
	fmt.Println("        Side-effect-free health sweep of the running app (preview resolves,")
	fmt.Println("        vocab lag, transcript transport) — executes nothing")
	fmt.Println("  say <text> [--pipeline NAME]")
	fmt.Println("        Inject a synthetic transcript — matched commands REALLY execute")
	fmt.Println("  chain [tr_id] [--limit N] [--json]")
	fmt.Println("        Query correlated event chains (no id = recent-chains index)")
	fmt.Println("  margins [--collection NAME]")
	fmt.Println("        Recognition-margin distribution (verdict-split) of a keyed")
	fmt.Println("        recognition log, read via its compacted projection — for floor siting")
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
