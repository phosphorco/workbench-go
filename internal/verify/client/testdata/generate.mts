// bun internal/verify/client/testdata/generate.mts
// delta.json retains released 0.9.0 attention fixtures; grouped output snapshots
// are recorded through go test ./internal/verify/client -run Grouped -update-grouped.
import { join } from "node:path"
const patterns = ['src/**','**/*.txt','*.txt','src/*','src/{a,b}.txt','src/[ab].txt','src/[!c].txt','!src/a.txt','src/?.txt','src/**/a.txt','**','a/**/b','**/.hidden','src/\\*.txt','!!src/a.txt','!!!src/a.txt','[','{a','a/**b','@(a|b)','!(a|b)']
const paths = ['src/a.txt','src/b.txt','src/c.txt','src/nested/a.txt','a.txt','src/.hidden','a/b','a/x/b','src/*.txt','src/🦊.txt']
const globs = patterns.flatMap(pattern => paths.map(file => ({pattern,file,match:new Bun.Glob(pattern).match(file)})))
await Bun.write(join(import.meta.dir,'globs.json'),JSON.stringify(globs,null,2)+'\n')
