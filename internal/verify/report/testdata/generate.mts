// Run: bun generate.mts /absolute/path/to/tools/verify/src/Report.mts
import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const reference = process.argv[2]
if (!reference) throw new Error('Pass the TypeScript Report.mts reference path')
const Report = await import(pathToFileURL(reference).href)
const diagnostic = (line: number, errorKey = 'FAIL<&>\u2028\u2029\\u2028') => ({
  errorKey, code: 'CODE', severity: 'error', message: 'failure', file: 'src/秘密.ts',
  range: { start: { line, column: 2 } },
})
const unit = (verifier: string, unitId: string, diagnostics: unknown[]) => ({
  verifier, unitId, report: { unitId, diagnostics, members: [{ name: 'member', status: 'fail' }] },
})
const base = 'header\nbad\nbad\nlast\r\n'
const cases = [
  { name: 'before', files: { 'src/秘密.ts': [...Buffer.from(base)] }, units: [unit('v', 'b', [diagnostic(3)]), unit('v', 'a', [diagnostic(2), diagnostic(4, 'CRLF')])] },
  { name: 'edited-above', files: { 'src/秘密.ts': [...Buffer.from('inserted\n' + base)] }, units: [unit('v', 'b', [diagnostic(4)]), unit('v', 'a', [diagnostic(3), diagnostic(5, 'CRLF')])] },
  { name: 'changed-line', files: { 'src/秘密.ts': [...Buffer.from('header\nchanged\nbad\nlast\r\n')] }, units: [unit('v', 'b', [diagnostic(3)]), unit('v', 'a', [diagnostic(2), diagnostic(4, 'CRLF')])] },
  { name: 'missing-line-and-file', files: { 'src/秘密.ts': [...Buffer.from('x\n')] }, units: [unit('v', 'a', [diagnostic(20), diagnostic(2), { errorKey: 'unit', severity: 'message', message: 'no file' }, { errorKey: 'no range', severity: 'warning', message: 'no range', file: 'missing' }])] },
  { name: 'binary-lines-and-utf16-order', files: { 'src/秘密.ts': [255, 0, 13, 10, 254] }, units: [unit('\uE000', 'z', [diagnostic(1)]), unit('😀', 'z', [diagnostic(1), diagnostic(2)])] },
]
const golden = cases.map(c => ({ ...c,
  occurrences: Report.occurrences(c.units, new Map(Object.entries(c.files).map(([path, bytes]) => [path, Buffer.from(bytes)]))),
  members: Report.members(c.units),
}))
writeFileSync(join(dirname(fileURLToPath(import.meta.url)), 'keys.json'), JSON.stringify(golden, null, 2) + '\n')
