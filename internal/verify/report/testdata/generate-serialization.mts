// Run: bun generate-serialization.mts /absolute/path/to/tools/verify/src/Report.mts
import { createHash } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
if (!process.argv[2]) throw new Error('Pass the TypeScript Report.mts reference path')
const Report = await import(pathToFileURL(process.argv[2]).href)
const minimal = { unitId: 'u', diagnostics: [{ errorKey: 'k', severity: 'error', message: 'm' }] }
const cases = [
  { name: 'optional-fields-absent', input: JSON.stringify(minimal) },
  { name: 'empty-members', input: JSON.stringify({ ...minimal, members: [] }) },
  { name: 'empty-advice', input: JSON.stringify({ ...minimal, adviceById: {} }) },
  { name: 'both-empty', input: JSON.stringify({ ...minimal, members: [], adviceById: {} }) },
  { name: 'normalized-field-order', input: JSON.stringify({
    adviceById: { z: 'last', a: 'first' }, members: [{ status: 'pass', discarded: 1, name: 'member' }],
    diagnostics: [{ range: { end: { column: 9, line: 3, extra: true }, extra: 1, start: { column: 2, line: 1 } },
      file: 'src/a.ts', code: 'CODE', message: 'm', severity: 'warning', errorKey: 'k', extra: true }],
    unitId: 'u', ignored: { safe: 'unused' },
  }) },
  { name: 'advice-authored-order', input: '{"unitId":"u","diagnostics":[],"adviceById":{"z":{"beta":2,"alpha":1},"a":[{"y":true,"x":null}]}}' },
  { name: 'advice-reversed-order', input: '{"unitId":"u","diagnostics":[],"adviceById":{"a":[{"y":true,"x":null}],"z":{"beta":2,"alpha":1}}}' },
  { name: 'advice-index-and-duplicate-keys', input: '{"unitId":"u","diagnostics":[],"adviceById":{"z":0,"10":"ten","2":"two","01":"one","z":3,"4294967295":"not-index","a":{"b":1,"a":2,"b":4}}}' },
  { name: 'unicode-and-escaping', input: JSON.stringify({
    unitId: 'u', diagnostics: [{ errorKey: '秘密😀<&>\u2028\u2029', code: '\\u2028', severity: 'message', message: '\u0000\b\f\n\r\t"\\/秘密😀\u2028\u2029' }],
    members: [{ name: '秘密😀', status: 'skip' }], adviceById: { '雪\u2028': '<&>"\\\u2029' },
  }) },
  { name: 'authored-escapes-and-surrogates', input: '{"unitId":"u","diagnostics":[{"errorKey":"\\u006b","severity":"error","message":"\\ud800 lone \\udfff pair \\ud83d\\ude00 slash \\/"}],"adviceById":{"\\u007a":"\\u2028\\u2029","\\ud800":"\\udfff"}}' },
  { name: 'numbers-normalized', input: '{"unitId":"u","diagnostics":[],"adviceById":{"numbers":[-0,1e2,1.2300,1e-6,1e-7,1e20,1e21,-1e-9,9007199254740993,1000000000000000100,5e-324,1.7976931348623157e308]}}' },
  { name: 'empty-report', input: '{"unitId":"u","diagnostics":[]}' },
]
const visibility = { unitId: 'u', tree: '5f0c6e1a9b2d4c3e8f7a6b5c4d3e2f1a0b9c8d7e', files: new Set(['src/a.ts']) }
const golden = cases.map(c => {
  const decoded = Report.decode(Buffer.from(c.input), visibility)
  if ('refused' in decoded) throw new Error(c.name + ': ' + decoded.refused)
  const canonical = JSON.stringify(decoded.report)
  return { ...c, canonical, digest: createHash('sha256').update(canonical).digest('hex') }
})
writeFileSync(join(dirname(fileURLToPath(import.meta.url)), 'serialization.json'), JSON.stringify(golden, null, 2) + '\n')
