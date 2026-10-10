// Usage: bun generate.mts /path/to/monorepo/tools/verify/src
import * as Path from "node:path"
const source = process.argv[2]
if (!source) throw new Error("pass the TypeScript reference src directory")
const Engine = await import(Path.join(source, "Engine.mts"))
const Store = await import(Path.join(source, "Store.mts"))
const entry = (path: string, text: string, mode = "100644") => ({path, digest: Store.sha256(text), mode})
const image = "sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895"
const base = {version: Store.sha256("manifest version"), role:"run", unitId:"unit", inputs:[], upstream:[], program:[entry("run.ts","program")], command:["bun","/verifier/run.ts"], imageDigest:image}
const cases = [
 {name:"workspace-sync", action:{...base,verifier:"workspace-sync",unitId:"workspace-sync",imageDigest:"sha256:d4dfb48b4cdb0a7114036a735b35160e5fb192fed8d845d5cffcb0f706d881e0",inputs:[entry("workspace.pkl","workspace")]}},
 {name:"bun-install", action:{...base,verifier:"bun-install",unitId:"bun-install",inputs:[entry("bun.lock","lock")],upstream:[{name:"workspace-sync",digest:Store.sha256("workspace artifact")}]}},
 {name:"bun-test", action:{...base,verifier:"bun-test",unitId:"packages/a/test.ts",inputs:[entry("packages/a/test.ts","test"),entry("packages/a/a.ts","a")],upstream:[{name:"workspace-sync",digest:Store.sha256("workspace artifact")},{name:"bun-install",digest:Store.sha256("dependencies")}]}},
 {name:"select", action:{...base,verifier:"bun-test",role:"select",unitId:"*",inputs:[entry("a.ts","a")]}},
 {name:"empty",action:{...base,verifier:"empty",program:[],command:[]}},
 {name:"unicode-modes",action:{...base,verifier:"edges",unitId:"雪",inputs:[entry("😀.ts","emoji"),entry("\ue000.ts","bmp"),entry("café/<&>.ts","line\u2028next"),entry("bin/tool","#!/bin/sh", "100755"),entry("link","../target","120000")]}},
 {name:"tuple-sort",action:{...base,verifier:"edges",inputs:[entry("a","z"),entry("a!","a"),entry("a,","comma"),entry("a","a")],program:[entry("z.ts","z"),entry("a.ts","a")],upstream:[{name:"a",digest:"z"},{name:"a!",digest:"a"}]}},
 {name:"json-strings",action:{...base,verifier:"<&>\u2028\u2029",command:["\"\\\n\t","<&>","\u2028\u2029",String.raw`\u2028\u2029`,String.raw`\\u2028`]}}
]
const goldens=cases.map(({name,action})=>({name,action,key:Engine.actionKey({manifest:{id:action.verifier,command:action.command},version:action.version,imageDigest:action.imageDigest},action.role,action.unitId,action.inputs,action.upstream,action.program),directory:Store.digestOf({directory:[...action.inputs].sort((a,b)=>a.path<b.path?-1:a.path>b.path?1:0).map(e=>[e.path,e.digest,e.mode])})}))
await Bun.write(Path.join(import.meta.dir,"keys.json"),JSON.stringify(goldens,null,2)+"\n")
