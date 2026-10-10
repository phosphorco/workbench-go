package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type ScratchPointer struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.!~*'()", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
func scratchPath(home, verifier, unit string) (string, error) {
	if err := component(verifier); err != nil {
		return "", err
	}
	encoded := encodeURIComponent(unit)
	if err := component(encoded); err != nil {
		return "", err
	}
	return filepath.Join(home, "scratch", verifier, encoded), nil
}
func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".scratch-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), p)
}
func SaveScratch(home string, v Verifier, unit string, entries []DirectoryEntry) error {
	blocked, err := Witnessed(home, v)
	if err != nil {
		return err
	}
	if blocked {
		return nil
	}
	p, err := scratchPath(home, v.ID, unit)
	if err != nil {
		return err
	}
	d, err := PutDirectory(home, entries)
	if err != nil {
		return err
	}
	b, err := pretty(ScratchPointer{v.Version, d})
	if err != nil {
		return err
	}
	return writeAtomic(p, b)
}

// RestoreScratch only restores the current version when that version has no witness.
func RestoreScratch(home string, v Verifier, unit string) (*ScratchPointer, []DirectoryEntry, error) {
	blocked, err := Witnessed(home, v)
	if err != nil || blocked {
		return nil, nil, err
	}
	p, err := scratchPath(home, v.ID, unit)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var pointer ScratchPointer
	if err = json.Unmarshal(b, &pointer); err != nil {
		return nil, nil, err
	}
	if pointer.Version != v.Version {
		return nil, nil, nil
	}
	entries, err := ReadDirectory(home, pointer.Digest)
	return &pointer, entries, err
}

// DropScratch removes only pointers whose recorded version matches v.
func DropScratch(home string, v Verifier) error {
	if err := component(v.ID); err != nil {
		return err
	}
	dir := filepath.Join(home, "scratch", v.ID)
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".scratch-") {
			continue
		}
		p := filepath.Join(dir, f.Name())
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var pointer ScratchPointer
		if err = json.Unmarshal(b, &pointer); err != nil {
			return err
		}
		if pointer.Version == v.Version {
			if err = os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
