---
name: branchkit-plugin
description: Build, change, test or debug a BranchKit plugin, a program with a plugin.json manifest written in Go, TypeScript or Python against a BranchKit SDK. Use when the user wants a BranchKit plugin or a voice command, key binding, settings tab or HUD for BranchKit, or works with branchkit-cli.
---

# BranchKit plugins

BranchKit is a desktop plugin platform. A plugin is a separate program that
speaks JSON-RPC with the BranchKit app, and its `plugin.json` declares what
it provides and what it needs. Plugins run sandboxed.

## Check the tools

```bash
branchkit-cli version
```

The CLI ships inside the BranchKit app. If it is missing, BranchKit is not
installed: point the user to https://branchkit.dev/install and stop, since
nothing can be built or tested without it.

## A new plugin

Ask which language the user wants unless they said. Go needs Go 1.24 or
newer; TypeScript and Python need nothing beyond the app. Pass every flag,
or `dev init` stops to ask:

```bash
branchkit-cli dev init --name <id> --template go|ts|py --description "<one line>"
cd <id>
```

`<id>` is lowercase letters, digits and hyphens. Then read `AGENTS.md` in
the new directory and follow it: it has the build and test loop, a map from
each kind of task to the docs page that explains it, and the rules that are
easy to get wrong.

## An existing plugin

Read its `AGENTS.md` and follow it. If there is none, write one:

```bash
branchkit-cli dev agents .
```

## Without the app

To read about BranchKit without it installed, the docs index is
https://branchkit.dev/llms.txt. Every page is served as markdown at its URL
plus `.md`, and https://branchkit.dev/llms/plugin-author.txt is the pages a
first plugin needs, in one file.

## Never, unless the user asks

- Install the plugin into the running app (`branchkit-cli plugin install`)
  or run `dev watch`: the app on the user's machine is in use, and
  `branchkit-cli dev test .` checks everything without it.
- Edit a generated `actions_gen` file by hand. `AGENTS.md` says how to
  regenerate it.
