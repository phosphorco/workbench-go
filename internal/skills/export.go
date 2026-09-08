package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

// Export writes complete skill trees to a catalog directory. Unlike managed
// projection, it never adopts, replaces, or removes existing skills.
func Export(root string, selected []Skill) ([]string, error) {
	desired, err := desiredSkills(selected)
	if err != nil {
		return nil, err
	}
	if err := preflightProjectedLinks(root, desired); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(desired) {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return nil, fmt.Errorf("skill destination %q already exists: %w", filepath.Join(root, name), os.ErrExist)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect export destination: %w", err)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create skill export directory: %w", err)
	}
	paths := []string{}
	for _, name := range sortedKeys(desired) {
		target := filepath.Join(root, name)
		if err := os.Mkdir(target, 0o755); err != nil {
			return nil, fmt.Errorf("reserve skill destination %q: %w", target, err)
		}
		capability, err := os.OpenRoot(target)
		if err != nil {
			return nil, err
		}
		for _, relative := range sortedKeys(desired[name].Files) {
			if err = capability.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
				break
			}
			err = capability.WriteFile(relative, desired[name].Files[relative], 0o644)
			if err != nil {
				break
			}
			paths = append(paths, filepath.Join(target, relative))
		}
		closeErr := capability.Close()
		if err != nil {
			return nil, fmt.Errorf("write skill %q (partial export remains at %q): %w", name, target, err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return paths, nil
}
