package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Source grants the catalog loader read authority to one exact flat skill
// directory. Name is stable provenance (normally a repository identity); Root
// is never inferred from it.
type Source struct {
	Name string
	Root string
}

type Skill struct {
	Name         string
	Description  string
	Domain       string
	Dependencies []string
	Links        []LocalLink
	Files        map[string][]byte
}

// LocalLink is a parsed relative Markdown link carried into projection
// preflight. DocumentPath is relative to the authored skill directory.
type LocalLink struct {
	Source       string
	DocumentPath string
	Line         int
	Target       string
}

type SkillSummary struct {
	Source      string
	Name        string
	Description string
	Domain      string
	Path        string
}

// Diagnostic retains provenance as data so setup and the CLI can render the
// same parsed fact at their respective public boundaries.
type Diagnostic struct {
	Source  string
	Path    string
	Line    int
	Message string
}

func (diagnostic Diagnostic) Location() string {
	location := diagnostic.Path
	if diagnostic.Source != "" {
		location = diagnostic.Source + ":" + location
	}
	if diagnostic.Line > 0 {
		location = fmt.Sprintf("%s:%d", location, diagnostic.Line)
	}
	return location
}

type Report struct {
	SkillCount           int
	CompositionEdgeCount int
	Skills               []SkillSummary
	Issues               []Diagnostic
	Warnings             []Diagnostic
}

type Inventory map[string]Skill

// Catalog is the sole parsed representation of authored skill facts. Its
// inventory is private so selection and projection cannot bypass validation.
type Catalog struct {
	inventory Inventory
	report    Report
}

func (catalog Catalog) Report() Report {
	report := catalog.report
	report.Skills = append([]SkillSummary(nil), report.Skills...)
	report.Issues = append([]Diagnostic(nil), report.Issues...)
	report.Warnings = append([]Diagnostic(nil), report.Warnings...)
	return report
}

type catalogDocument struct {
	source       Source
	skillName    string
	absolutePath string
	relativePath string
	contents     string
}

type parsedSkill struct {
	source       Source
	relativeFile string
	skill        Skill
	documents    []catalogDocument
}

// Load reads and validates every explicitly designated flat catalog. Contract
// failures are report values; only failures to observe the designated inputs
// are returned as Go errors.
func Load(sources []Source) (Catalog, error) {
	orderedSources := append([]Source(nil), sources...)
	sort.Slice(orderedSources, func(left int, right int) bool {
		if orderedSources[left].Name != orderedSources[right].Name {
			return orderedSources[left].Name < orderedSources[right].Name
		}
		return orderedSources[left].Root < orderedSources[right].Root
	})

	report := Report{}
	rootDocuments := make([]catalogDocument, 0)
	domainDocuments := make([]catalogDocument, 0)
	parsedSkills := make([]parsedSkill, 0)
	knownSkillNames := make([]string, 0)

	for _, source := range orderedSources {
		info, err := os.Lstat(source.Root)
		if err != nil {
			return Catalog{}, fmt.Errorf("inspect skill catalog %q: %w", source.Root, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Catalog{}, fmt.Errorf("skill catalog %q is not a real directory", source.Root)
		}
		entries, err := os.ReadDir(source.Root)
		if err != nil {
			return Catalog{}, fmt.Errorf("read skill catalog %q: %w", source.Root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillRoot := filepath.Join(source.Root, entry.Name())
			skillFile := filepath.Join(skillRoot, "SKILL.md")
			fileInfo, err := os.Lstat(skillFile)
			if os.IsNotExist(err) {
				report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: entry.Name(), Message: "skill directory has no SKILL.md"})
				continue
			}
			if err != nil {
				return Catalog{}, fmt.Errorf("inspect skill contract %q: %w", skillFile, err)
			}
			if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
				return Catalog{}, fmt.Errorf("skill contract %q is not a regular file", skillFile)
			}

			report.SkillCount++
			knownSkillNames = append(knownSkillNames, entry.Name())
			files, documents, err := readSkillTree(source, skillRoot, entry.Name())
			if err != nil {
				return Catalog{}, err
			}
			skillSource := string(files["SKILL.md"])
			rootDocument := catalogDocument{
				source:       source,
				skillName:    entry.Name(),
				absolutePath: skillFile,
				relativePath: filepath.ToSlash(filepath.Join(entry.Name(), "SKILL.md")),
				contents:     skillSource,
			}
			rootDocuments = append(rootDocuments, rootDocument)

			frontmatter, issue := parseFrontmatter(skillSource)
			if issue != "" {
				report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: rootDocument.relativePath, Line: 1, Message: issue})
				continue
			}
			if !skillNamePattern.MatchString(entry.Name()) {
				report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: rootDocument.relativePath, Line: 1, Message: "skill folder name must use lowercase letters, digits, and hyphens"})
			}
			if frontmatter.Name != entry.Name() {
				report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: rootDocument.relativePath, Line: 1, Message: fmt.Sprintf("name must equal folder %s", entry.Name())})
			}
			if strings.TrimSpace(frontmatter.Description) == "" {
				report.Warnings = append(report.Warnings, Diagnostic{Source: source.Name, Path: rootDocument.relativePath, Message: "frontmatter description is missing or empty"})
			}

			report.Skills = append(report.Skills, SkillSummary{
				Source:      source.Name,
				Name:        frontmatter.Name,
				Description: frontmatter.Description,
				Domain:      frontmatter.Domain,
				Path:        rootDocument.relativePath,
			})
			checkSkillMetadata(source, entry.Name(), files, frontmatter.Description, &report)

			validDocuments := make([]catalogDocument, 0, len(documents))
			for _, document := range documents {
				validDocuments = append(validDocuments, document)
				domainDocuments = append(domainDocuments, document)
			}
			parsedSkills = append(parsedSkills, parsedSkill{
				source:       source,
				relativeFile: rootDocument.relativePath,
				skill: Skill{
					Name:        frontmatter.Name,
					Description: frontmatter.Description,
					Domain:      frontmatter.Domain,
					Files:       files,
				},
				documents: validDocuments,
			})
		}
	}

	knownNames := make(map[string]struct{}, len(knownSkillNames))
	skillSourceRoots := make(map[string]map[string]struct{}, len(knownSkillNames))
	for _, parsed := range parsedSkills {
		if skillSourceRoots[parsed.skill.Name] == nil {
			skillSourceRoots[parsed.skill.Name] = make(map[string]struct{})
		}
		skillSourceRoots[parsed.skill.Name][parsed.source.Root] = struct{}{}
	}
	for _, name := range knownSkillNames {
		knownNames[name] = struct{}{}
	}
	sort.Strings(knownSkillNames)
	knownSkillNames = uniqueStrings(knownSkillNames)

	rootFiles := make(map[string]struct{}, len(rootDocuments))
	for _, document := range rootDocuments {
		rootFiles[document.absolutePath] = struct{}{}
	}
	contractDocuments := append([]catalogDocument(nil), rootDocuments...)
	for _, document := range domainDocuments {
		if _, root := rootFiles[document.absolutePath]; !root {
			contractDocuments = append(contractDocuments, document)
		}
	}

	dependencies := make(map[string]map[string]struct{})
	localLinks := make(map[string][]LocalLink)
	for _, document := range contractDocuments {
		for _, link := range extractLinks(document.contents) {
			if link.external {
				continue
			}
			documentPath := strings.TrimPrefix(document.relativePath, document.skillName+"/")
			localLinks[documentKey(document)] = append(localLinks[documentKey(document)], LocalLink{
				Source:       document.source.Name,
				DocumentPath: documentPath,
				Line:         link.line,
				Target:       link.target,
			})
			resolved := filepath.Clean(filepath.Join(filepath.Dir(document.absolutePath), filepath.FromSlash(link.target)))
			targetName := ""
			crossSourcePeer := false
			if link.composition {
				targetName = filepath.Base(filepath.Dir(resolved))
				_, knownPeer := knownNames[targetName]
				_, sameSource := skillSourceRoots[targetName][document.source.Root]
				crossSourcePeer = targetName != document.skillName && knownPeer && !sameSource
			}
			if _, err := os.Stat(resolved); os.IsNotExist(err) && !crossSourcePeer {
				report.Issues = append(report.Issues, Diagnostic{Source: document.source.Name, Path: document.relativePath, Line: link.line, Message: fmt.Sprintf("missing link target %s", link.target)})
			} else if err != nil && !os.IsNotExist(err) {
				return Catalog{}, fmt.Errorf("inspect skill link target %q: %w", resolved, err)
			}
			if !link.composition {
				continue
			}
			report.CompositionEdgeCount++
			if targetName != document.skillName {
				if _, known := knownNames[targetName]; known {
					if !hasSkillLabel(link.label, targetName) {
						report.Issues = append(report.Issues, Diagnostic{Source: document.source.Name, Path: document.relativePath, Line: link.line, Message: fmt.Sprintf("composition link to %q must name %q in its label", targetName, "$"+targetName)})
					}
					if dependencies[documentKey(document)] == nil {
						dependencies[documentKey(document)] = make(map[string]struct{})
					}
					dependencies[documentKey(document)][targetName] = struct{}{}
				}
			}
		}
		checkSkillReferences(document, knownSkillNames, &report)
	}

	domainByName := make(map[string]string, len(report.Skills))
	for _, skill := range report.Skills {
		domainByName[skill.Name] = skill.Domain
	}
	for _, document := range domainDocuments {
		sourceDomain, exists := domainByName[document.skillName]
		if !exists {
			continue
		}
		for _, link := range extractLinks(document.contents) {
			if link.external || !link.composition {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(document.absolutePath), filepath.FromSlash(link.target)))
			targetName := filepath.Base(filepath.Dir(resolved))
			targetDomain, exists := domainByName[targetName]
			if !exists || sourceDomain == targetDomain || targetDomain == "general" {
				continue
			}
			report.Warnings = append(report.Warnings, Diagnostic{
				Source:  document.source.Name,
				Path:    document.relativePath,
				Line:    link.line,
				Message: fmt.Sprintf("$%s (%s) references $%s (%s); cross-domain skill references may target only %q", document.skillName, sourceDomain, targetName, targetDomain, "general"),
			})
		}
	}

	inventory := make(Inventory)
	for _, parsed := range parsedSkills {
		dependencySet := make(map[string]struct{})
		for _, document := range parsed.documents {
			for dependency := range dependencies[documentKey(document)] {
				dependencySet[dependency] = struct{}{}
			}
			parsed.skill.Links = append(parsed.skill.Links, localLinks[documentKey(document)]...)
		}
		parsed.skill.Dependencies = sortedKeys(dependencySet)
		if previous, exists := inventory[parsed.skill.Name]; exists {
			if !equalSkill(previous, parsed.skill) {
				report.Issues = append(report.Issues, Diagnostic{Source: parsed.source.Name, Path: parsed.relativeFile, Message: fmt.Sprintf("skill %q has conflicting sources", parsed.skill.Name)})
			}
			continue
		}
		inventory[parsed.skill.Name] = parsed.skill
	}

	return Catalog{inventory: inventory, report: report}, nil
}

func Select(catalog Catalog, selection contract.SkillSelection) ([]Skill, error) {
	if len(catalog.report.Issues) > 0 {
		return nil, fmt.Errorf("skill catalog contains %d contract violation%s", len(catalog.report.Issues), plural(len(catalog.report.Issues)))
	}
	selected := make(map[string]struct{})
	for name, skill := range catalog.inventory {
		if selection.All || contains(selection.Domains, skill.Domain) || contains(selection.Names, name) {
			selected[name] = struct{}{}
		}
	}
	for _, name := range selection.Names {
		if _, exists := catalog.inventory[name]; !exists {
			return nil, fmt.Errorf("selected skill %q is absent from the assembled source repositories", name)
		}
	}
	queue := sortedKeys(selected)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, dependency := range catalog.inventory[name].Dependencies {
			if _, exists := selected[dependency]; exists {
				continue
			}
			if _, exists := catalog.inventory[dependency]; !exists {
				return nil, fmt.Errorf("skill %q composes absent skill %q", name, dependency)
			}
			selected[dependency] = struct{}{}
			queue = append(queue, dependency)
		}
	}
	names := sortedKeys(selected)
	result := make([]Skill, 0, len(names))
	for _, name := range names {
		result = append(result, cloneSkill(catalog.inventory[name]))
	}
	return result, nil
}

func readSkillTree(source Source, root string, skillName string) (map[string][]byte, []catalogDocument, error) {
	files := make(map[string][]byte)
	documents := make([]catalogDocument, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %q contains symlink %q", skillName, path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("skill %q contains non-regular file %q", skillName, path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = contents
		if strings.HasSuffix(relative, ".md") {
			documents = append(documents, catalogDocument{
				source:       source,
				skillName:    skillName,
				absolutePath: path,
				relativePath: filepath.ToSlash(filepath.Join(skillName, relative)),
				contents:     string(contents),
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read skill %q from %q: %w", skillName, source.Root, err)
	}
	sort.Slice(documents, func(left int, right int) bool { return documents[left].relativePath < documents[right].relativePath })
	return files, documents, nil
}

func documentKey(document catalogDocument) string {
	return document.source.Name + "\x00" + document.absolutePath
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	unique := values[:1]
	for _, value := range values[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func cloneSkill(skill Skill) Skill {
	files := make(map[string][]byte, len(skill.Files))
	for path, contents := range skill.Files {
		files[path] = bytes.Clone(contents)
	}
	return Skill{
		Name:         skill.Name,
		Description:  skill.Description,
		Domain:       skill.Domain,
		Dependencies: append([]string(nil), skill.Dependencies...),
		Links:        append([]LocalLink(nil), skill.Links...),
		Files:        files,
	}
}
