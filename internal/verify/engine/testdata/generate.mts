import * as Fs from "node:fs/promises"
import * as Path from "node:path"
import * as Url from "node:url"
const reference = Path.resolve(process.argv[2]!)
const Engine = await import(Url.pathToFileURL(Path.join(reference, "Engine.mts")).href)
const Manifest = await import(Url.pathToFileURL(Path.join(reference, "Manifest.mts")).href)
const Store = await import(Url.pathToFileURL(Path.join(reference, "Store.mts")).href)
const image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
const script = `printf '{"unitId":"%s","diagnostics":[],"members":[],"adviceById":{}}' "$VERIFY_UNIT" > /out/report.json`
const files = {"src/a": "content <&>\n", "verifiers/check/run.sh": script}
const manifest = { id: "check", image, program: ["verifiers/check/run.sh"], command: ["sh", "/verifier/run.sh"], selector: {kind: "Paths", patterns: ["src/**"]}, displayNoun: "unit", produces: "report", outputs: [], members: false, requires: {}, toolchain: {kind: "strong"}, deterministic: true, network: "none", timeout: 30 }
const registered = Manifest.register(manifest)
const input = [{path: "src/a", digest: Store.sha256(files["src/a"]), mode: "100644"}]
const program = [{path: "run.sh", digest: Store.sha256(script), mode: "100644"}]
const rawReport = JSON.stringify({unitId:"check",diagnostics:[],members:[],adviceById:{}})
await Fs.writeFile(Path.join(import.meta.dir,"action.json"), JSON.stringify({manifest, files, actionKey: Engine.actionKey(registered,"run","check",input,[],program), reportDigest: Store.sha256(rawReport)},null,2)+"\n")
