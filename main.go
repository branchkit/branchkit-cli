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
				fmt.Fprintln(os.Stderr, "Usage: branchkit-cli plugin install <source> [--build]")
				os.Exit(1)
			}
			source := os.Args[3]
			build := len(os.Args) >= 5 && os.Args[4] == "--build"
			cmdInstall(source, build)
		case "check-updates":
			cmdCheckUpdates()
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
	fmt.Println("  plugin install <source> [--build]  Install a plugin")
	fmt.Println("  plugin list                        List installed plugins")
	fmt.Println("  plugin remove <plugin-id>          Remove a user-installed plugin")
	fmt.Println("  plugin info <plugin-id>            Show plugin details")
	fmt.Println("  plugin check-updates               Check for available updates (JSON)")
	fmt.Println("  plugin update [plugin-id]          Update one or all plugins")
	fmt.Println("  model download <engine/model>      Download a speech model")
	fmt.Println("  model list                         List downloaded models")
	fmt.Println("  dev init [flags]                   Scaffold a new plugin from template")
	fmt.Println("  dev build [path]                   Build a plugin from source")
	fmt.Println("  help                               Show this help")
}

func printModelUsage() {
	fmt.Println("Usage: branchkit-cli model <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  download <engine/model>  Download a speech model (e.g., vosk/vosk-model-small-en-us-0.15)")
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
}

func printPluginUsage() {
	fmt.Println("Usage: branchkit-cli plugin <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  install <source> [--build]  Install from GitHub (owner/repo) or local path")
	fmt.Println("  list                        List installed plugins")
	fmt.Println("  remove <plugin-id>          Remove a user-installed plugin")
	fmt.Println("  info <plugin-id>            Show plugin details")
	fmt.Println("  check-updates               Check for available updates (JSON output)")
	fmt.Println("  update [plugin-id]          Update one or all plugins")
}
