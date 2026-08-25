package acceptance

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCurrentVocabularyCensus(t *testing.T) {
	workbenchRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	files := censusFiles(t, workbenchRoot)

	const (
		apacheLegal       = "Apache legal text"
		immutableEvidence = "immutable append-only evidence"
		legacyAdapter     = "released 0.1-0.3 compatibility adapter"
		legacyFixture     = "released 0.1-0.3 compatibility fixture"
	)
	allowed := map[string]string{
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:3:437":                   immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:10:48":                   immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:11:48":                   immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:14:39":                   immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:15:39":                   immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:19:431":                  immutableEvidence,
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl:20:48":                   immutableEvidence,
		"tools/workbench-go/LICENSE:69:7":                                                     apacheLegal,
		"tools/workbench-go/LICENSE:76:7":                                                     apacheLegal,
		"tools/workbench-go/acceptance/compatibility_v020_v030_snapshot_test.go:53:126":       legacyFixture,
		"tools/workbench-go/internal/legacy/v020v030snapshot/adapter.go:11:28":                legacyAdapter,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts.go:8:55":        legacyAdapter,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts_test.go:18:34":  legacyFixture,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts_test.go:65:36":  legacyFixture,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts_test.go:87:53":  legacyFixture,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts_test.go:118:33": legacyFixture,
		"tools/workbench-go/internal/setup/legacy_v010_v030_managed_checkouts_test.go:126:46": legacyFixture,
	}

	retiredVocabulary := "wo" + "rld"
	observed := make(map[string]struct{})
	for _, file := range files {
		contents, err := os.ReadFile(file.path)
		if err != nil {
			t.Fatalf("read census source %q: %v", file.name, err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		for line := 1; scanner.Scan(); line++ {
			lower := strings.ToLower(scanner.Text())
			for offset := 0; ; {
				index := strings.Index(lower[offset:], retiredVocabulary)
				if index < 0 {
					break
				}
				column := offset + index + 1
				key := fmt.Sprintf("%s:%d:%d", file.name, line, column)
				if category, ok := allowed[key]; !ok {
					t.Errorf("unclassified retired vocabulary at %s", key)
				} else if category == "" {
					t.Errorf("allowlisted occurrence %s has no compatibility/evidence class", key)
				}
				observed[key] = struct{}{}
				offset += index + len(retiredVocabulary)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan census source %q: %v", file.name, err)
		}
	}
	for key, category := range allowed {
		if _, ok := observed[key]; !ok && censusIncludes(files, key) {
			t.Errorf("stale %s allowlist entry %s", category, key)
		}
	}
}

type censusFile struct {
	name string
	path string
}

func censusFiles(t *testing.T, workbenchRoot string) []censusFile {
	t.Helper()
	repositoryRootCommand := exec.Command("git", "rev-parse", "--show-toplevel")
	repositoryRootCommand.Dir = workbenchRoot
	repositoryRootOutput, err := repositoryRootCommand.Output()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	repositoryRoot := strings.TrimSpace(string(repositoryRootOutput))

	command := exec.Command("git", "ls-files", "-z", "--full-name", "--cached", "--others", "--exclude-standard", "--", ".")
	command.Dir = workbenchRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("enumerate Workbench source: %v", err)
	}
	encoded := strings.TrimSuffix(string(output), "\x00")
	if encoded == "" {
		t.Fatal("Workbench source census is empty")
	}
	files := make([]censusFile, 0, len(strings.Split(encoded, "\x00"))+2)
	for _, repositoryRelative := range strings.Split(encoded, "\x00") {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(repositoryRelative))
		workbenchRelative, err := filepath.Rel(workbenchRoot, path)
		if err != nil || workbenchRelative == ".." || strings.HasPrefix(workbenchRelative, ".."+string(filepath.Separator)) {
			t.Fatalf("census source escaped Workbench root: %q", repositoryRelative)
		}
		files = append(files, censusFile{
			name: filepath.ToSlash(filepath.Join("tools/workbench-go", workbenchRelative)),
			path: path,
		})
	}
	for _, name := range []string{
		".context/plans/workbench-go-v1/workbench-go-v1.plan.ts",
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl",
	} {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(name))
		if _, err := os.Stat(path); err == nil {
			files = append(files, censusFile{name: name, path: path})
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect optional census source %q: %v", name, err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files
}

func censusIncludes(files []censusFile, occurrence string) bool {
	for _, file := range files {
		if strings.HasPrefix(occurrence, file.name+":") {
			return true
		}
	}
	return false
}
