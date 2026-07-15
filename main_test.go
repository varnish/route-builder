package routebuilder

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
			name:  "valid name has hyphen",
			input: "name: my-service\nhostnames:\n  - foo.com",
			want:  VCLConfig{Name: "my-service", Hostnames: []string{"foo.com"}},
		},
		{
			name:    "invalid name starts with digit",
			input:   "name: 1bad\nhostnames:\n  - foo.com",
			wantErr: "not a valid route name",
		},
		{
			name:    "invalid name has slash",
			input:   "name: my/service\nhostnames:\n  - foo.com",
			wantErr: "not a valid route name",
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

func TestValidateConfigs(t *testing.T) {
	valid := []VCLConfig{{Name: "my-service", Hostnames: []string{"foo.com"}}}
	if err := ValidateConfigs(valid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	invalid := []VCLConfig{{Name: "bad/name", Hostnames: []string{"foo.com"}}}
	err := ValidateConfigs(invalid)
	if err == nil || !strings.Contains(err.Error(), "valid route name") {
		t.Fatalf("want route name validation error, got %v", err)
	}

	missingHostnames := []VCLConfig{{Name: "foo"}}
	err = ValidateConfigs(missingHostnames)
	if err == nil || !strings.Contains(err.Error(), "missing required field: hostnames") {
		t.Fatalf("want hostnames validation error, got %v", err)
	}
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
		got := FindRoute(tt.host, configs)
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
		if err := os.WriteFile(filepath.Join(dir, "foo.vcl"), []byte("vcl 4.1;"), 0644); err != nil {
			t.Fatal(err)
		}
		yaml := "routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: foo.vcl\n"
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := ParseRoutes(path)
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
		_, err := ParseRoutes(path)
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
		if err := os.MkdirAll(filepath.Dir(absVCL), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absVCL, []byte("vcl 4.1;"), 0644); err != nil {
			t.Fatal(err)
		}
		yaml := fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n", absVCL)
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := ParseRoutes(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if configs[0].VclPath != absVCL {
			t.Errorf("want VclPath %q, got %q", absVCL, configs[0].VclPath)
		}
	})

	t.Run("tls paths resolved relative to yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		vclFile := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
		yaml := fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n    tls:\n      - pem: cert.pem\n", vclFile)
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		configs, err := ParseRoutes(path)
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
		_, err := ParseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "no routes") {
			t.Fatalf("want 'no routes' error, got %v", err)
		}
	})

	t.Run("invalid route name", func(t *testing.T) {
		dir := t.TempDir()
		yaml := "routes:\n  - name: bad/name\n    hostnames:\n      - foo.com\n"
		path := writeRoutesYAML(t, dir, "routes.yaml", yaml)
		_, err := ParseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "valid route name") {
			t.Fatalf("want validation error, got %v", err)
		}
	})
}

// --- parseVCL / parseRoutes error tests ---

func TestParseVCLErrors(t *testing.T) {
	t.Run("file not found", func(t *testing.T) {
		_, err := ParseVCL("/nonexistent/path/foo.vcl")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "no_fm.vcl")
		if err := os.WriteFile(path, []byte("vcl 4.1;\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := ParseVCL(path)
		if err == nil || !strings.Contains(err.Error(), "/* routing") {
			t.Fatalf("want frontmatter error, got %v", err)
		}
	})

	t.Run("invalid yaml in frontmatter", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.vcl")
		if err := os.WriteFile(path, []byte("/* routing\nname: [unclosed\n*/\nvcl 4.1;\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := ParseVCL(path)
		if err == nil || !strings.Contains(err.Error(), "invalid YAML") {
			t.Fatalf("want YAML error, got %v", err)
		}
	})
}

func TestParseVCLRelativeTLSPaths(t *testing.T) {
	dir := t.TempDir()
	content := "/* routing\nname: foo\nhostnames:\n  - foo.com\ntls:\n  - cert: cert.pem\n    key: key.pem\n*/\nvcl 4.1;\n"
	path := filepath.Join(dir, "foo.vcl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseVCL(path)
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
		_, err := ParseRoutes("/nonexistent/routes.yaml")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad yaml syntax", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml", "routes: [unclosed\n")
		_, err := ParseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "invalid YAML") {
			t.Fatalf("want YAML error, got %v", err)
		}
	})

	t.Run("vcl file not found", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: nonexistent.vcl\n")
		_, err := ParseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "vclPath:") {
			t.Fatalf("want vclPath error, got %v", err)
		}
	})

	t.Run("tls file not found", func(t *testing.T) {
		dir := t.TempDir()
		vclFile := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
		path := writeRoutesYAML(t, dir, "routes.yaml",
			fmt.Sprintf("routes:\n  - name: foo\n    hostnames:\n      - foo.com\n    vclPath: %q\n    tls:\n      - pem: nonexistent.pem\n", vclFile))
		_, err := ParseRoutes(path)
		if err == nil || !strings.Contains(err.Error(), "tls:") {
			t.Fatalf("want tls error, got %v", err)
		}
	})
}

func TestWriteFileAtomicError(t *testing.T) {
	err := WriteFileAtomic("/nonexistent/dir/that/does/not/exist/file.txt", "content")
	if err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
}
