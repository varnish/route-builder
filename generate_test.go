package routebuilder

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
		{Name: "foo-service", Hostnames: []string{"foo.com", "www.foo.com"}},
		{Name: "bar_service", Hostnames: []string{"bar.com"}},
		{Name: "wc_service", Hostnames: []string{"*.wc.com"}},
	}

	const ts = "2024-01-15T10-30-45_0"
	got, err := BuildRoutingVCL(configs, ts)
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
		"return(vcl(rb-label-foo-service-2024-01-15T10-30-45_0));",
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
	got, err := BuildCmdfile(configs, "/etc/vcl/routing.vcl", ts)
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

func TestBuildCmdfilePlanWithExisting(t *testing.T) {
	configs := []VCLConfig{
		{Name: "foo_service", VclPath: "/etc/vcl/foo.vcl"},
		{Name: "bar_service", VclPath: "/etc/vcl/bar.vcl"},
	}
	const ts = "2024-01-15T10-30-45_0"
	existing := map[string]bool{
		RouteVCLName(configs[0], ts):   true,
		RouteLabelName(configs[0], ts): true,
		RoutingVCLName(ts):             true,
	}

	plan, err := BuildCmdfilePlan(configs, "/etc/vcl/routing.vcl", ts, CmdfileOptions{ExistingVCLNames: existing})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := plan.String()
	for _, notWant := range []string{
		`vcl.load rb-vcl-foo_service-2024-01-15T10-30-45_0`,
		`vcl.label rb-label-foo_service-2024-01-15T10-30-45_0`,
		`vcl.load rb-routing-2024-01-15T10-30-45_0`,
	} {
		if strings.Contains(got, notWant) {
			t.Errorf("did not expect %q in output:\n%s", notWant, got)
		}
	}
	for _, want := range []string{
		`vcl.load rb-vcl-bar_service-2024-01-15T10-30-45_0 "/etc/vcl/bar.vcl"`,
		`vcl.label rb-label-bar_service-2024-01-15T10-30-45_0 rb-vcl-bar_service-2024-01-15T10-30-45_0`,
		`vcl.use rb-routing-2024-01-15T10-30-45_0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in output:\n%s", want, got)
		}
	}
}

func TestBuildCmdfileWithExisting(t *testing.T) {
	configs := []VCLConfig{{Name: "foo_service", VclPath: "/etc/vcl/foo.vcl"}}
	const ts = "2024-01-15T10-30-45_0"
	existing := map[string]bool{RouteVCLName(configs[0], ts): true}
	got, err := BuildCmdfileWithExisting(configs, "/etc/vcl/routing.vcl", ts, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `vcl.load rb-vcl-foo_service-2024-01-15T10-30-45_0`) {
		t.Errorf("existing vcl should not be loaded:\n%s", got)
	}
	if !strings.Contains(got, `vcl.label rb-label-foo_service-2024-01-15T10-30-45_0 rb-vcl-foo_service-2024-01-15T10-30-45_0`) {
		t.Errorf("missing label command for existing vcl:\n%s", got)
	}
}

func TestCleanupCommandsFromNames(t *testing.T) {
	names := []string{
		"rb-vcl-stale-b",
		"rb-label-stale-a",
		"rb-routing-old",
		"rb-vcl-keep",
		"rb-label-keep",
		"rb-routing-current",
		"unrelated",
	}
	keep := map[string]bool{
		"rb-vcl-keep":        true,
		"rb-label-keep":      true,
		"rb-routing-current": true,
	}
	got := strings.Join(CleanupCommandsFromNames(names, keep), "\n")
	want := strings.Join([]string{
		"vcl.discard rb-routing-old",
		"vcl.discard rb-label-stale-a",
		"vcl.discard rb-vcl-stale-b",
	}, "\n")
	if got != want {
		t.Fatalf("want:\n%s\ngot:\n%s", want, got)
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

	got, err := BuildCmdfile(configs, "/etc/vcl/routing.vcl", ts)
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
	configs := []VCLConfig{{Name: "foo_service"}}
	_, err := BuildCmdfile(configs, "/etc/vcl/routing.vcl", "2024-01-15T10-30-45_0")
	if err == nil || !strings.Contains(err.Error(), "vclPath") {
		t.Fatalf("want vclPath validation error, got %v", err)
	}
}

func TestBuildGenerationValidation(t *testing.T) {
	_, err := BuildRoutingVCL([]VCLConfig{{Name: "bad/name", Hostnames: []string{"foo.com"}}}, "2024-01-15T10-30-45_0")
	if err == nil || !strings.Contains(err.Error(), "valid route name") {
		t.Fatalf("want route name validation error, got %v", err)
	}

	_, err = BuildRoutingVCL([]VCLConfig{{Name: "foo", Hostnames: []string{"foo.com"}}}, "bad timestamp")
	if err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("want timestamp validation error, got %v", err)
	}

	_, err = BuildCmdfile([]VCLConfig{{Name: "foo", VclPath: "/etc/vcl/foo.vcl", TLS: []TLSEntry{{}}}}, "/etc/vcl/routing.vcl", "2024-01-15T10-30-45_0")
	if err == nil || !strings.Contains(err.Error(), "must specify pem or key+cert") {
		t.Fatalf("want TLS validation error, got %v", err)
	}
}

func TestWriteOutputStdout(t *testing.T) {
	var buf strings.Builder
	if err := WriteOutput("-", "hello world", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world" {
		t.Errorf("want %q, got %q", "hello world", buf.String())
	}
}
