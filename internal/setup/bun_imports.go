package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/workspace"
)

const bunScanImportsProgram = `
const files = await Bun.stdin.json();
const results = files.map((file) => {
  try {
    const transpiler = new Bun.Transpiler({ loader: file.loader });
    transpiler.scanImports(file.source);
    return { imports: transpiler.scanImports(file.probeSource) };
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
	Source      string `json:"source"`
	ProbeSource string `json:"probeSource"`
	Loader      string `json:"loader"`
}

type bunScannedImport struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type bunScanResult struct {
	Imports []bunScannedImport `json:"imports"`
	Error   string             `json:"error"`
}

type importProbe struct {
	candidate observedTypeScriptImport
	sentinel  string
}

type probedTypeScriptFile struct {
	file   typeScriptSourceFile
	probes []importProbe
}

func parserBackedTypeScriptImports(ctx context.Context, bun string, files []typeScriptSourceFile) ([]workspace.Import, error) {
	inputs := make([]bunScanInput, 0, len(files))
	probedFiles := make([]probedTypeScriptFile, 0, len(files))
	for fileIndex, file := range files {
		mutated, probes, err := probeTypeScriptImports(file, fileIndex)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, bunScanInput{Source: string(file.source), ProbeSource: string(mutated), Loader: file.loader})
		probedFiles = append(probedFiles, probedTypeScriptFile{file: file, probes: probes})
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
	for index, probed := range probedFiles {
		file := probed.file
		if results[index].Error != "" {
			return nil, fmt.Errorf("parse TypeScript imports in %s with Bun: %s", file.path, results[index].Error)
		}
		confirmed := make(map[string]string, len(probed.probes))
		unmatched := make([]string, 0)
		for _, imported := range results[index].Imports {
			probe, cooked, exists := matchingImportProbe(probed.probes, imported.Path)
			if exists {
				if imported.Kind != probe.candidate.kind {
					return nil, fmt.Errorf("attach source provenance to Bun imports in %s: sentinel %q has parser kind %q, want %q", file.path, imported.Path, imported.Kind, probe.candidate.kind)
				}
				confirmed[probe.sentinel] = cooked
				continue
			}
			if imported.Kind != "require-call" {
				unmatched = append(unmatched, importKey(imported.Kind, imported.Path))
			}
		}
		if len(unmatched) != 0 {
			sort.Strings(unmatched)
			return nil, fmt.Errorf("attach source provenance to Bun imports in %s: unmatched parser import(s) %v", file.path, unmatched)
		}
		for _, probe := range probed.probes {
			cooked, exists := confirmed[probe.sentinel]
			if !exists {
				continue
			}
			candidate := probe.candidate
			observed = append(observed, workspace.Import{
				Specifier: cooked, Source: file.path, Line: candidate.line, Development: file.development,
			})
		}
	}
	return observed, nil
}

func probeTypeScriptImports(file typeScriptSourceFile, fileIndex int) ([]byte, []importProbe, error) {
	candidates := observeTypeScriptImports(file.source)
	probes := make([]importProbe, 0, len(candidates))
	var mutated bytes.Buffer
	cursor := 0
	for candidateIndex, candidate := range candidates {
		if candidate.spanStart < cursor || candidate.spanEnd < candidate.spanStart || candidate.spanEnd > len(file.source) {
			return nil, nil, fmt.Errorf("attach source provenance to Bun imports in %s: overlapping or invalid lexical span [%d,%d)", file.path, candidate.spanStart, candidate.spanEnd)
		}
		sentinel := fmt.Sprintf("workbench-import-probe-f%06d-c%06d-", fileIndex, candidateIndex)
		candidate.line = 1 + bytes.Count(file.source[:candidate.spanStart], []byte{'\n'})
		mutated.Write(file.source[cursor:candidate.spanStart])
		mutated.WriteString(sentinel)
		mutated.Write(file.source[candidate.spanStart:candidate.spanEnd])
		cursor = candidate.spanEnd
		probes = append(probes, importProbe{candidate: candidate, sentinel: sentinel})
	}
	mutated.Write(file.source[cursor:])
	return mutated.Bytes(), probes, nil
}

func matchingImportProbe(probes []importProbe, parserPath string) (importProbe, string, bool) {
	for _, probe := range probes {
		if strings.HasPrefix(parserPath, probe.sentinel) {
			return probe, strings.TrimPrefix(parserPath, probe.sentinel), true
		}
	}
	return importProbe{}, "", false
}

func importKey(kind, path string) string {
	return kind + "\x00" + path
}
