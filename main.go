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
		default:
			fmt.Fprintf(os.Stderr, "Unknown plugin command: %s\n", os.Args[2])
			printPluginUsage()
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
	fmt.Println("  help                               Show this help")
}

func printPluginUsage() {
	fmt.Println("Usage: branchkit-cli plugin <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  install <source> [--build]  Install from GitHub (owner/repo) or local path")
	fmt.Println("  list                        List installed plugins")
	fmt.Println("  remove <plugin-id>          Remove a user-installed plugin")
	fmt.Println("  info <plugin-id>            Show plugin details")
}
