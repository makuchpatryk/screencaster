// Package config loads and validates the per-project screencaster.yaml (FR-001).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

const (
	fileName         = "screencaster.yaml"
	defaultOutputDir = "output"
)

// Config is the validated project configuration. Paths are resolved against
// WorkDir, so callers never need the working directory.
type Config struct {
	BaseURL      string
	StorageState string // absolute path of an existing file
	OutputDir    string // absolute path
	Voices       map[string]string
	WorkDir      string
}

// file mirrors screencaster.yaml. Unknown keys are rejected on decode.
type file struct {
	BaseURL      string            `yaml:"baseUrl"`
	StorageState string            `yaml:"storageState"`
	OutputDir    string            `yaml:"outputDir"`
	Voices       map[string]string `yaml:"voices"`
}

// Load reads screencaster.yaml from workDir. Error texts are specified by the
// PRD and reproduced exactly.
func Load(workDir string) (Config, error) {
	path := filepath.Join(workDir, fileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("config not found: %s", path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var f file
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", fileName, err)
	}

	if f.BaseURL == "" {
		return Config{}, errors.New("baseUrl is required")
	}
	if u, err := url.Parse(f.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("baseUrl must be an absolute http or https URL: %s", f.BaseURL)
	}
	if f.StorageState == "" {
		return Config{}, errors.New("storageState is required")
	}

	storageState := resolve(workDir, f.StorageState)
	if info, err := os.Stat(storageState); err != nil || info.IsDir() {
		return Config{}, fmt.Errorf("storageState not found: %s", storageState)
	}

	if f.OutputDir == "" {
		f.OutputDir = defaultOutputDir
	}
	return Config{
		BaseURL:      f.BaseURL,
		StorageState: storageState,
		OutputDir:    resolve(workDir, f.OutputDir),
		Voices:       f.Voices,
		WorkDir:      workDir,
	}, nil
}

func resolve(workDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}
