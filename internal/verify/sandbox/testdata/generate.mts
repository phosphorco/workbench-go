// Run: bun generate.mts /absolute/path/to/monorepo/tools/verify/src/Sandbox.mts
import * as Fs from "node:fs/promises"
import * as Os from "node:os"
import * as Path from "node:path"
import { pathToFileURL } from "node:url"

const reference = await import(pathToFileURL(process.argv[2]).href)
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
} finally {
  await Fs.rm(stage, { recursive: true, force: true })
}
