package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orderedjson"
	"github.com/phosphorco/workbench-go/internal/tsfiles"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

// supportsRoles names the contracts whose declarations use 0.14 roles,
// rendering and observation. Like the 0.13 gate, it names its version.
func supportsRoles(version string) bool {
	return version == "0.14.0"
}

// repositoryFiles lists the working tree minus gitignored files: tracked files
// that still exist plus untracked files that are not ignored. A new source file
// creates its edges before anyone runs git add.
func repositoryFiles(ctx context.Context, root string) ([]string, error) {
	return gitFileList(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
}

// trackedFiles lists files Git tracks that exist in the working tree. Only
// these can be Workbench-owned for --check: untracked and ignored files are
// never owned.
func trackedFiles(ctx context.Context, root string) ([]string, error) {
	return gitFileList(ctx, root, "ls-files", "-z", "--cached")
}

func gitFileList(ctx context.Context, root string, arguments ...string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("list repository files: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	seen := map[string]bool{}
	files := []string{}
	for _, entry := range strings.Split(stdout.String(), "\x00") {
		if entry == "" || seen[entry] {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(entry)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[entry] = true
		files = append(files, entry)
	}
	sort.Strings(files)
	return files, nil
}

// layoutTree is one repository observed for the 0.14 projection.
type layoutTree struct {
	root      string
	files     []string
	fileSet   map[string]bool
	directory map[string]bool
}

func newLayoutTree(root string, files []string) *layoutTree {
	tree := &layoutTree{root: root, files: files, fileSet: map[string]bool{}, directory: map[string]bool{".": true}}
	for _, file := range files {
		tree.fileSet[file] = true
		for parent := path.Dir(file); parent != "." && !tree.directory[parent]; parent = path.Dir(parent) {
			tree.directory[parent] = true
		}
	}
	return tree
}

func (tree *layoutTree) exists(relative string) bool {
	return tree.fileSet[relative] || tree.directory[relative]
}

// resolvePattern resolves one include, exclude or files entry written in a
// config file in directory, for a leaf config in configDirectory.
func resolvePattern(pattern, directory, configDirectory string) string {
	if strings.HasPrefix(pattern, "${configDir}") {
		return cleanRelative(path.Join(configDirectory, strings.TrimPrefix(pattern, "${configDir}")))
	}
	return cleanRelative(path.Join(directory, pattern))
}

func cleanRelative(value string) string {
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func resolvePatterns(patterns []string, directory, configDirectory string) []string {
	resolved := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		resolved = append(resolved, resolvePattern(pattern, directory, configDirectory))
	}
	return resolved
}

// roleSelection resolves a role's include, exclude and files the way
// TypeScript reads an extending config: each key comes from the leaf if set,
// else from the nearest base that sets it; base entries resolve against the
// base's directory except ${configDir}, which is the leaf's directory.
func roleSelection(role contract.Role, config string, bases map[string]contract.TsconfigBase) tsfiles.Spec {
	configDirectory := path.Dir(config)
	base := workspace.RoleBase(role, configDirectory, bases)
	chain := workspace.BaseChain(base, bases)
	spec := tsfiles.Spec{Directory: cleanRelative(configDirectory)}
	if role.Include != nil {
		spec.Include = resolvePatterns(*role.Include, configDirectory, configDirectory)
	} else {
		for _, name := range chain {
			if bases[name].Include != nil {
				spec.Include = resolvePatterns(*bases[name].Include, bases[name].Directory, configDirectory)
				break
			}
		}
	}
	if role.Files != nil {
		spec.Files = resolvePatterns(*role.Files, configDirectory, configDirectory)
	}
	// Without an exclude anywhere TypeScript excludes only the outDir, whose
	// emitted files Git ignores; common package folders are never matched by a
	// wildcard.
	if role.Exclude != nil {
		spec.Exclude = resolvePatterns(*role.Exclude, configDirectory, configDirectory)
	} else {
		for _, name := range chain {
			if bases[name].Exclude != nil {
				spec.Exclude = resolvePatterns(*bases[name].Exclude, bases[name].Directory, configDirectory)
				break
			}
		}
	}
	if spec.Include == nil && spec.Files == nil {
		spec.Include = []string{joinRelative(spec.Directory, "**/*")}
	}
	return spec
}

func joinRelative(directory, name string) string {
	if directory == "" {
		return name
	}
	return directory + "/" + name
}

// staleEntries reports a role's own literal include, exclude and files
// entries that name nothing in the tree. They are rendered as written.
func staleEntries(role contract.Role, config string, tree *layoutTree) []workspace.Note {
	notes := []workspace.Note{}
	directory := path.Dir(config)
	for _, list := range []struct {
		kind   string
		values *[]string
	}{{"staleInclude", role.Include}, {"staleExclude", role.Exclude}, {"staleFile", role.Files}} {
		if list.values == nil {
			continue
		}
		for _, value := range *list.values {
			if strings.ContainsAny(value, "*?") {
				continue
			}
			if resolved := resolvePattern(value, directory, directory); !tree.exists(resolved) {
				notes = append(notes, workspace.Note{Kind: list.kind, Path: config, Detail: fmt.Sprintf("%q names %s, which does not exist", value, resolved)})
			}
		}
	}
	return notes
}

// observeLayoutRoles selects every role's files and scans their imports once.
func observeLayoutRoles(ctx context.Context, bun string, tree *layoutTree, bases map[string]contract.TsconfigBase, packages []workspace.LayoutPackage, projects []workspace.LayoutRole) ([]workspace.Note, error) {
	notes := []workspace.Note{}
	scan := map[string]bool{}
	selectRole := func(role *workspace.LayoutRole) error {
		spec := roleSelection(role.Role, role.Config, bases)
		selected, err := tsfiles.Select(spec, tree.files)
		if err != nil {
			return fmt.Errorf("%s: %w", role.Config, err)
		}
		role.Files = selected
		for _, file := range selected {
			if tsfiles.HasSourceExtension(file) {
				scan[file] = true
			}
		}
		notes = append(notes, staleEntries(role.Role, role.Config, tree)...)
		return nil
	}
	for index := range packages {
		for roleIndex := range packages[index].Roles {
			if err := selectRole(&packages[index].Roles[roleIndex]); err != nil {
				return nil, err
			}
		}
	}
	for index := range projects {
		if err := selectRole(&projects[index]); err != nil {
			return nil, err
		}
	}
	imports, err := scanLayoutImports(ctx, bun, tree.root, sortedSet(scan))
	if err != nil {
		return nil, err
	}
	attach := func(role *workspace.LayoutRole) {
		role.Imports = nil
		for _, file := range role.Files {
			for _, observed := range imports[file] {
				observed.Development = role.Role.Development
				role.Imports = append(role.Imports, observed)
			}
		}
	}
	for index := range packages {
		for roleIndex := range packages[index].Roles {
			attach(&packages[index].Roles[roleIndex])
		}
	}
	for index := range projects {
		attach(&projects[index])
	}
	return notes, nil
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func isDeclarationFile(name string) bool {
	return strings.HasSuffix(name, ".d.ts") || strings.HasSuffix(name, ".d.mts") || strings.HasSuffix(name, ".d.cts")
}

// scanLayoutImports observes every file's imports: Bun confirms the lexer's
// candidates in implementation files, type-only forms are kept, and
// declaration files are read by the lexer alone.
func scanLayoutImports(ctx context.Context, bun, root string, files []string) (map[string][]workspace.Import, error) {
	result := map[string][]workspace.Import{}
	batch := []typeScriptSourceFile{}
	flush := func() error {
		observed, err := parserBackedImports(ctx, bun, batch, true)
		if err != nil {
			return err
		}
		for _, imported := range observed {
			result[imported.Source] = append(result[imported.Source], imported)
		}
		batch = batch[:0]
		return nil
	}
	for _, file := range files {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, err
		}
		contents = withoutShebang(contents)
		if isDeclarationFile(file) {
			for _, candidate := range observeTypeScriptImports(contents) {
				result[file] = append(result[file], workspace.Import{Specifier: candidate.specifier, Source: file, Line: 1 + bytes.Count(contents[:candidate.spanStart], []byte{'\n'})})
			}
			continue
		}
		loader := "ts"
		if strings.HasSuffix(file, ".tsx") {
			loader = "tsx"
		}
		batch = append(batch, typeScriptSourceFile{source: contents, path: file, loader: loader})
		if len(batch) >= 1500 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	for file := range result {
		sort.SliceStable(result[file], func(left, right int) bool {
			if result[file][left].Line != result[file][right].Line {
				return result[file][left].Line < result[file][right].Line
			}
			return result[file][left].Specifier < result[file][right].Specifier
		})
	}
	return result, nil
}

// readCircularEdges reads the repository-generated circular-edge set.
func readCircularEdges(root, relative string) ([]string, error) {
	encoded, err := readRealFileWithin(root, relative)
	if err != nil {
		return nil, fmt.Errorf("read circular-edge set %s: %w; remedy: generate it with the repository's own tooling", relative, err)
	}
	value, err := orderedjson.Decode(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode circular-edge set %s: %w", relative, err)
	}
	edges, exists := value.Get("edges")
	if value.Kind != orderedjson.Object || !exists || edges.Kind != orderedjson.Array {
		return nil, fmt.Errorf("circular-edge set %s must be an object with an edges array of \"source -> target\" strings", relative)
	}
	result := []string{}
	seen := map[string]bool{}
	for _, item := range edges.Items {
		source, target, ok := strings.Cut(item.Text(), " -> ")
		if item.Kind != orderedjson.String || !ok || source == "" || target == "" || source == target {
			return nil, fmt.Errorf("circular-edge set %s entry %s is not \"source -> target\"", relative, mustCompact(item))
		}
		if seen[item.Text()] {
			return nil, fmt.Errorf("circular-edge set %s lists %q twice", relative, item.Text())
		}
		seen[item.Text()] = true
		result = append(result, item.Text())
	}
	return result, nil
}

func mustCompact(value orderedjson.Value) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
