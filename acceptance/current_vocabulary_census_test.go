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
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files := sourceFiles(t, repositoryRoot)
	files = append(files,
		".context/plans/workbench-go-v1/workbench-go-v1.plan.ts",
		".context/plans/workbench-go-v1/workbench-go-v1.ledger.jsonl",
	)
	sort.Strings(files)
	files = compactStrings(files)

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
	for _, relative := range files {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read census source %q: %v", relative, err)
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
				key := fmt.Sprintf("%s:%d:%d", relative, line, column)
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
			t.Fatalf("scan census source %q: %v", relative, err)
		}
	}
	for key, category := range allowed {
		if _, ok := observed[key]; !ok {
			t.Errorf("stale %s allowlist entry %s", category, key)
		}
	}
}

func sourceFiles(t *testing.T, repositoryRoot string) []string {
	t.Helper()
	command := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "tools/workbench-go")
	command.Dir = repositoryRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("enumerate Workbench source: %v", err)
	}
	encoded := strings.TrimSuffix(string(output), "\x00")
	if encoded == "" {
		t.Fatal("Workbench source census is empty")
	}
	return strings.Split(encoded, "\x00")
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
