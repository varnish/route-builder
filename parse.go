package routebuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func extractFrontMatter(content string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if lines[0] != "/* routing" {
		return "", fmt.Errorf("first line must be exactly `/* routing`")
	}
	var yamlLines []string
	for _, line := range lines[1:] {
		if line == "*/" {
			return strings.Join(yamlLines, "\n"), nil
		}
		yamlLines = append(yamlLines, line)
	}
	return "", fmt.Errorf("routing block not closed with `*/`")
}

func unmarshalConfig(raw string) (VCLConfig, error) {
	var cfg VCLConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return VCLConfig{}, fmt.Errorf("invalid YAML front matter: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return VCLConfig{}, fmt.Errorf("front matter %w", err)
	}
	return cfg, nil
}

// ParseVCL parses a VCL file with routing frontmatter and returns its configuration.
func ParseVCL(path string) (VCLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VCLConfig{}, err
	}
	raw, err := extractFrontMatter(string(data))
	if err != nil {
		return VCLConfig{}, err
	}
	cfg, err := unmarshalConfig(raw)
	if err != nil {
		return VCLConfig{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return VCLConfig{}, err
	}
	cfg.SourceFile = abs
	cfg.VclPath = abs
	cfg.TLS = resolveTLSPaths(cfg.TLS, filepath.Dir(abs))
	cfg.Hostnames = normalizeHostnames(cfg.Hostnames)
	return cfg, nil
}

type routesFile struct {
	Routes []VCLConfig `yaml:"routes"`
}

// ParseRoutes parses a YAML routes file and returns all route configurations.
func ParseRoutes(path string) ([]VCLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rf routesFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(rf.Routes) == 0 {
		return nil, fmt.Errorf("routes file contains no routes")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	for i := range rf.Routes {
		if err := validateConfig(rf.Routes[i]); err != nil {
			return nil, fmt.Errorf("route %d: %w", i, err)
		}
		rf.Routes[i].SourceFile = abs
		if rf.Routes[i].VclPath == "" {
			return nil, fmt.Errorf("route %q: vclPath: field is required", rf.Routes[i].Name)
		}
		if !filepath.IsAbs(rf.Routes[i].VclPath) {
			rf.Routes[i].VclPath = filepath.Clean(filepath.Join(dir, rf.Routes[i].VclPath))
		}
		if _, err := os.Stat(rf.Routes[i].VclPath); err != nil {
			return nil, fmt.Errorf("route %q: vclPath: %w", rf.Routes[i].Name, err)
		}
		rf.Routes[i].TLS = resolveTLSPaths(rf.Routes[i].TLS, dir)
		rf.Routes[i].Hostnames = normalizeHostnames(rf.Routes[i].Hostnames)
		for _, t := range rf.Routes[i].TLS {
			for _, p := range []string{t.PEM, t.Cert, t.Key} {
				if p != "" {
					if _, err := os.Stat(p); err != nil {
						return nil, fmt.Errorf("route %q: tls: %w", rf.Routes[i].Name, err)
					}
				}
			}
		}
	}
	return rf.Routes, nil
}

// MarshalRoutes serializes route configurations to YAML format.
func MarshalRoutes(configs []VCLConfig) ([]byte, error) {
	return yaml.Marshal(routesFile{Routes: configs})
}
