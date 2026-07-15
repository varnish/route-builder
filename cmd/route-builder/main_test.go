package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rb "github.com/varnish/route-builder"
	"github.com/varnish/varnish-go/vtest"
)

// buildFrontmatter builds the YAML block used inside a VCL routing comment.
func buildFrontmatter(name string, hostnames []string) string {
	fm := fmt.Sprintf("name: %s\nhostnames:", name)
	for _, h := range hostnames {
		fm += fmt.Sprintf("\n  - %q", h)
	}
	return fm
}

// writeTempVCL writes a VCL file with routing frontmatter and returns its path.
func writeTempVCL(t *testing.T, dir, name string, hostnames []string) string {
	t.Helper()
	content := fmt.Sprintf("/* routing\n%s\n*/\nvcl 4.1;\n", buildFrontmatter(name, hostnames))
	path := filepath.Join(dir, name+".vcl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRoutesYAML writes a raw YAML string to a file and returns its path.
func writeRoutesYAML(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// routesYAMLFor writes a routes YAML file that references the given VCL path.
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

// writeRouteVCL writes a VCL file that returns synth(200, name) for testing.
func writeRouteVCL(t *testing.T, dir, name string, hostnames []string) string {
	t.Helper()
	content := fmt.Sprintf("/* routing\n%s\n*/\nvcl 4.1;\nbackend default none;\nsub vcl_recv {\n    return(synth(200, %q));\n}\n", buildFrontmatter(name, hostnames), name)
	path := filepath.Join(dir, name+".vcl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadLabels loads each route VCL and creates its label for a vtest instance.
func loadLabels(t *testing.T, v vtest.Varnish, configs []rb.VCLConfig, ts string) {
	t.Helper()
	for _, cfg := range configs {
		vclName := fmt.Sprintf("rb-vcl-%s-%s", cfg.Name, ts)
		if _, err := v.Adm("vcl.load", vclName, cfg.VclPath); err != nil {
			t.Fatalf("vcl.load %s: %v", cfg.Name, err)
		}
		if _, err := v.Adm("vcl.label", fmt.Sprintf("rb-label-%s-%s", cfg.Name, ts), vclName); err != nil {
			t.Fatalf("vcl.label %s: %v", cfg.Name, err)
		}
	}
}

// activateRoutingVCL writes the routing VCL to a file, loads it, and switches to it.
func activateRoutingVCL(t *testing.T, v vtest.Varnish, dir, content, ts string) {
	t.Helper()
	path := filepath.Join(dir, "routing.vcl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	name := "rb-routing-" + ts
	if _, err := v.Adm("vcl.load", name, path); err != nil {
		t.Fatalf("vcl.load routing: %v", err)
	}
	if _, err := v.Adm("vcl.use", name); err != nil {
		t.Fatalf("vcl.use routing: %v", err)
	}
}

// --- run() tests: VCL input (old parse-vcl) ---

func TestRunVCLToYAMLStdout(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com", "www.foo.com"})
	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-yamlfile", "-", fooVCL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"routes:", "name: foo_service", "foo.com", "www.foo.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in output, got:\n%s", want, out)
		}
	}
}

func TestRunVCLToYAMLFile(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	outPath := filepath.Join(dir, "routes.yaml")
	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-yamlfile", outPath, fooVCL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}
	if !strings.Contains(string(data), "foo_service") {
		t.Errorf("want foo_service in output file, got:\n%s", data)
	}
}

func TestRunVCLYAMLIncludesVCLPath(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-yamlfile", "-", fooVCL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "vclPath:") {
		t.Errorf("want vclPath: path in output, got:\n%s", stdout.String())
	}
}

func TestRunVCLDuplicateNamesRejected(t *testing.T) {
	dir := t.TempDir()
	a := writeTempVCL(t, dir, "foo_service", []string{"a.com"})
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	b := writeTempVCL(t, subdir, "foo_service", []string{"b.com"})
	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-yamlfile", "-", a, b}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("want exit 1 for duplicate name, got %d", code)
	}
}

func TestRunVCLDuplicateHostnamesRejected(t *testing.T) {
	dir := t.TempDir()
	a := writeTempVCL(t, dir, "foo", []string{"shared.com"})
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	b := writeTempVCL(t, subdir, "bar", []string{"shared.com"})
	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-yamlfile", "-", a, b}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("want exit 1 for duplicate hostname, got %d", code)
	}
	if !strings.Contains(stderr.String(), `overlaps with`) {
		t.Errorf("want duplicate hostname error, got: %s", stderr.String())
	}
}

// --- run() tests: YAML input (old generate) ---

func TestRunGenerateDryRun(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)

	vclPath := filepath.Join(dir, "routing.vcl")
	cmdfilePath := filepath.Join(dir, "cmdfile")

	var stdout, stderr strings.Builder
	code := run([]string{"-vclfile", "none", "-cmdfile", "none", routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	_ = vclPath
	_ = cmdfilePath
}

func TestRunGenerateNoVCL(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)

	cmdfilePath := filepath.Join(dir, "cmdfile")

	var stdout, stderr strings.Builder
	code := run([]string{"-vclfile", "none", "-cmdfile", cmdfilePath, routesYAML}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("want non-zero exit: -vclfile none with active -cmdfile is invalid")
	}
	if !strings.Contains(stderr.String(), "-vclfile") {
		t.Errorf("want error mentioning -vclfile, got: %s", stderr.String())
	}
}

func TestRunGenerateTestRoute(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com", "www.foo.com"})
	barVCL := writeTempVCL(t, dir, "bar_service", []string{"bar.com"})
	routesYAML := writeRoutesYAML(t, dir, "routes.yaml", fmt.Sprintf(
		"routes:\n  - name: foo_service\n    hostnames:\n      - foo.com\n      - www.foo.com\n    vclPath: %q\n  - name: bar_service\n    hostnames:\n      - bar.com\n    vclPath: %q\n",
		fooVCL, barVCL,
	))

	tests := []struct {
		url      string
		wantOut  string
		wantCode int
	}{
		{"http://foo.com/path", "foo_service", 0},
		{"http://www.foo.com:8080/", "foo_service", 0},
		{"http://bar.com/", "bar_service", 0},
		{"http://unknown.com/", "no route matched", 1},
	}

	for _, tt := range tests {
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-test-route", tt.url, routesYAML}, &stdout, &stderr)
		if code != tt.wantCode {
			t.Fatalf("url %s: want exit %d, got %d: %s", tt.url, tt.wantCode, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), tt.wantOut) {
			t.Errorf("url %s: want %q in output, got %q", tt.url, tt.wantOut, stdout.String())
		}
	}
}

func TestRunGenerateTestRouteWildcard(t *testing.T) {
	dir := t.TempDir()
	wcVCL := writeTempVCL(t, dir, "wc_service", []string{"*.example.com"})
	routesYAML := writeRoutesYAML(t, dir, "routes.yaml", fmt.Sprintf(
		"routes:\n  - name: wc_service\n    hostnames:\n      - \"*.example.com\"\n    vclPath: %q\n", wcVCL,
	))

	tests := []struct {
		url      string
		wantOut  string
		wantCode int
	}{
		{"http://foo.example.com/", "wc_service", 0},
		{"http://bar.example.com/", "wc_service", 0},
		{"http://example.com/", "no route matched", 1},
		{"http://foo.other.com/", "no route matched", 1},
	}

	for _, tt := range tests {
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-test-route", tt.url, routesYAML}, &stdout, &stderr)
		if code != tt.wantCode {
			t.Fatalf("url %s: want exit %d, got %d: %s", tt.url, tt.wantCode, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), tt.wantOut) {
			t.Errorf("url %s: want %q in output, got %q", tt.url, tt.wantOut, stdout.String())
		}
	}
}

func TestRunTestRouteNoFileWrite(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
	cmdfilePath := filepath.Join(dir, "cmdfile")
	vclfilePath := filepath.Join(dir, "routing.vcl")

	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", cmdfilePath, "-vclfile", vclfilePath, "-test-route", "http://foo.com/", routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(cmdfilePath); err == nil {
		t.Error("-test-route wrote cmdfile but should not write files")
	}
	if _, err := os.Stat(vclfilePath); err == nil {
		t.Error("-test-route wrote vclfile but should not write files")
	}
}

func TestRunGenerateVCLFile(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
	vclPath := filepath.Join(dir, "routing.vcl")

	var stdout, stderr strings.Builder
	code := run([]string{"-vclfile", vclPath, "-cmdfile", "none", routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	data, err := os.ReadFile(vclPath)
	if err != nil {
		t.Fatalf("routing.vcl not written: %v", err)
	}
	out := string(data)
	for _, want := range []string{"vcl 4.1;", "backend default none;", "foo_service", "foo.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in routing.vcl, got:\n%s", want, out)
		}
	}
}

func TestRunVCLFileStdoutRejected(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)

	var stdout, stderr strings.Builder
	code := run([]string{"-vclfile", "-", routesYAML}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("want non-zero exit for -vclfile - without -cmdfile none")
	}
	if !strings.Contains(stderr.String(), "-vclfile -") {
		t.Errorf("want error mentioning -vclfile -, got: %s", stderr.String())
	}
}

func TestRunVCLFileStdoutAllowed(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)

	var stdout, stderr strings.Builder
	code := run([]string{"-vclfile", "-", "-cmdfile", "none", routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"vcl 4.1;", "backend default none;", "foo_service"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in stdout, got:\n%s", want, out)
		}
	}
}

func TestRunGenerateStdoutCmdfile(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
	vclPath := filepath.Join(dir, "routing.vcl")

	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "-", "-vclfile", vclPath, routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"vcl.load rb-vcl-foo_service-", "vcl.label rb-label-foo_service-", "vcl.use rb-routing-"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in stdout, got:\n%s", want, out)
		}
	}
}

func TestRunGenerateNoVclPath(t *testing.T) {
	dir := t.TempDir()
	routesYAML := writeRoutesYAML(t, dir, "routes.yaml",
		"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
	vclPath := filepath.Join(dir, "routing.vcl")

	var stdout, stderr strings.Builder
	code := run([]string{"-cmdfile", "-", "-vclfile", vclPath, routesYAML}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("want non-zero exit for route missing vclPath:, got 0")
	}
	if !strings.Contains(stderr.String(), "vclPath") {
		t.Errorf("want error mentioning 'vclPath' in stderr, got: %s", stderr.String())
	}
}

// --- run() error path tests ---

func TestRunErrors(t *testing.T) {
	t.Run("no args", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := run([]string{}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("unknown extension", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := run([]string{"routes.txt"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("mixed vcl and yaml", func(t *testing.T) {
		dir := t.TempDir()
		vcl := writeTempVCL(t, dir, "foo", []string{"foo.com"})
		yaml := writeRoutesYAML(t, dir, "routes.yaml", "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{vcl, yaml}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1 for mixed input, got %d", code)
		}
	})

	t.Run("yaml input with -yamlfile rejected", func(t *testing.T) {
		dir := t.TempDir()
		yaml := writeRoutesYAML(t, dir, "routes.yaml", "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-yamlfile", "-", yaml}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
		if !strings.Contains(stderr.String(), "incompatible") {
			t.Errorf("want incompatible error, got: %s", stderr.String())
		}
	})

	t.Run("vclfile stdout without cmdfile none", func(t *testing.T) {
		dir := t.TempDir()
		yaml := writeRoutesYAML(t, dir, "routes.yaml", "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-vclfile", "-", yaml}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("bad routes file", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "bad.yaml", "routes: [unclosed\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("bad vcl file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.vcl")
		if err := os.WriteFile(path, []byte("vcl 4.1;\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("duplicate names in yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n  - name: foo\n    hostnames:\n      - bar.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("duplicate hostnames in yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - shared.com\n  - name: bar\n    hostnames:\n      - shared.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("test-route no hostname", func(t *testing.T) {
		dir := t.TempDir()
		fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
		path := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
		var stdout, stderr strings.Builder
		code := run([]string{"-cmdfile", "none", "-vclfile", "none", "-test-route", "http:///no-host", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
		if !strings.Contains(stderr.String(), "no hostname") {
			t.Errorf("want 'no hostname' error, got: %s", stderr.String())
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := run([]string{"-unknown-flag"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("want exit 2, got %d", code)
		}
	})

	t.Run("write vclfile fails", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-vclfile", "/nonexistent/dir/routing.vcl", "-cmdfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1 for bad vclfile path, got %d", code)
		}
	})

	t.Run("write cmdfile fails", func(t *testing.T) {
		dir := t.TempDir()
		vclPath := filepath.Join(dir, "routing.vcl")
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-vclfile", vclPath, "-cmdfile", "/nonexistent/dir/cmdfile", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1 for bad cmdfile path, got %d", code)
		}
	})
}

func TestRunReloadTimeoutFlag(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	path := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
	var stdout, stderr strings.Builder
	// A very short timeout still succeeds in connecting to a valid instance
	// but here we test that the flag is accepted and connect failure still
	// returns exit 1 (not a flag parse error).
	code := run([]string{"-reload", "-timeout", "1s", "-n", "nonexistent_instance_xyz",
		"-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("want exit 1 for bad instance with -timeout, got %d", code)
	}
	// Must be a connect error, not a flag parse error.
	if strings.Contains(stderr.String(), "flag") {
		t.Errorf("unexpected flag error: %s", stderr.String())
	}
}

func TestRunTimeoutWithoutReloadWarns(t *testing.T) {
	dir := t.TempDir()
	fooVCL := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	path := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooVCL)
	var stdout, stderr strings.Builder
	code := run([]string{"-timeout", "5s", "-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no effect") {
		t.Errorf("want 'no effect' warning in stderr, got: %s", stderr.String())
	}
}

// --- run() reload tests (requires varnishd in PATH) ---

func TestRunReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := rb.ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []rb.VCLConfig{fooCfg}

	routingVCL, err := rb.NewBuilder(rb.WithConstantNamer(ts)).BuildRoutingVCL(configs)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooPath)

	var stdout, stderr strings.Builder
	code := run([]string{"-reload", "-n", v.Name(), "-cmdfile", "none", "-vclfile", "none", routesYAML}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "reloaded 1 route(s)") {
		t.Errorf("want 'reloaded 1 route(s)' in stdout, got: %s", stdout.String())
	}
}

func TestRunReloadErrors(t *testing.T) {
	t.Run("no args", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := run([]string{"-reload"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("bad routes file", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "bad.yaml", "routes: [unclosed\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-reload", "-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("connect fails", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := run([]string{"-reload", "-n", "nonexistent_instance_xyz", "-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1 for bad instance, got %d", code)
		}
	})
}
