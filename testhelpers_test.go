package routebuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func buildFrontmatter(name string, hostnames []string) string {
	fm := fmt.Sprintf("name: %s\nhostnames:", name)
	for _, h := range hostnames {
		fm += fmt.Sprintf("\n  - %q", h)
	}
	return fm
}

func writeRoutesYAML(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func routesYAMLFor(t *testing.T, dir, name string, hostnames []string, vclPath string) string {
	t.Helper()
	content := fmt.Sprintf("routes:\n  - name: %s\n    hostnames:", name)
	for _, h := range hostnames {
		content += fmt.Sprintf("\n      - %q", h)
	}
	if vclPath != "" {
		content += fmt.Sprintf("\n    vclPath: %q", vclPath)
	}
	content += "\n"
	return writeRoutesYAML(t, dir, name+"-routes.yaml", content)
}
