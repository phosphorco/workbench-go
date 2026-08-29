package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"

	"github.com/phosphorco/workbench-go/internal/workspace"
)

const bunScanImportsProgram = `
const files = await Bun.stdin.json();
const results = files.map((file) => {
  try {
    return { imports: new Bun.Transpiler({ loader: file.loader }).scanImports(file.source) };
  } catch (error) {
    return { error: error instanceof Error ? error.message : String(error) };
  }
});
process.stdout.write(JSON.stringify(results));
`

type typeScriptSourceFile struct {
	source      []byte
	path        string
	loader      string
	development bool
}

type bunScanInput struct {
	Source string `json:"source"`
	Loader string `json:"loader"`
}

type bunScannedImport struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type bunScanResult struct {
	Imports []bunScannedImport `json:"imports"`
	Error   string             `json:"error"`
}

// ImportProvenanceError refuses a parser-confirmed import when the lexical
// provenance pass found more same-kind, same-path candidates than Bun reported.
// Without parser spans, choosing any one candidate would risk a false source
// location in a closure diagnostic.
type ImportProvenanceError struct {
	Source         string
	Kind           string
	Path           string
	ParserCount    int
	CandidateLines []int
}

func (failure *ImportProvenanceError) Error() string {
	return fmt.Sprintf("attach source provenance to Bun imports in %s: %s %q occurs %d time(s) in parser truth but has lexical candidates on lines %v; remove the ambiguous import-like text", failure.Source, failure.Kind, failure.Path, failure.ParserCount, failure.CandidateLines)
}

func parserBackedTypeScriptImports(ctx context.Context, bun string, files []typeScriptSourceFile) ([]workspace.Import, error) {
	inputs := make([]bunScanInput, 0, len(files))
	for _, file := range files {
		inputs = append(inputs, bunScanInput{Source: string(file.source), Loader: file.loader})
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("encode TypeScript import scan: %w", err)
	}
	command := exec.CommandContext(ctx, bun, "-e", bunScanImportsProgram)
	command.Stdin = bytes.NewReader(encoded)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("scan TypeScript imports with Bun: %w: %s", err, stderr.String())
	}
	var results []bunScanResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("decode Bun TypeScript import scan: %w: %s", err, stdout.String())
	}
	if len(results) != len(files) {
		return nil, fmt.Errorf("Bun TypeScript import scan returned %d result(s) for %d file(s)", len(results), len(files))
	}

	observed := make([]workspace.Import, 0)
	for index, file := range files {
		if results[index].Error != "" {
			return nil, fmt.Errorf("parse TypeScript imports in %s with Bun: %s", file.path, results[index].Error)
		}
		available := make(map[string]int)
		for _, imported := range results[index].Imports {
			available[importKey(imported.Kind, imported.Path)]++
		}
		candidates := observeTypeScriptImports(file.source)
		candidateLines := make(map[string][]int)
		candidateByKey := make(map[string]observedTypeScriptImport)
		for _, candidate := range candidates {
			key := importKey(candidate.kind, candidate.specifier)
			candidateLines[key] = append(candidateLines[key], candidate.line)
			candidateByKey[key] = candidate
		}
		for key, lines := range candidateLines {
			if available[key] > 0 && len(lines) > available[key] {
				candidate := candidateByKey[key]
				return nil, &ImportProvenanceError{
					Source: file.path, Kind: candidate.kind, Path: candidate.specifier,
					ParserCount: available[key], CandidateLines: append([]int(nil), lines...),
				}
			}
		}
		for _, candidate := range candidates {
			key := importKey(candidate.kind, candidate.specifier)
			if available[key] == 0 {
				continue
			}
			available[key]--
			observed = append(observed, workspace.Import{
				Specifier: candidate.specifier, Source: file.path, Line: candidate.line, Development: file.development,
			})
		}
		unmatched := make([]string, 0)
		for _, imported := range results[index].Imports {
			if imported.Kind == "require-call" || available[importKey(imported.Kind, imported.Path)] == 0 {
				continue
			}
			key := importKey(imported.Kind, imported.Path)
			unmatched = append(unmatched, key)
			available[key]--
		}
		if len(unmatched) != 0 {
			sort.Strings(unmatched)
			return nil, fmt.Errorf("attach source provenance to Bun imports in %s: unmatched parser import(s) %v", file.path, unmatched)
		}
	}
	return observed, nil
}

func importKey(kind, path string) string {
	return kind + "\x00" + path
}
