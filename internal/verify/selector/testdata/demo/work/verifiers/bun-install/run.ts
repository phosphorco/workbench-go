// Run: /work holds bun.lock; /upstream/workspace-sync holds the generated manifests.
// Installs third-party packages from the frozen lockfile with the install cache in
// /scratch, and writes a self-contained node_modules (no symlinks, no workspace links)
// to /out/node_modules. Workspace packages are Tree source, linked by their consumers.
import { cpSync, existsSync, lstatSync, mkdtempSync, readdirSync, rmSync } from "node:fs"
import { join } from "node:path"

const stage = mkdtempSync("/tmp/install-")
cpSync("/upstream/workspace-sync", stage, { recursive: true })
cpSync("/work/bun.lock", join(stage, "bun.lock"))

const installed = Bun.spawnSync(
  ["bun", "install", "--frozen-lockfile", "--linker", "hoisted", "--ignore-scripts", "--no-progress"],
  {
    cwd: stage,
    env: {
      PATH: process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin",
      HOME: "/tmp",
      BUN_INSTALL_CACHE_DIR: "/scratch/.bun-cache",
    },
    stdout: "inherit",
    stderr: "inherit",
  },
)
if (installed.exitCode !== 0) process.exit(1)

const prune = (dir: string): void => {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (entry === ".bin" || entry === ".cache") rmSync(path, { recursive: true, force: true })
    else if (lstatSync(path).isSymbolicLink()) rmSync(path)
    else if (lstatSync(path).isDirectory()) {
      prune(path)
      if (entry.startsWith("@") && readdirSync(path).length === 0) rmSync(path, { recursive: true })
    }
  }
}
const modules = join(stage, "node_modules")
if (existsSync(modules)) {
  prune(modules)
  cpSync(modules, "/out/node_modules", { recursive: true, dereference: true })
}
