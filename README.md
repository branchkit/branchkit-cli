# branchkit-cli

CLI tool for managing [BranchKit](https://github.com/branchkit) plugins. Install, list, remove, and inspect plugins from the command line.

## Install

```bash
go install github.com/branchkit/branchkit-cli@latest
```

Or download a prebuilt binary from [Releases](https://github.com/branchkit/branchkit-cli/releases).

## Usage

```bash
# Install a plugin from GitHub
branchkit-cli plugin install owner/branchkit-plugin-name
branchkit-cli plugin install owner/branchkit-plugin-name@v1.0.0

# Install from a local directory
branchkit-cli plugin install ./path/to/plugin

# Build from source (clones, detects build system, builds)
branchkit-cli plugin install owner/branchkit-plugin-name --build

# List installed plugins
branchkit-cli plugin list

# Show plugin details
branchkit-cli plugin info <plugin-id>

# Remove a user-installed plugin
branchkit-cli plugin remove <plugin-id>
```

## How it works

The CLI manages plugin files in the BranchKit plugins directory (`~/Library/Application Support/BranchKit/plugins/`). It does not modify the BranchKit app itself.

When installing from GitHub, it downloads release artifacts matching `branchkit-plugin-{name}-{os}-{arch}.tar.gz` and verifies SHA256 checksums when available.

After install or remove, the CLI notifies a running BranchKit instance to reload plugins via a localhost HTTP call. If BranchKit isn't running, the plugin loads on next launch.

## Building plugins

To create a new plugin, use the [Go plugin template](https://github.com/branchkit/plugin-template-go) and the [Go plugin SDK](https://github.com/branchkit/plugin-sdk-go).

## Development

```bash
go build .
go test ./...
```

Zero external dependencies — uses only Go stdlib.

## License

MIT
