# My Plugin

A BranchKit plugin

## Build

```bash
branchkit-cli dev build
```

## Test

```bash
branchkit-cli dev test .
```

## Install

```bash
branchkit-cli plugin install . --build
```

## Files

| File | Purpose |
|---|---|
| `plugin.json` | Manifest — declares actions, keybinds, voice commands |
| `commands.json` | Voice command patterns that trigger actions |
| `src/main.go` | Handler logic — your plugin's behavior |

## Platform documentation

The full platform docs ship with the app as markdown. Grep them rather than
guessing — they are the reference for the manifest, the RPC surface, matching,
collections, and the event bus.

```bash
branchkit-cli docs sync          # once, after installing or updating BranchKit
grep -rl "requires_tags" "$(branchkit-cli docs path)"
```

## When it does not work

The running app answers questions no document can, because the answer depends
on what else is installed and what state the machine is in.

```bash
branchkit-cli dev smoke                    # side-effect-free health sweep
branchkit-cli dev plog my-plugin --since 60s   # what this plugin logged
branchkit-cli dev chain                    # recent command chains, then: dev chain <tr_id>
```

## Learn more

- [Your First Plugin](https://branchkit.dev/guide/getting-started/your-first-plugin)
- [Plugin SDK (Go)](https://github.com/branchkit/plugin-sdk-go)
