package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

func BlobPath(home, digest string) string { return filepath.Join(home, "cas", digest) }

// CreateExclusively publishes complete bytes by hardlink, leaving an existing file intact.
func CreateExclusively(file string, b []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".publish-")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	closeErr := tmp.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = os.Link(tmp.Name(), file); errors.Is(err, os.ErrExist) {
		return false, nil
	}
	return err == nil, err
}
func Put(home string, b []byte) (string, error) {
	d := SHA256(b)
	_, err := CreateExclusively(BlobPath(home, d), b)
	return d, err
}
func Read(home, digest string) ([]byte, error) { return os.ReadFile(BlobPath(home, digest)) }
func PutDirectory(home string, entries []DirectoryEntry) (string, error) {
	t := tuples(entries)
	sort.SliceStable(t, func(i, j int) bool { return jsLess(t[i][0], t[j][0]) })
	b, err := JSONBytes(struct {
		Directory [][3]string `json:"directory"`
	}{t})
	if err != nil {
		return "", err
	}
	return Put(home, b)
}
func ReadDirectory(home, digest string) ([]DirectoryEntry, error) {
	b, err := Read(home, digest)
	if err != nil {
		return nil, err
	}
	var m struct {
		Directory [][3]string `json:"directory"`
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := make([]DirectoryEntry, 0, len(m.Directory))
	for _, t := range m.Directory {
		out = append(out, DirectoryEntry{t[0], t[1], Mode(t[2])})
	}
	return out, nil
}

var ErrRefused = errors.New("file refused")

func readRegular(f *os.File) ([]byte, Mode, error) {
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !s.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: not a regular file", ErrRefused)
	}
	b, err := io.ReadAll(f)
	mode := Regular
	if s.Mode()&0100 != 0 {
		mode = Executable
	}
	return b, mode, err
}
func ReadRegularFile(file string) ([]byte, Mode, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, "", fmt.Errorf("%w: symlink", ErrRefused)
	}
	if err != nil {
		return nil, "", err
	}
	return readRegular(f)
}

// Collect stores symlink target text and refuses nonregular files without following links.
func Collect(home, root string, keep func(string) bool) ([]DirectoryEntry, error) {
	r, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return []DirectoryEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []DirectoryEntry{}
	var walk func(string) error
	walk = func(dir string) error {
		f, err := r.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		children, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return err
		}
		for _, c := range children {
			p := filepath.Join(dir, c.Name())
			rel := filepath.ToSlash(p)
			if c.IsDir() {
				if err = walk(p); err != nil {
					return err
				}
				continue
			}
			if keep != nil && !keep(rel) {
				continue
			}
			var b []byte
			var mode Mode
			if c.Type()&os.ModeSymlink != 0 {
				var target string
				target, err = r.Readlink(p)
				b = []byte(target)
				mode = Symlink
			} else {
				var file *os.File
				file, err = r.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
				if err == nil {
					b, mode, err = readRegular(file)
				}
			}
			if err != nil {
				return fmt.Errorf("%w: %s: %v", ErrRefused, rel, err)
			}
			d, e := Put(home, b)
			if e != nil {
				return e
			}
			out = append(out, DirectoryEntry{rel, d, mode})
		}
		return nil
	}
	if err = walk("."); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return jsLess(out[i].Path, out[j].Path) })
	return out, nil
}

// Materialize copies regular blobs so chmod or later writes cannot mutate the CAS.
func Materialize(home string, entries []DirectoryEntry, target string) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	r, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, e := range entries {
		if !filepath.IsLocal(e.Path) || e.Path == "." {
			return fmt.Errorf("%w: invalid path %q", ErrRefused, e.Path)
		}
		if err = r.MkdirAll(filepath.Dir(e.Path), 0755); err != nil {
			return err
		}
		b, err := Read(home, e.Digest)
		if err != nil {
			return err
		}
		if e.Mode == Symlink {
			if err = r.Symlink(string(b), e.Path); err != nil {
				return err
			}
			continue
		}
		if e.Mode != Regular && e.Mode != Executable {
			return fmt.Errorf("%w: invalid mode", ErrRefused)
		}
		mode := os.FileMode(0644)
		if e.Mode == Executable {
			mode = 0755
		}
		f, err := r.OpenFile(e.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Chmod(mode)
		}
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
	}
	return nil
}
