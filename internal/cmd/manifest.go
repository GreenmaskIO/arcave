package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const gmManifestFile = "manifest.json"

// savepointVolume records where the data of one stateful mount was snapshotted,
// plus what to clear before restoring it.
type savepointVolume struct {
	Service   string `json:"service"`          // compose service that owns the mount
	MountPath string `json:"mount_path"`       // mount path inside the container
	Type      string `json:"type"`             // "volume" or "bind"
	Source    string `json:"source,omitempty"` // Docker volume name or host path; "" if anonymous
	Path      string `json:"path"`             // snapshot archive, relative to the project root
}

// savepointRecord is one savepoint entry persisted in .gm/manifest.json.
type savepointRecord struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	CreatedAt   time.Time         `json:"created_at"`
	Volumes     []savepointVolume `json:"volumes"`
}

// manifest is the savepoint index persisted in .gm/manifest.json.
type manifest struct {
	Savepoints []savepointRecord `json:"savepoints"`
}

func manifestFilePath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	return filepath.Join(cwd, gmDir, gmManifestFile), nil
}

// loadManifest reads .gm/manifest.json; a missing file yields an empty
// manifest so list/remove work before the first savepoint is created.
func loadManifest() (*manifest, error) {
	path, err := manifestFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &manifest{}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, nil
}

// saveManifest writes the manifest atomically (temp file + rename) so a crash
// mid-write cannot corrupt the savepoint index.
func saveManifest(m *manifest) error {
	path, err := manifestFilePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
