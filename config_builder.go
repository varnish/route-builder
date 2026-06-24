package routebuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var validLabelName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

const (
	prefixRouting = "rb-routing-"
	prefixLabel   = "rb-label-"
	prefixVCL     = "rb-vcl-"
	prefixCert    = "rb-cert-"
)

func expandGlobs(args []string) ([]string, error) {
	var out []string
	for _, arg := range args {
		if !strings.ContainsAny(arg, "*?[") {
			out = append(out, arg)
			continue
		}
		matches, err := filepath.Glob(arg)
		if err != nil {
			return nil, fmt.Errorf("invalid glob %q: %w", arg, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matched %q", arg)
		}
		out = append(out, matches...)
	}
	return out, nil
}

func isYAMLFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".yaml" || ext == ".yml"
}

func isVCLFile(path string) bool {
	return filepath.Ext(path) == ".vcl"
}

func allVCLFiles(paths []string) bool {
	for _, p := range paths {
		if !isVCLFile(p) {
			return false
		}
	}
	return len(paths) > 0
}

type TLSEntry struct {
	PEM  string `yaml:"pem"`
	Key  string `yaml:"key"`
	Cert string `yaml:"cert"`
}

type VCLConfig struct {
	Name       string     `yaml:"name"`
	Hostnames  []string   `yaml:"hostnames"`
	TLS        []TLSEntry `yaml:"tls"`
	VclPath    string     `yaml:"vclPath"` // path Varnish loads (cmdfile + adm.VCLLoad)
	SourceFile string     `yaml:"-"`
}

func ExtractFrontMatter(content string) (string, error) {
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

func ValidateHostname(h string) error {
	for _, seg := range strings.Split(h, ".") {
		if seg == "" {
			return fmt.Errorf("hostname %q: empty segment", h)
		}
		if strings.Contains(seg, "*") && seg != "*" {
			return fmt.Errorf("hostname %q: wildcard must be the only character in a segment (got %q)", h, seg)
		}
	}
	return nil
}

func HostnamesOverlap(a, b string) bool {
	segsA := strings.Split(a, ".")
	segsB := strings.Split(b, ".")
	if len(segsA) != len(segsB) {
		return false
	}
	for i := range segsA {
		if segsA[i] != "*" && segsB[i] != "*" && segsA[i] != segsB[i] {
			return false
		}
	}
	return true
}

func normalizeHostnames(hostnames []string) []string {
	out := make([]string, len(hostnames))
	for i, h := range hostnames {
		out[i] = strings.ToLower(h)
	}
	return out
}

func validateConfig(cfg VCLConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("missing required field: name")
	}
	if !validLabelName.MatchString(cfg.Name) {
		return fmt.Errorf("name %q is not a valid VCL label name (must match [a-zA-Z][a-zA-Z0-9_]*)", cfg.Name)
	}
	if len(cfg.Name) > 64 {
		return fmt.Errorf("name %q: exceeds 64-character limit", cfg.Name)
	}
	if cfg.Name == "routing" {
		return fmt.Errorf("name %q is reserved", cfg.Name)
	}
	if len(cfg.Hostnames) == 0 {
		return fmt.Errorf("missing required field: hostnames")
	}
	for _, h := range cfg.Hostnames {
		if err := ValidateHostname(h); err != nil {
			return err
		}
	}
	for i, t := range cfg.TLS {
		hasPEM := t.PEM != ""
		hasKey := t.Key != ""
		hasCert := t.Cert != ""
		switch {
		case !hasPEM && !hasKey && !hasCert:
			return fmt.Errorf("tls entry %d: must specify pem or key+cert", i)
		case hasPEM && (hasKey || hasCert):
			return fmt.Errorf("tls entry %d: cannot mix pem with key/cert", i)
		case !hasPEM && hasKey != hasCert:
			return fmt.Errorf("tls entry %d: key and cert must both be set", i)
		}
	}
	return nil
}

func UnmarshalConfig(raw string) (VCLConfig, error) {
	var cfg VCLConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return VCLConfig{}, fmt.Errorf("invalid YAML front matter: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return VCLConfig{}, fmt.Errorf("front matter %w", err)
	}
	return cfg, nil
}

func CheckDuplicateNames(configs []VCLConfig) error {
	seen := map[string]string{}
	for _, cfg := range configs {
		if prev, ok := seen[cfg.Name]; ok {
			return fmt.Errorf("duplicate name %q: defined in %s and %s", cfg.Name, prev, cfg.SourceFile)
		}
		seen[cfg.Name] = cfg.SourceFile
	}
	return nil
}

func CheckDuplicateHostnames(configs []VCLConfig) error {
	type entry struct{ hostname, label string }
	var all []entry
	for _, cfg := range configs {
		label := fmt.Sprintf("%s (%s)", cfg.Name, cfg.SourceFile)
		for _, h := range cfg.Hostnames {
			all = append(all, entry{h, label})
		}
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if HostnamesOverlap(all[i].hostname, all[j].hostname) {
				return fmt.Errorf("hostname %q overlaps with %q: used in %s and %s",
					all[i].hostname, all[j].hostname, all[i].label, all[j].label)
			}
		}
	}
	return nil
}

func resolveTLSPaths(entries []TLSEntry, dir string) []TLSEntry {
	out := make([]TLSEntry, len(entries))
	for i, t := range entries {
		if t.PEM != "" && !filepath.IsAbs(t.PEM) {
			t.PEM = filepath.Clean(filepath.Join(dir, t.PEM))
		}
		if t.Cert != "" && !filepath.IsAbs(t.Cert) {
			t.Cert = filepath.Clean(filepath.Join(dir, t.Cert))
		}
		if t.Key != "" && !filepath.IsAbs(t.Key) {
			t.Key = filepath.Clean(filepath.Join(dir, t.Key))
		}
		out[i] = t
	}
	return out
}

func ParseVCL(path string) (VCLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VCLConfig{}, err
	}
	raw, err := ExtractFrontMatter(string(data))
	if err != nil {
		return VCLConfig{}, err
	}
	cfg, err := UnmarshalConfig(raw)
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

func marshalRoutes(configs []VCLConfig) ([]byte, error) {
	return yaml.Marshal(routesFile{Routes: configs})
}
