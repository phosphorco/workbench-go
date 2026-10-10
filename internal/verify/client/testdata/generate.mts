// Run: bun internal/verify/client/testdata/generate.mts /path/to/monorepo/tools/verify/src
import { join } from 'node:path'
const source = process.argv[2]
if (!source) throw new Error('provide the TypeScript reference src directory')
const D = await import(join(source, 'Delta.mts'))
const R = await import(join(source, 'Render.mts'))
const Report = await import(join(source, 'Report.mts'))
const emptyFocus = () => ({ files: [], codes: [], verifiers: [], units: [], members: [], severities: [] })
const verifiers = [{ id: 'lint', version: 'v1', displayNoun: 'source file', produces: 'report' }, { id: 'test', version: 'v1', displayNoun: 'test file', produces: 'report' }]
const diagnostic = (errorKey, file, line, column = 1, extra = {}) => ({ errorKey, code: 'TODO', severity: 'error', message: 'TODO left in source', file, range: { start: { line, column } }, ...extra })
const unit = (verifier, unitId, diagnostics, members = []) => ({ verifier, unitId, report: { unitId, diagnostics, members } })
const files = new Map([['src/a.txt', Buffer.from('a\nTODO one\n')], ['src/b.txt', Buffer.from('TODO two\n')], ['src/c.txt', Buffer.from('TODO three\n')]])
const a = unit('lint', 'a', [diagnostic('todo', 'src/a.txt', 2)])
const b = unit('lint', 'b', [diagnostic('todo', 'src/b.txt', 1)])
const c = unit('lint', 'c', [diagnostic('todo', 'src/c.txt', 1)])
let last = D.empty
const fixtures = []
function step(name, units, focus = emptyFocus(), unknown = [], gatePolicy = undefined, all = false) {
 const observation = { tree: name, verifiers, results: [], unknown }
 const occurrences = Report.occurrences(units, files)
 const members = Report.members(units)
 const gate = gatePolicy ? D.gateStatus('commit', gatePolicy, observation, occurrences) : undefined
 const lastSeen = all ? D.empty : last
 const { delta, next } = D.fold({ observation, occurrences, members, lastSeen, focus, ...(gate ? {gate} : {}) })
 const exit = D.exitCode({ observation, occurrences, focus, gate, blocking: gatePolicy?.blocking ?? ['error'] })
 fixtures.push({ name, input: { observation, occurrences, members, lastSeen, focus, ...(gate ? {gate} : {}) }, all, blocking: gatePolicy?.blocking ?? ['error'], exit, text: R.render(delta, {all, exit}), deltaJSON: JSON.stringify(delta), nextJSON: JSON.stringify(next,null,2), json: JSON.stringify({ observation: {...observation, occurrences, members}, delta, focus }, null, 2) + '\n' })
 if (!all) last = next
}
step('first', [a,b], emptyFocus(), [], {requires:['lint'],blocking:['error']})
step('unchanged', [a,b])
files.set('src/a.txt', Buffer.from('inserted\na\nTODO one\n'))
const movedA = unit('lint','a',[diagnostic('todo','src/a.txt',3)])
step('moved', [movedA,b])
step('resolved-outside-focus', [movedA], {...emptyFocus(),files:['src/a.txt']})
step('reappearance', [movedA,b])
step('hidden-new', [movedA,b,c], {...emptyFocus(),files:['src/a.txt']})
step('forced-new', [movedA,b,c], {...emptyFocus(),files:['src/c.txt']})
step('code-resolution', [movedA,c], {...emptyFocus(),codes:['TODO']})
step('unknown-retains', [c], {...emptyFocus(),files:['src/a.txt']}, [{verifier:'lint',unitId:'a',display:'a',reason:'tool failed'}])
step('unknown-whole-verifier', [], {...emptyFocus(),units:['not-a']}, [{verifier:'lint',unitId:'*',display:'every source file',reason:'select failed'}])
step('resolved-all', [], emptyFocus(), [], {requires:['lint'],blocking:['error']})
step('all-read', [a,b], emptyFocus(), [], undefined, true)
step('gate-ignores-focus', [a], {...emptyFocus(),files:['elsewhere']}, [], {requires:['lint','missing'],blocking:['error']})
step('unknown-outside-focus', [], {...emptyFocus(),verifiers:['test']}, [{verifier:'lint',unitId:'a',display:'a',reason:'broken'}])
const rich = unit('test','suite',[
 diagnostic('test "one"','src/b.txt',1,8,{code:'TEST_FAIL',severity:'warning',message:'<bad> & broken\n\nsecond line\u2028end'}),
 diagnostic('test "one"','src/a.txt',3,2,{code:'TEST_FAIL',severity:'warning',message:'<bad> & broken\n\nsecond line\u2028end'}),
 {errorKey:'global',severity:'message',message:'global message'},
 {errorKey:'file',code:'',severity:'suggestion',message:'file check',file:'src/c.txt'},
], [{name:'test "one"',status:'fail'},{name:'pass',status:'pass'},{name:'skip',status:'skip'}])
step('rich-render', [rich])
step('utf16-file-order', [unit('lint','unicode',[diagnostic('same','src/\ue000.txt',1),diagnostic('same','src/🦊.txt',1),diagnostic('same','src/z.txt',1,4),diagnostic('same','src/z.txt',1,1)])])
step('member-focus', [rich], {...emptyFocus(),members:['test "one"','pass','skip']})
step('rich-resolved', [])
step('duplicate-keys', [unit('test','suite',[{errorKey:'global',severity:'message',message:'first'},{errorKey:'global',severity:'warning',message:'last'}])])
step('duplicate-focus', [a], {...emptyFocus(),codes:['TODO','TODO']})
step('duplicate-resolution', [])
await Bun.write(join(import.meta.dir,'delta.json'),JSON.stringify(fixtures,null,2)+'\n')
const patterns = ['src/**','**/*.txt','*.txt','src/*','src/{a,b}.txt','src/[ab].txt','src/[!c].txt','!src/a.txt','src/?.txt','src/**/a.txt','**','a/**/b','**/.hidden','src/\\*.txt','!!src/a.txt','!!!src/a.txt','[','{a','a/**b','@(a|b)','!(a|b)']
const paths = ['src/a.txt','src/b.txt','src/c.txt','src/nested/a.txt','a.txt','src/.hidden','a/b','a/x/b','src/*.txt','src/🦊.txt']
const globs = patterns.flatMap(pattern => paths.map(file => ({pattern,file,match:new Bun.Glob(pattern).match(file)})))
await Bun.write(join(import.meta.dir,'globs.json'),JSON.stringify(globs,null,2)+'\n')
const resultJSON = []
for (const provenance of ['recomputed', {carriedFrom:'previous-tree'}]) {
 const observation = {tree:'result',verifiers,results:[{verifier:'lint',unitId:'a',display:'a',actionKey:'action',attestation:{actionKey:'action',verifier:{id:'lint',version:'v1'},unitId:'a',exit:0,outputs:{'report.json':'report-digest'}},provenance,scratch:'absent',audit:'matched'}],unknown:[]}
 const {delta} = D.fold({observation,occurrences:[],members:[],lastSeen:D.empty,focus:emptyFocus()})
 resultJSON.push(JSON.stringify({observation:{...observation,occurrences:[],members:[]},delta,focus:emptyFocus()},null,2)+'\n')
}
await Bun.write(join(import.meta.dir,'results.json'),JSON.stringify(resultJSON,null,2)+'\n')
