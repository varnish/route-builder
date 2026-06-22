package routebuilder

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ExpandGlobs expands glob patterns in the input arguments.
// Arguments without glob characters are passed through unchanged.
// Returns an error if a glob pattern is invalid or matches no files.
func ExpandGlobs(args []string) ([]string, error) {
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

// IsYAMLFile returns true if the path has a .yaml or .yml extension.
func IsYAMLFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".yaml" || ext == ".yml"
}

// IsVCLFile returns true if the path has a .vcl extension.
func IsVCLFile(path string) bool {
	return filepath.Ext(path) == ".vcl"
}

// AllVCLFiles returns true if all paths have a .vcl extension and the slice is non-empty.
func AllVCLFiles(paths []string) bool {
	for _, p := range paths {
		if !IsVCLFile(p) {
			return false
		}
	}
	return len(paths) > 0
}

// NewTimestamp returns a timestamp string suitable for Varnish object names.
// Format: 2006-01-02T15-04-05_000000000 (nanosecond precision to avoid collisions).
func NewTimestamp() string {
	now := time.Now()
	return now.Format("2006-01-02T15-04-05") + fmt.Sprintf("_%09d", now.Nanosecond())
}

// FindRoute returns the first VCLConfig whose hostnames overlap with the given host,
// or nil if no route matches. The host is matched case-insensitively.
func FindRoute(host string, configs []VCLConfig) *VCLConfig {
	host = strings.ToLower(host)
	for i := range configs {
		for _, h := range configs[i].Hostnames {
			if hostnamesOverlap(host, h) {
				return &configs[i]
			}
		}
	}
	return nil
}
