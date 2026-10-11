// Usage: bun generate.mts /path/to/monorepo/tools/verify
import { cpSync, mkdirSync, rmSync, writeFileSync } from "node:fs"
import { join, dirname } from "node:path"
const reference = process.argv[2]
if (!reference) throw new Error("pass the tools/verify reference directory")
const { select } = await import(join(reference, "src/Select.mts"))
const harness = await import(join(reference, "demo/verifiers/harness.ts"))
const root = import.meta.dir
const paths = ["bun.lock", "workspace.pkl", "nested/bun.lock", ".hidden.ts", "a.ts", "b.tsx", "src/a.test.ts", "src/nested/b.test.ts", "packages/money/src/money.ts", "packages/invoice/src/total.test.ts", "a.txt"] .sort()
const patterns = ["bun.lock", "workspace.pkl", "**/*.ts", "**/*.test.ts", "packages/**", "*.ts", "src/**/*.test.ts", "**/*.{ts,tsx}", "[ab].ts", "**", "nothing/**"]
const tree = {hash: "", root: "", entries: new Map(paths.map(path => [path, {path, mode: "100644", oid: ""}])), env: {}}
writeFileSync(join(root, "globs.json"), JSON.stringify({paths, cases: patterns.map(pattern => ({pattern, matches: paths.filter(path => new Bun.Glob(pattern).match(path)), units: select(tree, "v", {kind: "Paths", patterns: [pattern]})})), whole: select(tree, "v", {kind: "WholeTree"})}, null, 2) + "\n")
const demoRoot = join(root, "demo")
rmSync(demoRoot, {recursive: true, force: true})
mkdirSync(demoRoot, {recursive: true})
for (const path of harness.tracked()) {
 const target = join(demoRoot, "work", path)
 mkdirSync(dirname(target), {recursive: true})
 cpSync(join(harness.repo, path), target)
}
const sync = join(demoRoot, "workspace-sync")
mkdirSync(sync)
const evaluated = Bun.spawnSync(["pkl", "eval", "-f", "json", "workspace.pkl"], {cwd: harness.repo, stdout: "pipe", stderr: "pipe"})
if (evaluated.exitCode !== 0) throw new Error(evaluated.stderr.toString())
for (const [path, content] of Object.entries(JSON.parse(evaluated.stdout.toString()).files)) {
 const target = join(sync, path)
 mkdirSync(dirname(target), {recursive: true})
 writeFileSync(target, JSON.stringify(content, null, 2) + "\n")
}
const install = join(demoRoot, "bun-install")
mkdirSync(install)
try {
 const units = await harness.select({id: "bun-test", upstream: {"workspace-sync": sync, "bun-install": install}})
 writeFileSync(join(root, "demo-units.json"), JSON.stringify({tracked: harness.tracked(), units: units.map(unit => ({...unit, display: unit.unitId}))}, null, 2) + "\n")
} finally { harness.cleanup() }
