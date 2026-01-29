package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manifest describes the incident bundle contents.
type Manifest struct {
	IncidentID  string    `json:"incident_id"`
	InfraKey    string    `json:"infra_key"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
	Layers      []string  `json:"layers"`
	Errors      []string  `json:"errors,omitempty"`
}

// assembleTarball creates a .tar.gz bundle from collected layer data.
// layers is a map of layer name → (filename → content).
// Returns the path to the written tarball.
func assembleTarball(manifest Manifest, layers map[string]map[string]string, outputDir string) (string, error) {
	ts := manifest.Timestamp.Format("20060102-150405")
	prefix := fmt.Sprintf("incident-%s-%s", manifest.IncidentID, ts)
	filename := prefix + ".tar.gz"
	outPath := filepath.Join(outputDir, filename)

	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("failed to create tarball: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Write manifest.json
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal manifest: %v", err)
	}
	if err := addFileToTar(tw, filepath.Join(prefix, "manifest.json"), manifestJSON); err != nil {
		return "", err
	}

	// Write layer files
	for layerName, files := range layers {
		for fname, content := range files {
			path := filepath.Join(prefix, layerName, fname)
			if err := addFileToTar(tw, path, []byte(content)); err != nil {
				return "", err
			}
		}
	}

	// Write errors.txt if there were any errors
	if len(manifest.Errors) > 0 {
		errContent := strings.Join(manifest.Errors, "\n")
		if err := addFileToTar(tw, filepath.Join(prefix, "errors.txt"), []byte(errContent)); err != nil {
			return "", err
		}
	}

	return outPath, nil
}

// addFileToTar writes a single file entry to the tar archive.
func addFileToTar(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("failed to write tar header for %s: %v", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("failed to write tar content for %s: %v", name, err)
	}
	return nil
}
