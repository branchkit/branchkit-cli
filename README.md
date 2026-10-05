# branchkit-cli

The command-line tool for [BranchKit](https://github.com/branchkit), an
accessibility plugin platform for the desktop. It scaffolds, builds, tests and
packages plugins; installs, updates and removes them; and queries a running app
when a plugin does not behave.

BranchKit is pre-launch: the app is not publicly released. The authoring
commands (`dev init`, `dev build`, `dev test`, `dev platforms`,
`plugin package`) work without it. Commands that install into or query the app
need it installed and, for most, running.

## Install

```bash
go install github.com/branchkit/branchkit-cli@latest
```

Requires Go 1.25.8 or newer. No prebuilt binaries are published yet.

## Write a plugin

```bash
branchkit-cli dev init --name my-plugin --template go   # or: ts | py
cd my-plugin
branchkit-cli dev test .
```

`dev init` asks for anything you leave out. It writes a working plugin (one
action, two voice commands, one key binding, a settings tab, tests, and a
GitHub workflow), resolves the SDK, and builds or import-checks the result.
It refuses to leave behind a scaffold that does not compile. `--bare` writes
the files only, with no network and no build.

The generated plugin's own README covers building, testing and installing it.
The same output is published as starter repositories:
[Go](https://github.com/branchkit/branchkit-plugin-helloworld-go),
[TypeScript](https://github.com/branchkit/branchkit-plugin-helloworld-ts),
[Python](https://github.com/branchkit/branchkit-plugin-helloworld-py). Those
repositories are generated from `templates/` here, so changes to them belong in
this repository.

## Commands

Run `branchkit-cli help`, `branchkit-cli <group> --help`, or add `--help` to
any command for its flags.

### Plugins

| Command | What it does |
|---|---|
| `plugin install <source>` | Install a plugin. `<source>` is a catalog name (`keyboard`), `github:owner/repo[@version]`, or a local path (`.`) |
| `plugin install <source> --build` | Build from source first: a local directory in place, or a GitHub repo cloned and built |
| `plugin install <source> --preview` | Show what a GitHub plugin would be granted and do, without installing |
| `plugin list` | List installed plugins |
| `plugin info <plugin-id>` | Show a plugin's manifest details |
| `plugin remove <plugin-id>` | Remove a user-installed plugin (bundled plugins cannot be removed) |
| `plugin check-updates` | Report available updates as JSON |
| `plugin update [plugin-id]` | Update one plugin, or every plugin with a newer release |
| `plugin check-blocklist` | Check installed plugins against the registry blocklist |
| `plugin package [dir]` | Build a release tarball and checksum (`--binary`, `--os`, `--arch`, `--name`, `--out`, `--exclude`) |

Install flags: `--force` skips the blocklist check; `--yes` (`-y`) answers the
consent prompt. On a terminal, install shows what the plugin asks for (its
privileges, filesystem, network hosts) and asks before anything is written; an
update shows what changed.

### Authoring

These need no running app.

| Command | What it does |
|---|---|
| `dev init` | Scaffold a plugin (`--name`, `--template go\|ts\|py`, `--description`, `--bare`) |
| `dev build [path]` | Build with the manifest's `dev.build` recipe, or by source layout. `--os`/`--arch` cross-build into `dist/<os>-<arch>/` |
| `dev test [path]` | Static checks of the manifest and source, then conformance under the test harness when it is found (`--static-only`, `--json`) |
| `dev platforms [path...]` | Which operating systems the plugin will work on, derived from the operations its source calls. `--write` records the result in `plugin.json`; `--check` fails when the recorded list is stale |
| `docs path` | Print the directory of platform docs bundled with the app (markdown, for grep) |
| `docs sync` | Copy the bundled docs to a stable path that survives app updates |
| `version` | Print the CLI's version |

The test harness (`branchkit-test-harness`) runs a real matcher and event bus
against a plugin without the app. It ships inside the app; set
`BRANCHKIT_TEST_HARNESS` to use a copy elsewhere.

### Diagnosing a plugin in the running app

These talk to a running BranchKit. On an installed app, turn on **Developer
Access** on the plugin's card in BranchKit's settings: the CLI then uses that
grant, which answers for that one plugin only.

| Command | What it does |
|---|---|
| `dev watch [path]` | Rebuild on change and have the app restart the plugin |
| `dev plog <plugin-id>` | Query the plugin's log (`--since 30s`, `--tag`, `--exclude`, `--level`, `--limit`, `--json`) |
| `dev logs [plugin-id]` | Tail the app log, optionally filtered (`--source`, `--json`) |
| `dev events` | Query event records (`--tr`, `--types`, `--plugin`, `--severity`, `--since`, `--source show-all\|audit`). `--source audit --types 'consent.**'` shows what a plugin attempted and what was refused |
| `dev chain [tr_id]` | Follow one command's correlated event chain; with no id, recent chains (under Developer Access, the plugin's own recent records) |
| `dev say <text> --simulate` | Run a phrase through matching and report what would have executed, without executing it |
| `dev vocab <word>...` | Whether each word can be recognized right now, and why not |

`dev say` without `--simulate` really executes whatever the phrase matches.

### Development builds of the app only

These read or change app-wide state, so a Developer Access grant cannot run
them. They need a development build of BranchKit (`--dev` points the CLI at
its data folder).

| Command | What it does |
|---|---|
| `dev smoke` | Side-effect-free health sweep of the running app |
| `dev margins --collection NAME` | Recognition-margin distribution of a keyed recognition log |
| `dev bisect` | Find which plugin causes a symptom by disabling dependency-closed halves (`--pin`, `--check`, `restore`, `cancel`) |
| `dev trial --template go\|ts\|py` | Scaffold a plugin, install it in the running app, check it starts, matches and renders, then remove it |

### Other

| Command | What it does |
|---|---|
| `artifact list` | List installed artifacts (models and other large files) and what plugins declare |
| `artifact download <plugin/name>` | Download an artifact a plugin declares |
| `runtime install python` | Install the managed CPython that Python plugins run under |
| `runtime list` | List installed managed runtimes |
| `mcp --connection <id>` | Serve BranchKit's connection reads to an AI app over MCP (stdio). Create the connection in BranchKit's settings first |
| `registry keygen`, `registry sign` | Registry maintainers only: the catalog counter-signature |

## Where things go

The CLI works on BranchKit's data folder and never modifies the app itself:

| OS | Data folder |
|---|---|
| macOS | `~/Library/Application Support/BranchKit` |
| Linux | `$XDG_DATA_HOME/BranchKit` (default `~/.local/share/BranchKit`) |
| Windows | `%APPDATA%\BranchKit` |

Plugins install into `plugins/` there. When the app runs the CLI it passes its
folder in `BRANCHKIT_APP_SUPPORT`, which wins. `--dev` (or `BRANCHKIT_DEV=1`)
selects a development build's folder, `BranchKitDev`.

After an install or removal, the CLI asks a running app to reload plugins. If
none is running, the change takes effect at next launch.

## Installing from GitHub

A catalog name is resolved through the [registry](https://github.com/branchkit/registry)
to a `github:owner/repo` source. The CLI downloads the release asset
`branchkit-plugin-<name>-<os>-<arch>.tar.gz` (where `<name>` is the repository
name without its `branchkit-plugin-` prefix) and then:

- verifies `<asset>.sha256` when the release publishes one;
- verifies a Sigstore attestation when present and checks it names the
  repository the plugin's `publisher` field claims; a plugin whose attestation
  contradicts its stated publisher is refused;
- for a catalog install, checks the registry's counter-signature over the
  manifest; a present but invalid signature is refused;
- refuses sources on the registry blocklist unless `--force` is given.

Unsigned plugins still install, and are shown as unsigned. The release workflow
that `dev init` writes for a Go plugin produces signed assets in this format.

No plugin in the catalog has published a release yet, so installing by catalog
name does not succeed today. Local installs (`plugin install .`) work.

## Development

```bash
go build .
go test ./...
```

## License

MIT
