package routebuilder

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const maxRouteNameLen = 64

var (
	validRouteName      = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	validObjectNamePart = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// TLSEntry represents a TLS certificate configuration.
// Either PEM must be set (single-file bundle), or both Key and Cert must be set.
type TLSEntry struct {
	PEM  string `yaml:"pem"`
	Key  string `yaml:"key"`
	Cert string `yaml:"cert"`
}

// VCLConfig represents a single route configuration.
type VCLConfig struct {
	// Name is embedded in generated Varnish object names. It must match
	// [a-zA-Z][a-zA-Z0-9_-]*, is limited to 64 characters, and must not be
	// "routing".
	Name       string     `yaml:"name"`
	Hostnames  []string   `yaml:"hostnames"`
	TLS        []TLSEntry `yaml:"tls"`
	VclPath    string     `yaml:"vclPath"` // path Varnish loads (cmdfile + adm.VCLLoad)
	SourceFile string     `yaml:"-"`       // where we read config from (error messages, dupe detection)
}

func validateHostname(h string) error {
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

func hostnamesOverlap(a, b string) bool {
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
	if err := validateRouteName(cfg.Name); err != nil {
		return err
	}
	if len(cfg.Hostnames) == 0 {
		return fmt.Errorf("missing required field: hostnames")
	}
	for _, h := range cfg.Hostnames {
		if err := validateHostname(h); err != nil {
			return err
		}
	}
	return validateTLS(cfg.TLS)
}

func validateRouteName(name string) error {
	if name == "" {
		return fmt.Errorf("missing required field: name")
	}
	if !validRouteName.MatchString(name) {
		return fmt.Errorf("name %q is not a valid route name (must match [a-zA-Z][a-zA-Z0-9_-]*)", name)
	}
	if len(name) > maxRouteNameLen {
		return fmt.Errorf("name %q: exceeds %d-character limit", name, maxRouteNameLen)
	}
	if name == "routing" {
		return fmt.Errorf("name %q is reserved", name)
	}
	return nil
}

func validateObjectNamePart(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !validObjectNamePart.MatchString(value) {
		return fmt.Errorf("%s %q is not a valid Varnish object name component (must match [a-zA-Z0-9_-]+)", name, value)
	}
	return nil
}

func validateTLS(entries []TLSEntry) error {
	for i, t := range entries {
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

func validateRoutingConfig(cfg VCLConfig) error {
	if err := validateRouteName(cfg.Name); err != nil {
		return err
	}
	if len(cfg.Hostnames) == 0 {
		return fmt.Errorf("route %q: missing required field: hostnames", cfg.Name)
	}
	for _, h := range cfg.Hostnames {
		if err := validateHostname(h); err != nil {
			return err
		}
	}
	return nil
}

func validateCmdfileConfig(cfg VCLConfig) error {
	if err := validateRouteName(cfg.Name); err != nil {
		return err
	}
	if cfg.VclPath == "" {
		return fmt.Errorf("route %q: vclPath: field is required", cfg.Name)
	}
	return validateTLS(cfg.TLS)
}

func checkDuplicateNames(configs []VCLConfig) error {
	seen := map[string]string{}
	for _, cfg := range configs {
		if prev, ok := seen[cfg.Name]; ok {
			return fmt.Errorf("duplicate name %q: defined in %s and %s", cfg.Name, prev, cfg.SourceFile)
		}
		seen[cfg.Name] = cfg.SourceFile
	}
	return nil
}

func checkDuplicateHostnames(configs []VCLConfig) error {
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
			if hostnamesOverlap(all[i].hostname, all[j].hostname) {
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

// ValidateConfigs validates all route configurations and checks for duplicate
// names or overlapping hostnames across the set.
func ValidateConfigs(configs []VCLConfig) error {
	for i, cfg := range configs {
		if err := validateConfig(cfg); err != nil {
			return fmt.Errorf("route %d: %w", i, err)
		}
	}
	if err := checkDuplicateNames(configs); err != nil {
		return err
	}
	if err := checkDuplicateHostnames(configs); err != nil {
		return err
	}
	return nil
}
