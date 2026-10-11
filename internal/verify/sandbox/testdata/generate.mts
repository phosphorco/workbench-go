// Run: bun generate.mts /absolute/path/to/monorepo/tools/verify/src/Sandbox.mts
import * as Fs from "node:fs/promises"
import * as Os from "node:os"
import * as Path from "node:path"
import { pathToFileURL } from "node:url"

const reference = await import(pathToFileURL(process.argv[2]).href)
const store = await import(pathToFileURL(Path.join(Path.dirname(process.argv[2]), "Store.mts")).href)
const image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
const stage = await Fs.mkdtemp(Path.join(Os.tmpdir(), "sandbox-golden-"))
try {
  const mounts = []
  for (const name of ["work", "verifier", "out"]) {
    const host = Path.join(stage, name)
    await Fs.mkdir(host)
    mounts.push({ host, container: `/${name}`, writable: name === "out" })
  }
  const fixtures = []
  for (const [unitId, role] of [["unit<&>", "run"], ["ユニット\n\"\\", "run"], ["*", "select"]]) {
    const outcome = await reference.execute({
      image, command: ["cat"], mounts,
      env: { VERIFY_UNIT: unitId, VERIFY_ROLE: role },
      stdin: JSON.stringify({ unitId }), network: "none", timeoutSeconds: 15,
    })
    if (outcome.exitCode !== 0 || outcome.timedOut) throw new Error(outcome.stderr)
    fixtures.push({ unitId, role, stdout: outcome.stdout.toString("utf8") })
  }
  await Fs.writeFile(new URL("./stdin.json", import.meta.url), JSON.stringify(fixtures, null, 2) + "\n")
  const artifact = Path.join(stage, "out")
  await Fs.mkdir(Path.join(artifact, "node_modules", ".bin"), { recursive: true })
  await Fs.mkdir(Path.join(artifact, "node_modules", "package"), { recursive: true })
  await Fs.writeFile(Path.join(artifact, "node_modules", "package", "tool"), "executable", { mode: 0o755 })
  await Fs.symlink("../package/tool", Path.join(artifact, "node_modules", ".bin", "tool"))
  await Fs.symlink("package", Path.join(artifact, "node_modules", "workspace"))
  await Fs.symlink("/private/host-only-input", Path.join(artifact, "node_modules", "absolute"))
  const home = Path.join(stage, "store")
  const entries = await store.collect(home, artifact, () => true)
  const outputs = await Promise.all(entries.map(async (entry) => ({
    path: entry.path, mode: entry.mode,
    bytes: (await store.read(home, entry.digest)).toString("utf8"), digest: entry.digest,
  })))
  await Fs.writeFile(new URL("./artifact.json", import.meta.url), JSON.stringify(outputs, null, 2) + "\n")
} finally {
  await Fs.rm(stage, { recursive: true, force: true })
}
