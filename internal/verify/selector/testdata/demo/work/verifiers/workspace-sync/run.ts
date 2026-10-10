// Run: /work holds workspace.pkl. Writes every generated package.json and tsconfig
// at its repository-relative path under /out.
import { mkdirSync, writeFileSync } from "node:fs"
import { dirname, join } from "node:path"

const evaluated = Bun.spawnSync(["pkl", "eval", "-f", "json", "workspace.pkl"], {
  cwd: "/work",
  stdout: "pipe",
  stderr: "inherit",
})
if (evaluated.exitCode !== 0) process.exit(1)

const { files } = JSON.parse(evaluated.stdout.toString()) as { files: Record<string, unknown> }
for (const [path, content] of Object.entries(files)) {
  if (path.startsWith("/") || path.split("/").includes("..")) throw new Error(`refusing path ${path}`)
  const target = join("/out", path)
  mkdirSync(dirname(target), { recursive: true })
  writeFileSync(target, `${JSON.stringify(content, null, 2)}\n`)
}
