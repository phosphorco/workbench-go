// bun generate.mts /path/to/monorepo
import { join } from 'node:path'
const monorepo = process.argv[2]
if (!monorepo) throw new Error('provide the monorepo path')
const { simplifyDiagnostics } = await import(join(monorepo, 'pkg/@phosphor/diagnostic-simplifier/src/internal/diagnostic-simplifier.ts'))
const fixtures: any[] = []
const entry = (message: string, extra: any = {}) => ({ diagnostic: { message }, errorMessage: message, errorKey: 'rule', ...extra })
const add = (name: string, diagnostics: any[], rootFolders: any[] = []) => {
 const input = { ...(rootFolders.length ? {rootFolders} : {}), diagnostics }
 // The host-neutral reference has one key. Adapt the verify identity tuple to it.
 const keys = new Map<string,string>()
 const entries = diagnostics.map(e => {
  const key = e.errorKey ?? e.errorCode ?? e.errorMessage
  const identity = JSON.stringify([e.verifier ?? '', e.errorCode, key])
  keys.set(identity, key)
  return {...e, errorKey: identity}
 })
 const result = simplifyDiagnostics({ rootFolders, diagnostics: entries }, {
  getErrorCode: (d: any) => diagnostics.find(e => e.diagnostic === d)?.errorCode,
  formatFilePosition: (d: any) => diagnostics.find(e => e.diagnostic === d)?.formattedPosition,
 })
 const expected = { groups: result.groups.map((g: any) => ({...g, errorKey: keys.get(g.errorKey)})) }
 fixtures.push({name,input,expected:JSON.stringify(expected)})
}
add('exact repeated', [entry('Repeated exact message',{filePath:'/repo/src/example.ts',errorCode:'rule',formattedPosition:'1:1'}),entry('Repeated exact message',{filePath:'/repo/src/example.ts',errorCode:'rule',formattedPosition:'2:1'})])
add('annotations',[entry('Repeated exact message',{annotations:['fixed-in-patch']}),entry('Repeated exact message')])
add('imports',[entry('Import "../a.ts" is not allowed.',{errorKey:'import-rule',filePath:'/repo/src/a.ts'}),entry('Import "../b.ts" is not allowed.',{errorKey:'import-rule',filePath:'/repo/src/b.ts'})],[{path:'/repo',label:'repo'}])
add('distinct repeated slots',[
 entry('Property \'tag\' does not exist on type \'ExecutionEvent\'.\nProperty \'tag\' does not exist on type \'{ readonly type: "execution.started"; }\'.',{errorKey:'typescript:TS2339'}),
 entry('Property \'profile\' does not exist on type \'ExecutionEvent\'.\nProperty \'session\' does not exist on type \'{ readonly type: "execution.started"; }\'.',{errorKey:'typescript:TS2339'}),
])
add('constant slots',[entry('Symbol "UserRepo" must not import "../a.ts".'),entry('Symbol "UserRepo" must not import "../b.ts".')])
add('short fallback',[entry('"a"'),entry('"b"')])
add('different error keys',[entry('Import "../a.ts" is not allowed.',{errorKey:'a'}),entry('Import "../b.ts" is not allowed.',{errorKey:'b'})])
add('same code and message distinct error identities', [entry('Shared diagnostic message',{errorKey:'family A',errorCode:'CODE',verifier:'lint'}),entry('Shared diagnostic message',{errorKey:'family B',errorCode:'CODE',verifier:'lint'})])
add('error identity equals message', [entry('Repeated exact message',{errorKey:'Repeated exact message',errorCode:'CODE'})])
add('different verifiers and codes', [entry('Import "../a.ts" is not allowed.',{verifier:'a',errorCode:'A'}),entry('Import "../b.ts" is not allowed.',{verifier:'b',errorCode:'A'}),entry('Import "../c.ts" is not allowed.',{verifier:'a',errorCode:'B'})])
add('escaped and backtick values',[entry('Cannot use `a\\`b` in "fixed".'),entry('Cannot use `c\\`d` in "fixed".')])
add('mixed delimiters',[entry('Import "alpha" is not allowed.'),entry("Import 'beta' is not allowed.")])
add('malformed quotes and scaffolds',[entry('Missing "end'),entry('Ordinary message'),entry('Import "a" is not allowed.'),entry('Import "b" is not allowed.'),entry('Cannot use "c".'),entry('Cannot use "d".')])
add('utf16 cost',[entry('"🦊"'),entry('"🐻"')])
add('fallback identities',[{diagnostic:1,errorMessage:'same',errorCode:'CODE'}, {diagnostic:2,errorMessage:'same',errorCode:'CODE'}, {diagnostic:3,errorMessage:'other'}])
add('paths and longest roots',[entry('Repeated',{filePath:'C:\\repo\\nested\\a.ts'}),entry('Repeated',{filePath:'C:/repo//b.ts/'}),entry('Repeated',{filePath:'/outside/x'}),entry('Repeated',{filePath:'C:/repo/nested/a.ts'})],[{path:'C:/repo',label:'repo'},{path:'C:/repo/nested',label:'nested'}])
add('empty',[])
// Frozen Reports from the released 0.9.0 demo golden.
const demo = await Bun.file(join(import.meta.dir,'demo-reports.json')).json()
for (const step of demo) {
 const occurrences = step.occurrences
 add('demo: '+step.name, occurrences.map((o: any) => ({diagnostic:o,errorMessage:o.message,errorKey:o.errorKey,errorCode:o.code,verifier:o.verifier,filePath:o.file,formattedPosition:o.range ? `${o.range.start.line}:${o.range.start.column}` : undefined})))
}
await Bun.write(join(import.meta.dir,'parity.json'),JSON.stringify(fixtures,null,2)+'\n')
