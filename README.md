# feature-implement

`feature-implement` provides a delegated `mise-en-place` skill and Go CLI for turning implementation scope into validated epic, feature, story, and merge-unit plans, then executing those plans through guarded branch and PR workflows.

It installs:

- `feature`, a self-contained Go CLI under `~/.local/bin`
- `$feature` and `$feature:implement` for Codex
- `/feature` and `/feature:implement` for Claude Code

## Install Contract

This repo follows the `mise-en-place` delegated installer contract. `install-skill.sh` is a thin wrapper around:

```sh
feature install-skills [--plan|--install|--uninstall] [--target claude|codex|tools|all] [--json] [--install-root <dir>]
```

The installer emits delegated JSON with `schema: 1`, `kind: delegated`, target file records, setup metadata, and SHA256 hashes for installed files. Go/self-contained tool installation is owned by the delegated installer through the `tools` target.

## Commands

```sh
feature plan materialize --manifest feature.plan.yaml --json
feature validate <plan-dir> --write-lock --json
feature status <plan-dir> --json
feature implement next <plan-dir> --json
feature implement push <plan-dir> --merge-unit <id> --allow-push --json
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --json
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --allow-delete-branch --json
```

`feature validate` writes `feature.plan.lock.json`; implementation commands consume that validated snapshot rather than live-edited Markdown.

## Development

Run the full local check:

```sh
go test ./...
./install-skill.sh --plan --target all --json
stage="$(mktemp -d)"
./install-skill.sh --install --target all --json --install-root "$stage"
"$stage/.local/bin/feature" version
```
