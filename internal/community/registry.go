package community

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Load reads a checkout of the registry: every addons/<owner>/<id>.json, each where its
// repository and id put it, and revoked.json (absent: nothing revoked).
func Load(root string) ([]Addon, []Revoked, error) {
	var addons []Addon
	dir := filepath.Join(root, "addons")
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Name() == ".gitkeep" {
			return nil
		}
		if !strings.HasSuffix(rel, ".json") {
			return fmt.Errorf("%s: only snapshots (.json) belong in addons/", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var a Addon
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if err := a.Check(); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if a.Path() != rel {
			return fmt.Errorf("%s: %w: it belongs at %s", rel, ErrLayout, a.Path())
		}
		addons = append(addons, a)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	revoked := []Revoked{}
	data, err := os.ReadFile(filepath.Join(root, "revoked.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, nil, err
	default:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&revoked); err != nil {
			return nil, nil, fmt.Errorf("revoked.json: %w", err)
		}
	}
	return addons, revoked, nil
}
