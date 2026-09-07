package skills

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

func (s Source) readDir(name string) ([]fs.DirEntry, error) {
	if s.FS != nil {
		return fs.ReadDir(s.FS, filepath.ToSlash(name))
	}
	return os.ReadDir(name)
}

func (s Source) stat(name string) (fs.FileInfo, error) {
	if s.FS != nil {
		return fs.Stat(s.FS, filepath.ToSlash(name))
	}
	return os.Stat(name)
}

func (s Source) lstat(name string) (fs.FileInfo, error) {
	if s.FS == nil {
		return os.Lstat(name)
	}
	name = filepath.ToSlash(name)
	if name == "." {
		return fs.Stat(s.FS, name)
	}
	entries, err := fs.ReadDir(s.FS, path.Dir(name))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == path.Base(name) {
			return entry.Info()
		}
	}
	return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
}

func (s Source) readFile(name string) ([]byte, error) {
	if s.FS != nil {
		return fs.ReadFile(s.FS, filepath.ToSlash(name))
	}
	return os.ReadFile(name)
}

func (s Source) walkDir(root string, visit fs.WalkDirFunc) error {
	if s.FS != nil {
		return fs.WalkDir(s.FS, filepath.ToSlash(root), visit)
	}
	return filepath.WalkDir(root, visit)
}
