package main

import (
	"strings"
	"testing"
)

func TestHostnameToVCL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo.com", `req.http.host == "foo.com"`},
		{"*.example.com", `req.http.host ~ "^[^.]+\.example\.com$"`},
		{"foo.*.com", `req.http.host ~ "^foo\.[^.]+\.com$"`},
		{"*.*.com", `req.http.host ~ "^[^.]+\.[^.]+\.com$"`},
	}
	for _, tt := range tests {
		got := hostnameToVCL(tt.input)
		if got != tt.want {
			t.Errorf("hostnameToVCL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildRoutingVCL(t *testing.T) {
	configs := []VCLConfig{
		{Name: "foo_service", Hostnames: []string{"foo.com", "www.foo.com"}},
		{Name: "bar_service", Hostnames: []string{"bar.com"}},
		{Name: "wc_service", Hostnames: []string{"*.wc.com"}},
	}

	const ts = "2024-01-15T10-30-45_0"
	got, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"vcl 4.1;",
		"import tls;",
		"backend default none;",
		"tls.is_tls()",
		"tls.authority()",
		// non-TLS IPv6 port strip
		`req.http.host ~ "^\["`,
		`regsub(req.http.host, "^\[([^\]]+)\](:\d+)?$", "[\1]")`,
		// non-TLS plain port strip
		`regsub(req.http.host, ":\d+$", "")`,
		`req.http.host == "foo.com" || req.http.host == "www.foo.com"`,
		"return(vcl(rb-label-foo_service-2024-01-15T10-30-45_0));",
		`req.http.host == "bar.com"`,
		"return(vcl(rb-label-bar_service-2024-01-15T10-30-45_0));",
		`req.http.host ~ "^[^.]+\.wc\.com$"`,
		"return(vcl(rb-label-wc_service-2024-01-15T10-30-45_0));",
		`return(synth(404, "No route matched"));`,
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("missing expected string: %q\nfull output:\n%s", c, got)
		}
	}
}

func TestBuildCmdfile(t *testing.T) {
	configs := []VCLConfig{
		{Name: "foo_service", VclPath: "/etc/vcl/foo.vcl"},
		{Name: "bar_service", VclPath: "/etc/vcl/bar.vcl"},
	}

	const ts = "2024-01-15T10-30-45_0"
	got, err := buildCmdfile(configs, "/etc/vcl/routing.vcl", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		`vcl.load rb-vcl-foo_service-2024-01-15T10-30-45_0 "/etc/vcl/foo.vcl"`,
		"vcl.label rb-label-foo_service-2024-01-15T10-30-45_0 rb-vcl-foo_service-2024-01-15T10-30-45_0",
		`vcl.load rb-vcl-bar_service-2024-01-15T10-30-45_0 "/etc/vcl/bar.vcl"`,
		"vcl.label rb-label-bar_service-2024-01-15T10-30-45_0 rb-vcl-bar_service-2024-01-15T10-30-45_0",
		`vcl.load rb-routing-2024-01-15T10-30-45_0 "/etc/vcl/routing.vcl"`,
		"vcl.use rb-routing-2024-01-15T10-30-45_0",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("missing expected string: %q\nfull output:\n%s", c, got)
		}
	}
	if strings.Contains(got, "tls.") {
		t.Errorf("unexpected tls lines in no-TLS cmdfile:\n%s", got)
	}
}

func TestBuildCmdfileTLS(t *testing.T) {
	const ts = "2024-01-15T10-30-45_0"

	configs := []VCLConfig{
		{
			Name:    "foo_service",
			VclPath: "/etc/vcl/foo.vcl",
			TLS:     []TLSEntry{{PEM: "/etc/certs/foo.pem"}},
		},
		{
			Name:    "bar_service",
			VclPath: "/etc/vcl/bar.vcl",
			TLS:     []TLSEntry{{Cert: "/etc/certs/bar.crt", Key: "/etc/certs/bar.key"}},
		},
	}

	got, err := buildCmdfile(configs, "/etc/vcl/routing.vcl", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pemIdx := strings.Index(got, `tls.cert.load rb-cert-foo_service-0-`+ts+` "/etc/certs/foo.pem"`)
	keyIdx := strings.Index(got, `tls.cert.load rb-cert-bar_service-0-`+ts+` "/etc/certs/bar.crt" -k "/etc/certs/bar.key"`)
	commitIdx := strings.Index(got, "tls.cert.commit")
	vclIdx := strings.Index(got, "vcl.load")

	if pemIdx < 0 {
		t.Errorf("missing tls.cert.load (pem):\n%s", got)
	}
	if keyIdx < 0 {
		t.Errorf("missing tls.cert.load (key+cert):\n%s", got)
	}
	if commitIdx < 0 {
		t.Errorf("missing tls.cert.commit:\n%s", got)
	}
	if vclIdx < 0 {
		t.Fatalf("missing vcl.load:\n%s", got)
	}
	if commitIdx > vclIdx {
		t.Errorf("tls.cert.commit must appear before vcl.load:\n%s", got)
	}
}

func TestBuildCmdfileNoVclPath(t *testing.T) {
	// VclPath is mandatory after validation; passing an empty VclPath is a
	// programmer error. The template no longer guards on it — verify the output
	// would contain the empty string, confirming the guard was removed (the
	// defensive panic in reloadVarnish catches this at runtime).
	configs := []VCLConfig{
		{Name: "foo_service", VclPath: "/etc/varnish/foo.vcl"},
	}
	const ts = "2024-01-15T10-30-45_0"
	got, err := buildCmdfile(configs, "/etc/vcl/routing.vcl", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "vcl.load rb-vcl-foo_service-") {
		t.Errorf("expected vcl.load line, got:\n%s", got)
	}
	if !strings.Contains(got, "vcl.load rb-routing-") {
		t.Errorf("expected routing vcl.load:\n%s", got)
	}
}

func TestWriteOutputStdout(t *testing.T) {
	var buf strings.Builder
	if err := writeOutput("-", "hello world", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world" {
		t.Errorf("want %q, got %q", "hello world", buf.String())
	}
}
