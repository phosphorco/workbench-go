// /upstream/workspace-sync holds the generated manifests; /upstream/bun-install a
// self-contained node_modules. Both roles work in a writable staging copy of /work
// with those linked in.
//
// Select (VERIFY_ROLE=select, /work is the whole Tree): one Unit per tracked
// `*.test.ts`, whose inputs are the file and every Tree file it reaches through the
// pinned Bun's own import scanner and resolver. Anything resolved under node_modules
// comes from the upstream artifact and is not an input. Computed imports and runtime
// filesystem reads are not followed; the sandbox holds only the selected closure, so
// such a miss fails to load and makes the Unit unknown, never a stale pass.
//
// Run (/work is one Unit's closure): runs the test file named by unitId and writes
// /out/report.json: one member per test, one TEST_FAIL diagnostic per failing test.
import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  symlinkSync,
  writeFileSync,
} from "node:fs"
import { dirname, join, relative } from "node:path"

const walk = (root: string, dir = root): string[] =>
  readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry)
    return lstatSync(path).isDirectory() ? walk(root, path) : [relative(root, path)]
  })

const link = (target: string, path: string) => {
  mkdirSync(dirname(path), { recursive: true })
  symlinkSync(target, path)
}

const staged = (): string => {
  const stage = mkdtempSync("/tmp/stage-")
  cpSync("/work", stage, { recursive: true })
  cpSync("/upstream/workspace-sync", stage, { recursive: true })
  const modules = join(stage, "node_modules")
  mkdirSync(modules)
  const installed = "/upstream/bun-install/node_modules"
  for (const entry of existsSync(installed) ? readdirSync(installed) : []) {
    if (entry.startsWith("@"))
      for (const scoped of readdirSync(join(installed, entry)))
        link(join(installed, entry, scoped), join(modules, entry, scoped))
    else link(join(installed, entry), join(modules, entry))
  }
  for (const manifest of walk(stage).filter(
    (path) => path.endsWith("/package.json") && !path.startsWith("node_modules/"),
  )) {
    const { name } = JSON.parse(readFileSync(join(stage, manifest), "utf8")) as { name?: string }
    if (name && !existsSync(join(modules, name))) link(join(stage, dirname(manifest)), join(modules, name))
  }
  return stage
}

const scannable = /\.(?:[cm]?[jt]sx?)$/
const loader = (path: string) => (/\.[cm]?tsx?$/.test(path) ? (path.endsWith("x") ? "tsx" : "ts") : "js")

const closure = (stage: string, tracked: ReadonlySet<string>, file: string): string[] => {
  const seen = new Set<string>()
  const visit = (path: string) => {
    if (seen.has(path)) return
    seen.add(path)
    if (!scannable.test(path)) return
    const absolute = join(stage, path)
    const imports = new Bun.Transpiler({ loader: loader(path) }).scanImports(readFileSync(absolute, "utf8"))
    for (const { path: specifier } of imports) {
      let resolved: string
      try {
        resolved = Bun.resolveSync(specifier, dirname(absolute))
      } catch {
        continue
      }
      const inTree = relative(stage, resolved)
      if (!inTree.startsWith("..") && !inTree.split("/").includes("node_modules") && tracked.has(inTree)) visit(inTree)
    }
  }
  visit(file)
  return [...seen].sort()
}

if (process.env.VERIFY_ROLE === "select") {
  const tracked = new Set(walk("/work"))
  const stage = staged()
  const units = [...tracked]
    .filter((path) => path.endsWith(".test.ts") && !path.split("/").includes("node_modules"))
    .sort()
    .map((file) => ({ unitId: file, inputs: closure(stage, tracked, file) }))
  process.stdout.write(`${JSON.stringify({ units })}\n`)
  process.exit(0)
}

const { unitId: file } = JSON.parse(await Bun.stdin.text()) as { readonly unitId: string }
const stage = staged()

const junit = join(mkdtempSync("/tmp/junit-"), "report.xml")
const run = Bun.spawnSync(["bun", "test", `./${file}`, "--reporter=junit", `--reporter-outfile=${junit}`], {
  cwd: stage,
  env: { PATH: process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin", HOME: "/tmp" },
  stdout: "pipe",
  stderr: "pipe",
})

const unescape = (s: string) =>
  s
    .replace(/&#10;/g, "\n")
    .replace(/&quot;/g, "\"")
    .replace(/&apos;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&")
const attribute = (tag: string, name: string) => {
  const match = new RegExp(`\\s${name}="([^"]*)"`).exec(tag)
  return match ? unescape(match[1]!) : undefined
}

const xml = existsSync(junit) ? readFileSync(junit, "utf8") : ""
const cases = [...xml.matchAll(/<testcase\b([^>]*?)(?:\/>|>([\s\S]*?)<\/testcase>)/g)]
const scrub = (text: string) => text.replaceAll(`${stage}/`, "").replaceAll(stage, ".")
if (cases.length === 0) {
  const output = scrub(run.stderr.toString())
  const cause = output.split("\n").find((line) => /^\w*error:/i.test(line.trim())) ?? "no test results"
  process.stderr.write(`${output}\n${file} did not load: ${cause.trim()}\n`)
  process.exit(1)
}
const members: Array<{ name: string; status: "pass" | "fail" | "skip" }> = []
const diagnostics: Array<Record<string, unknown>> = []
for (const [, attributes = "", body = ""] of cases) {
  const tag = ` ${attributes}`
  const scope = attribute(tag, "classname")
  const name = scope ? `${scope} > ${attribute(tag, "name")}` : attribute(tag, "name")!
  const status = body.includes("<failure") ? "fail" : body.includes("<skipped") ? "skip" : "pass"
  members.push({ name, status })
  if (status !== "fail") continue
  const failure = /<failure\b([^>]*)>/.exec(body)?.[1] ?? ""
  const line = Number(attribute(tag, "line"))
  diagnostics.push({
    errorKey: name,
    code: "TEST_FAIL",
    severity: "error",
    message: scrub(attribute(` ${failure}`, "message") ?? "test failed").trim(),
    file,
    ...(Number.isInteger(line) && line > 0 ? { range: { start: { line, column: 1 } } } : {}),
  })
}

writeFileSync(
  "/out/report.json",
  `${JSON.stringify({ unitId: file, diagnostics, members, adviceById: {} }, null, 2)}\n`,
)
