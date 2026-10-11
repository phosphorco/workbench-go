import * as Fs from "node:fs/promises"
import * as Path from "node:path"

const reference = Path.resolve(process.argv[2] ?? "tools/verify")
const Manifest = await import(Path.join(reference, "src/Manifest.mts"))
const destination = Path.join(import.meta.dir, "demo")
await Fs.mkdir(destination, { recursive: true })
const paths = ["workbench.pkl", ...["bun-install", "bun-test", "workspace-sync"].map(id => `verifiers/${id}/VERIFIER.pkl`)]
for (const file of paths) {
  await Fs.mkdir(Path.dirname(Path.join(destination, file)), { recursive: true })
  await Fs.copyFile(Path.join(reference, "demo/repo", file), Path.join(destination, file))
}
const child = Bun.spawn(["pkl", "eval", "-f", "json", "--module-path", Path.join(reference, "schema"), Path.join(destination, "workbench.pkl")], { stdout: "pipe", stderr: "pipe" })
const output = await new Response(child.stdout).text()
if (await child.exited !== 0) throw new Error(await new Response(child.stderr).text())
const repository = JSON.parse(output)
const registered = Manifest.order(repository)
await Fs.writeFile(Path.join(import.meta.dir, "versions.json"), JSON.stringify(registered.map(({manifest, imageDigest, manifestDigest, version}) => ({id:manifest.id, imageDigest, manifestDigest, version})), null, 2) + "\n")
// Exercise JSON.stringify details that compacting Pkl JSON or sorting maps would lose.
const unusual = structuredClone(repository.verifiers["bun-test"])
unusual.command = ["<&>", "\u2028\u2029", "quote\"", "slash\\", "literal\\u2028\\u2029"]
unusual.requires = {"z": "workspace-sync", "a": "bun-install"}
unusual.timeout = 1e-7
const r = Manifest.register(unusual)
await Fs.writeFile(Path.join(import.meta.dir, "unusual.json"), JSON.stringify({manifest:unusual, manifestDigest:r.manifestDigest, version:r.version}, null, 2) + "\n")
