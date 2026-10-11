import * as Fs from "node:fs/promises"
import * as Os from "node:os"
import * as Path from "node:path"

// Run: bun generate.mts /absolute/path/to/tools/verify/src/Tree.mts
const Tree = await import(Path.resolve(process.argv[2]!))
const root = await Fs.mkdtemp(Path.join(Os.tmpdir(), "tree-golden-"))
const home = await Fs.mkdtemp(Path.join(Os.tmpdir(), "tree-home-"))
async function git(...args: string[]) {
 const child = Bun.spawn(["git", ...args], {cwd: root, stdout: "pipe", stderr: "pipe"})
 const [out, err, status] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
 if (status) throw new Error(err)
 return out.trim()
}
try {
 await git("init", "--object-format=sha1")
 await Fs.mkdir(Path.join(root, "nested"))
 await Fs.writeFile(Path.join(root, "plain.txt"), "base\n")
 await Fs.writeFile(Path.join(root, "nested", "binary"), new Uint8Array([0, 255, 10, 13, 0]))
 await Fs.writeFile(Path.join(root, "run"), "#!/bin/sh\necho hi\n", {mode: 0o755})
 await Fs.symlink("plain.txt", Path.join(root, "link"))
 await Fs.writeFile(Path.join(root, "tab\tline\nfile"), "odd path\n")
 await Fs.writeFile(Path.join(root, "deleted"), "gone\n")
 await git("add", ".")
 await git("update-index", "--assume-unchanged", "plain.txt")
 await Fs.writeFile(Path.join(root, "plain.txt"), "edited\n")
 await Fs.rm(Path.join(root, "deleted"))
 await Fs.writeFile(Path.join(root, "untracked"), "excluded\n")
 const tree = await Tree.compute(root, home)
 const golden = {hash:tree.hash, entries:[...tree.entries.values()]}
 await Fs.writeFile(Path.join(import.meta.dir, "golden.json"), JSON.stringify(golden,null,2)+"\n")
} finally {
 await Fs.rm(root, {recursive:true,force:true})
 await Fs.rm(home, {recursive:true,force:true})
}
