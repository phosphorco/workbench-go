package skills

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

var compositionLinkPattern = regexp.MustCompile(`\[\x60\$([a-z0-9][a-z0-9-]*)\x60\]\([^)]+\)`)

type Source struct {
	Root string
}

type Skill struct {
	Name         string
	Domain       string
	Dependencies []string
	Files        map[string][]byte
}

type Inventory map[string]Skill

func Discover(sources []Source) (Inventory, error) {
	inventory := make(Inventory)
	for _, source := range sources {
		root := filepath.Join(source.Root, ".agents", "skills")
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skill root %q: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skill, err := readSkill(filepath.Join(root, entry.Name()), entry.Name())
			if err != nil {
				return nil, err
			}
			if previous, exists := inventory[skill.Name]; exists {
				if !equalSkill(previous, skill) {
					return nil, fmt.Errorf("skill %q has conflicting sources", skill.Name)
				}
				continue
			}
			inventory[skill.Name] = skill
		}
	}
	return inventory, nil
}

func Select(inventory Inventory, selection contract.SkillSelection) ([]Skill, error) {
	selected := make(map[string]struct{})
	for name, skill := range inventory {
		if selection.All || contains(selection.Domains, skill.Domain) || contains(selection.Names, name) {
			selected[name] = struct{}{}
		}
	}
	for _, name := range selection.Names {
		if _, exists := inventory[name]; !exists {
			return nil, fmt.Errorf("selected skill %q is absent from the assembled source world", name)
		}
	}

	queue := make([]string, 0, len(selected))
	for name := range selected {
		queue = append(queue, name)
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, dependency := range inventory[name].Dependencies {
			if _, exists := selected[dependency]; exists {
				continue
			}
			if _, exists := inventory[dependency]; !exists {
				return nil, fmt.Errorf("skill %q composes absent skill %q", name, dependency)
			}
			selected[dependency] = struct{}{}
			queue = append(queue, dependency)
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Skill, 0, len(names))
	for _, name := range names {
		result = append(result, inventory[name])
	}
	return result, nil
}

func Apply(root string, selected []Skill) ([]string, error) {
	files := make(map[string][]byte)
	for _, skill := range selected {
		for relative, contents := range skill.Files {
			path := filepath.Join(".agents", "skills", skill.Name, relative)
			if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("skill %q contains escaping path %q", skill.Name, relative)
			}
			files[path] = contents
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changed := make([]string, 0, len(paths))
	for _, relative := range paths {
		target := filepath.Join(root, relative)
		before, err := os.ReadFile(target)
		if err == nil && bytes.Equal(before, files[relative]) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read projected skill path %q: %w", relative, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("create projected skill parent %q: %w", relative, err)
		}
		if err := os.WriteFile(target, files[relative], 0o644); err != nil {
			return nil, fmt.Errorf("write projected skill path %q: %w", relative, err)
		}
		changed = append(changed, filepath.ToSlash(relative))
	}
	return changed, nil
}

func readSkill(root string, name string) (Skill, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %q contains symlink %q", name, path)
		}
		if entry.IsDir() {
			return nil
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
		return nil
	})
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %q: %w", name, err)
	}
	skillFile, exists := files["SKILL.md"]
	if !exists {
		return Skill{}, fmt.Errorf("skill %q has no SKILL.md", name)
	}
	domain, err := frontmatterDomain(string(skillFile))
	if err != nil {
		return Skill{}, fmt.Errorf("skill %q: %w", name, err)
	}
	dependencies := make([]string, 0)
	seen := make(map[string]struct{})
	for _, match := range compositionLinkPattern.FindAllSubmatch(skillFile, -1) {
		dependency := string(match[1])
		if dependency == name {
			return Skill{}, fmt.Errorf("skill %q composes itself", name)
		}
		if _, exists := seen[dependency]; !exists {
			seen[dependency] = struct{}{}
			dependencies = append(dependencies, dependency)
		}
	}
	sort.Strings(dependencies)
	return Skill{Name: name, Domain: domain, Dependencies: dependencies, Files: files}, nil
}

func frontmatterDomain(contents string) (string, error) {
	if !strings.HasPrefix(contents, "---\n") {
		return "", fmt.Errorf("SKILL.md has no YAML frontmatter")
	}
	end := strings.Index(contents[4:], "\n---\n")
	if end < 0 {
		return "", fmt.Errorf("SKILL.md has unterminated YAML frontmatter")
	}
	frontmatter := contents[4 : 4+end]
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "domain:") {
			continue
		}
		domain := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "domain:")), `"'`)
		switch domain {
		case "orchestration", "engineering", "general":
			return domain, nil
		default:
			return "", fmt.Errorf("unknown skill domain %q", domain)
		}
	}
	return "", fmt.Errorf("SKILL.md has no metadata.domain")
}

func equalSkill(left Skill, right Skill) bool {
	if left.Name != right.Name || left.Domain != right.Domain || !equalStrings(left.Dependencies, right.Dependencies) || len(left.Files) != len(right.Files) {
		return false
	}
	for path, contents := range left.Files {
		if !bytes.Equal(contents, right.Files[path]) {
			return false
		}
	}
	return true
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
