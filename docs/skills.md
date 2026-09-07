# Bundled agent skills

Workbench embeds two independent skill folders, including their references and
assets: [workbench](../skills/workbench/SKILL.md) for environment operations and
[workbench-plan](../skills/workbench-plan/SKILL.md) for optional task graphs.
Each folder is the unit of export; neither skill requires the other.

## Export for your agent

```sh
workbench self skills list
workbench self skills export workbench
workbench self skills export workbench-plan
```

`--skills-dir` defaults to `.agents/skills` relative to the current directory.
The exporter creates `<skills-dir>/<skill-name>/` and returns a JSON receipt
listing its paths. Both `--skills-dir DIR` and `--skills-dir=DIR` are accepted.
Export flags may appear before or after the skill name. `list` is read-only
and returns names, descriptions, and exact export commands as JSON.

| Harness | Project folder | Personal folder (all projects) |
| --- | --- | --- |
| [Codex](https://learn.chatgpt.com/docs/build-skills#where-codex-loads-local-skills) | `.agents/skills` | `$HOME/.agents/skills` |
| [Claude Code](https://code.claude.com/docs/en/skills#where-skills-live) | `.claude/skills` | `$HOME/.claude/skills` |

For example, export the planning skill for personal use in Claude Code:

```sh
workbench self skills export workbench-plan --skills-dir="$HOME/.claude/skills"
```

Existing skill folders are preserved. For updates, export into a fresh directory,
compare the complete trees, then replace the installed folder intentionally.
Exporting one skill does not reconcile or remove other installed skills. In a
Workbench-managed context, export into a source catalog and select it through
resource declarations; setup owns generated projections.

Exit codes for these commands are `0` for success or help, `2` for invalid
invocations, `3` for existing destinations, and `1` for operational failures.
Errors go to stderr; list results and export receipts go to stdout. A collision
names the preserved folder and a command for exporting a fresh comparison copy.
Old `self skill` and `--pkl-version` spellings produce corrective commands without
writing files. Export receipts name the effective reference version as
`pklPackageVersion`.

## Configure release references

`--pkl-package-version VERSION` substitutes the versioned GitHub release package URI in
reference documents. The default follows Workbench's configured Pkl contract
publication, which is independent of its binary release version. For an explicit
contract reference:

```sh
workbench self skills export workbench-plan --skills-dir=./exported-skills --pkl-package-version=0.7.0
```

This configures publication references without fetching them or asserting that
the release exists. It does not change the evaluator's supported contracts.
Runnable plan examples retain `workbench:plan`, the schema embedded in the binary;
release package URLs are not accepted as plan imports.

## Bundle contract

Markdown sources use Go templates with the typed `PklPackageURI` parameter.
Missing template fields fail export. Non-Markdown assets retain their bytes.
Rendered folders pass through the same catalog validator and skill selection
used for repository skills; all linked references travel with the selected skill.

Release archives contain rendered folders under `share/workbench/skills/` and
`share/workbench/pkl/Plan.pkl`. Archive construction and CLI export use the same
embedded sources and rendering function. Pkl package ZIPs remain contract-only.
