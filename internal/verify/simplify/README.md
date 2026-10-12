# Diagnostic simplification

`Simplify` groups plain entries by `(verifier, code, errorKey)` and applies the
quote-scaffold algorithm from `@phosphor/diagnostic-simplifier`. Single, double,
and backtick delimiters retain escaped bytes. Varying slots become `<arg1>`,
`<arg2>`; constant slots stay literal. A template is used only when its UTF-16
length is strictly smaller than the sum of distinct exact-message headers.
Original messages, diagnostic values, annotations, and placeholder values
remain on each occurrence.

Families, scaffolds, roots, files, and occurrences follow the reference's
insertion order. Parseable scaffolds precede messages without balanced quoted
values within one family, as in the reference. Paths normalize slashes and
match the longest supplied root.

The bun generator runs the actual TypeScript simplifier over all seven canonical
cases, extra quote/path/identity cases, and the released demo's Reports frozen
in `testdata/demo-reports.json`. Its adapter supplies the verify identity tuple
to the reference's single-key interface; the algorithm itself is unchanged.
Regenerate and compare structure byte for byte:

```sh
mise exec -- bun internal/verify/simplify/testdata/generate.mts /path/to/monorepo
mise exec -- go test ./internal/verify/simplify -run SimplifierParity
```
