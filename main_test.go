package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestExtractFrontMatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{
			name:  "valid",
			input: "/* routing\nname: foo\nhostnames:\n  - foo.com\n*/\nvcl 4.1;",
			want:  "name: foo\nhostnames:\n  - foo.com",
		},
		{
			name:    "wrong first line",
			input:   "vcl 4.1;\n/* routing\n*/",
			wantErr: "first line must be exactly `/* routing`",
		},
		{
			name:    "unclosed block",
			input:   "/* routing\nname: foo\n",
			wantErr: "routing block not closed with `*/`",
		},
		{
			name:  "empty front matter",
			input: "/* routing\n*/",
			want:  "",
		},
		{
			name:  "crlf line endings",
			input: "/* routing\r\nname: foo\r\nhostnames:\r\n  - foo.com\r\n*/\r\nvcl 4.1;",
			want:  "name: foo\nhostnames:\n  - foo.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractFrontMatter(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestUnmarshalConfig(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    VCLConfig
		wantErr string
	}{
		{
			name:  "valid single host",
			input: "name: foo_service\nhostnames:\n  - foo.com",
			want:  VCLConfig{Name: "foo_service", Hostnames: []string{"foo.com"}},
		},
		{
			name:  "valid multiple hosts",
			input: "name: svc\nhostnames:\n  - a.com\n  - b.com",
			want:  VCLConfig{Name: "svc", Hostnames: []string{"a.com", "b.com"}},
		},
		{
			name:    "missing name",
			input:   "hostnames:\n  - foo.com",
			wantErr: "missing required field: name",
		},
		{
			name:    "invalid name starts with digit",
			input:   "name: 1bad\nhostnames:\n  - foo.com",
			wantErr: "not a valid VCL label name",
		},
		{
			name:    "invalid name has hyphen",
			input:   "name: my-service\nhostnames:\n  - foo.com",
			wantErr: "not a valid VCL label name",
		},
		{
			name:    "reserved name routing",
			input:   "name: routing\nhostnames:\n  - foo.com",
			wantErr: "is reserved",
		},
		{
			name:    "name too long",
			input:   "name: " + strings.Repeat("a", 65) + "\nhostnames:\n  - foo.com",
			wantErr: "exceeds 64-character limit",
		},
		{
			name:    "missing hostnames",
			input:   "name: foo",
			wantErr: "missing required field: hostnames",
		},
		{
			name:    "invalid yaml",
			input:   "name: [unclosed",
			wantErr: "invalid YAML front matter",
		},
		{
			name:  "valid tls pem",
			input: "name: foo\nhostnames:\n  - foo.com\ntls:\n  - pem: /cert.pem",
			want:  VCLConfig{Name: "foo", Hostnames: []string{"foo.com"}, TLS: []TLSEntry{{PEM: "/cert.pem"}}},
		},
		{
			name:  "valid tls key+cert",
			input: "name: foo\nhostnames:\n  - foo.com\ntls:\n  - key: /key.pem\n    cert: /cert.pem",
			want:  VCLConfig{Name: "foo", Hostnames: []string{"foo.com"}, TLS: []TLSEntry{{Key: "/key.pem", Cert: "/cert.pem"}}},
		},
		{
			name:    "tls pem mixed with key",
			input:   "name: foo\nhostnames:\n  - foo.com\ntls:\n  - pem: /cert.pem\n    key: /key.pem",
			wantErr: "cannot mix pem with key/cert",
		},
		{
			name:    "tls key without cert",
			input:   "name: foo\nhostnames:\n  - foo.com\ntls:\n  - key: /key.pem",
			wantErr: "key and cert must both be set",
		},
		{
			name:    "tls empty entry",
			input:   "name: foo\nhostnames:\n  - foo.com\ntls:\n  - {}",
			wantErr: "must specify pem or key+cert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unmarshalConfig(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name: want %q, got %q", tt.want.Name, got.Name)
			}
			if len(got.Hostnames) != len(tt.want.Hostnames) {
				t.Fatalf("Hostnames len: want %d, got %d", len(tt.want.Hostnames), len(got.Hostnames))
			}
			for i, h := range tt.want.Hostnames {
				if got.Hostnames[i] != h {
					t.Errorf("Hostnames[%d]: want %q, got %q", i, h, got.Hostnames[i])
				}
			}
			if len(got.TLS) != len(tt.want.TLS) {
				t.Fatalf("TLS len: want %d, got %d", len(tt.want.TLS), len(got.TLS))
			}
			for i, e := range tt.want.TLS {
				if got.TLS[i] != e {
					t.Errorf("TLS[%d]: want %+v, got %+v", i, e, got.TLS[i])
				}
			}
		})
	}
}

func TestCheckDuplicateNames(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl"},
			{Name: "bar", SourceFile: "/b.vcl"},
		}
		if err := checkDuplicateNames(configs); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl"},
			{Name: "foo", SourceFile: "/b.vcl"},
		}
		err := checkDuplicateNames(configs)
		if err == nil || !strings.Contains(err.Error(), `duplicate name "foo"`) {
			t.Fatalf("want duplicate error, got %v", err)
		}
	})
}

func TestCheckDuplicateHostnames(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"foo.com", "www.foo.com"}},
			{Name: "bar", SourceFile: "/b.vcl", Hostnames: []string{"bar.com"}},
		}
		if err := checkDuplicateHostnames(configs); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate across configs", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"shared.com"}},
			{Name: "bar", SourceFile: "/b.vcl", Hostnames: []string{"shared.com"}},
		}
		err := checkDuplicateHostnames(configs)
		if err == nil || !strings.Contains(err.Error(), `overlaps with`) {
			t.Fatalf("want duplicate hostname error, got %v", err)
		}
	})

	t.Run("duplicate within same config", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"foo.com", "foo.com"}},
		}
		err := checkDuplicateHostnames(configs)
		if err == nil || !strings.Contains(err.Error(), `overlaps with`) {
			t.Fatalf("want overlap error, got %v", err)
		}
	})

	t.Run("wildcard overlaps exact", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"*.example.com"}},
			{Name: "bar", SourceFile: "/b.vcl", Hostnames: []string{"sub.example.com"}},
		}
		err := checkDuplicateHostnames(configs)
		if err == nil || !strings.Contains(err.Error(), `overlaps with`) {
			t.Fatalf("want overlap error, got %v", err)
		}
	})

	t.Run("wildcards with different fixed segments do not overlap", func(t *testing.T) {
		configs := []VCLConfig{
			{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"*.example.com"}},
			{Name: "bar", SourceFile: "/b.vcl", Hostnames: []string{"*.bar.com"}},
		}
		if err := checkDuplicateHostnames(configs); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestValidateHostname(t *testing.T) {
	valid := []string{
		"*.example.com",
		"foo.*.com",
		"*.*.com",
		"*",
		"foo.com",
	}
	for _, h := range valid {
		if err := validateHostname(h); err != nil {
			t.Errorf("hostname %q: want ok, got %v", h, err)
		}
	}

	invalid := []string{
		"**.example.com",
		"exampl*.com",
		"ex*mple.com",
		"foo.*x.com",
		"foo..com",
		".foo.com",
		"foo.com.",
	}
	for _, h := range invalid {
		if err := validateHostname(h); err == nil {
			t.Errorf("hostname %q: want error, got nil", h)
		}
	}
}

func TestHostnamesOverlap(t *testing.T) {
	tests := []struct {
		a, b    string
		overlap bool
	}{
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "*.bar.com", false},
		{"foo.example.com", "foo.example.com", true},
		{"*.*.com", "foo.bar.com", true},
		{"foo.example.com", "foo.example.org", false},
		{"foo.com", "foo.com.au", false},
		{"*.example.com", "*.example.org", false},
		{"*.example.com", "*.example.com", true},
	}
	for _, tt := range tests {
		got := hostnamesOverlap(tt.a, tt.b)
		if got != tt.overlap {
			t.Errorf("hostnamesOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.overlap)
		}
		// overlap must be symmetric
		got2 := hostnamesOverlap(tt.b, tt.a)
		if got2 != tt.overlap {
			t.Errorf("hostnamesOverlap(%q, %q) = %v, want %v (symmetry)", tt.b, tt.a, got2, tt.overlap)
		}
	}
}

func TestFindRoute(t *testing.T) {
	configs := []VCLConfig{
		{Name: "foo", SourceFile: "/a.vcl", Hostnames: []string{"foo.com", "www.foo.com"}},
		{Name: "bar", SourceFile: "/b.vcl", Hostnames: []string{"bar.com"}},
	}

	tests := []struct {
		host string
		want string
	}{
		{"foo.com", "foo"},
		{"www.foo.com", "foo"},
		{"bar.com", "bar"},
		{"unknown.com", ""},
	}

	for _, tt := range tests {
		got := findRoute(tt.host, configs)
		if tt.want == "" {
			if got != nil {
				t.Errorf("host %s: want nil, got %v", tt.host, got)
			}
		} else if got == nil || got.Name != tt.want {
			t.Errorf("host %s: want %q, got %v", tt.host, tt.want, got)
		}
	}
}

func TestParseRoutes(t *testing.T) {
	t.Run("valid with vcl path", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "foo.vcl"), []byte("vcl 4.1;"), 0644)
		yaml := "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: foo.vcl\n"
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := parseRoutes(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(configs) != 1 {
			t.Fatalf("want 1 config, got %d", len(configs))
		}
		if configs[0].Name != "foo" {
			t.Errorf("want name %q, got %q", "foo", configs[0].Name)
		}
		wantVclPath := filepath.Join(dir, "foo.vcl")
		if configs[0].VclPath != wantVclPath {
			t.Errorf("want VclPath %q, got %q", wantVclPath, configs[0].VclPath)
		}
		if configs[0].SourceFile != path {
			t.Errorf("want SourceFile %q, got %q", path, configs[0].SourceFile)
		}
	})

	t.Run("valid without vcl path", func(t *testing.T) {
		dir := t.TempDir()
		yaml := "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n"
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		_, err := parseRoutes(path)
		if err == nil {
			t.Fatal("want error for route missing vclPath:, got nil")
		}
		if !strings.Contains(err.Error(), "vclPath") {
			t.Errorf("want error mentioning 'vclPath', got: %v", err)
		}
	})

	t.Run("absolute vcl path preserved", func(t *testing.T) {
		dir := t.TempDir()
		absVCL := filepath.Join(dir, "sub", "foo.vcl")
		os.MkdirAll(filepath.Dir(absVCL), 0755)
		os.WriteFile(absVCL, []byte("vcl 4.1;"), 0644)
		yaml := fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n", absVCL)
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := parseRoutes(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if configs[0].VclPath != absVCL {
			t.Errorf("want VclPath %q, got %q", absVCL, configs[0].VclPath)
		}
	})

	t.Run("tls paths resolved relative to yaml", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "cert.pem"), []byte(""), 0644)
		vclFile := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
		yaml := fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n    tls:\n      - pem: cert.pem\n", vclFile)
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := parseRoutes(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(dir, "cert.pem")
		if configs[0].TLS[0].PEM != want {
			t.Errorf("want TLS.PEM %q, got %q", want, configs[0].TLS[0].PEM)
		}
	})

	t.Run("empty routes", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml", "routes: []\n")
		_, err := parseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "no routes") {
			t.Fatalf("want 'no routes' error, got %v", err)
		}
	})

	t.Run("invalid route name", func(t *testing.T) {
		dir := t.TempDir()
		yaml := "routes:\n  - name: bad-name\n    hostnames:\n      - foo.com\n"
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		_, err := parseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "valid VCL label name") {
			t.Fatalf("want validation error, got %v", err)
		}
	})
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
	os.MkdirAll(subdir, 0755)
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
	os.MkdirAll(subdir, 0755)
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
		os.WriteFile(path, []byte("vcl 4.1;\n"), 0644)
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

// --- parseVCL / parseRoutes error tests ---

func TestParseVCLErrors(t *testing.T) {
	t.Run("file not found", func(t *testing.T) {
		_, err := parseVCL("/nonexistent/path/foo.vcl")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "no_fm.vcl")
		os.WriteFile(path, []byte("vcl 4.1;\n"), 0644)
		_, err := parseVCL(path)
		if err == nil || !strings.Contains(err.Error(), "/* routing") {
			t.Fatalf("want frontmatter error, got %v", err)
		}
	})

	t.Run("invalid yaml in frontmatter", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.vcl")
		os.WriteFile(path, []byte("/* routing\nname: [unclosed\n*/\nvcl 4.1;\n"), 0644)
		_, err := parseVCL(path)
		if err == nil || !strings.Contains(err.Error(), "invalid YAML") {
			t.Fatalf("want YAML error, got %v", err)
		}
	})
}

func TestParseVCLRelativeTLSPaths(t *testing.T) {
	dir := t.TempDir()
	content := "/* routing\nname: foo\nhostnames:\n  - foo.com\ntls:\n  - cert: cert.pem\n    key: key.pem\n*/\nvcl 4.1;\n"
	path := filepath.Join(dir, "foo.vcl")
	os.WriteFile(path, []byte(content), 0644)
	cfg, err := parseVCL(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantCert := filepath.Join(dir, "cert.pem")
	wantKey := filepath.Join(dir, "key.pem")
	if cfg.TLS[0].Cert != wantCert {
		t.Errorf("Cert: want %q, got %q", wantCert, cfg.TLS[0].Cert)
	}
	if cfg.TLS[0].Key != wantKey {
		t.Errorf("Key: want %q, got %q", wantKey, cfg.TLS[0].Key)
	}
}

func TestParseRoutesErrors(t *testing.T) {
	t.Run("file not found", func(t *testing.T) {
		_, err := parseRoutes("/nonexistent/routes.yaml")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad yaml syntax", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml", "routes: [unclosed\n")
		_, err := parseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "invalid YAML") {
			t.Fatalf("want YAML error, got %v", err)
		}
	})

	t.Run("vcl file not found", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: nonexistent.vcl\n")
		_, err := parseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "vclPath:") {
			t.Fatalf("want vclPath error, got %v", err)
		}
	})

	t.Run("tls file not found", func(t *testing.T) {
		dir := t.TempDir()
		vclFile := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
		path := writeRoutesYAML(t, dir, "routes.yaml",
			fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n    tls:\n      - pem: nonexistent.pem\n", vclFile))
		_, err := parseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "tls:") {
			t.Fatalf("want tls error, got %v", err)
		}
	})
}

func TestWriteFileAtomicError(t *testing.T) {
	err := writeFileAtomic("/nonexistent/dir/that/does/not/exist/file.txt", "content")
	if err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
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
