package routebuilder

import (
	"fmt"
	"os"
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
	got, err := NewBuilder(WithConstantNamer(ts)).BuildRoutingVCL(configs)
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
		{Name: "foo_service", Hostnames: []string{"foo.com"}, VclPath: "/etc/vcl/foo.vcl"},
		{Name: "bar_service", Hostnames: []string{"bar.com"}, VclPath: "/etc/vcl/bar.vcl"},
	}

	const ts = "2024-01-15T10-30-45_0"
	plan, err := NewBuilder(WithConstantNamer(ts)).BuildCmdfilePlan(configs, "/etc/vcl/routing.vcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := plan.String()

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

func TestBuildRoutingVCLMD5Names(t *testing.T) {
	dir := t.TempDir()
	path := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	cfg, err := ParseVCL(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	suffix := contentHash(data)

	got, err := NewBuilder(WithMD5Namer()).BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "return(vcl(rb-label-foo_service-" + suffix + "));"
	if !strings.Contains(got, want) {
		t.Fatalf("want %q in routing VCL:\n%s", want, got)
	}
}

func TestBuildCmdfilePlanMD5Names(t *testing.T) {
	dir := t.TempDir()
	path := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	cfg, err := ParseVCL(path)
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(WithMD5Namer())
	routingVCL, err := builder.BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	routingPath := writeRoutesYAML(t, dir, "routing.vcl", routingVCL)
	routeData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routeSuffix := contentHash(routeData)
	routingSuffix := contentHash([]byte(routingVCL))

	first, err := builder.BuildCmdfilePlan([]VCLConfig{cfg}, routingPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := NewBuilder(WithMD5Namer()).BuildCmdfilePlan([]VCLConfig{cfg}, routingPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("MD5 cmdfile changed across builders:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
	for _, want := range []string{
		`vcl.load rb-vcl-foo_service-` + routeSuffix + ` "` + path + `"`,
		`vcl.label rb-label-foo_service-` + routeSuffix + ` rb-vcl-foo_service-` + routeSuffix,
		`vcl.load rb-routing-` + routingSuffix + ` "` + routingPath + `"`,
		`vcl.use rb-routing-` + routingSuffix,
	} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("want %q in MD5 cmdfile:\n%s", want, first.String())
		}
	}
}

func TestManagedVCLNames(t *testing.T) {
	dir := t.TempDir()
	path := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	cfg, err := ParseVCL(path)
	if err != nil {
		t.Fatal(err)
	}
	routingContent := []byte("routing content")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routeSuffix := contentHash(data)
	routingSuffix := contentHash(routingContent)

	got, err := NewBuilder(WithMD5Namer()).ManagedVCLNames([]VCLConfig{cfg}, routingContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"rb-routing-" + routingSuffix,
		"rb-vcl-foo_service-" + routeSuffix,
		"rb-label-foo_service-" + routeSuffix,
	} {
		if !got[want] {
			t.Fatalf("missing %q in keep set: %#v", want, got)
		}
	}
}

func TestBuildCmdfilePlanMD5NamesWithExisting(t *testing.T) {
	dir := t.TempDir()
	path := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	cfg, err := ParseVCL(path)
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(WithMD5Namer())
	routingVCL, err := builder.BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	routingPath := writeRoutesYAML(t, dir, "routing.vcl", routingVCL)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routeSuffix := contentHash(data)
	existing := map[string]bool{
		"rb-vcl-foo_service-" + routeSuffix:   true,
		"rb-label-foo_service-" + routeSuffix: true,
	}

	plan, err := builder.BuildCmdfilePlan([]VCLConfig{cfg}, routingPath, WithExistingVCLNames(mapKeys(existing)...))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := plan.String()
	if strings.Contains(got, "vcl.load rb-vcl-foo_service-") || strings.Contains(got, "vcl.label rb-label-foo_service-") {
		t.Fatalf("existing MD5 route objects should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "vcl.use rb-routing-") {
		t.Fatalf("missing vcl.use command:\n%s", got)
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func TestBuildCmdfilePlanWithExisting(t *testing.T) {
	configs := []VCLConfig{
		{Name: "foo_service", Hostnames: []string{"foo.com"}, VclPath: "/etc/vcl/foo.vcl"},
		{Name: "bar_service", Hostnames: []string{"bar.com"}, VclPath: "/etc/vcl/bar.vcl"},
	}
	const ts = "2024-01-15T10-30-45_0"
	existing := map[string]bool{
		"rb-vcl-foo_service-" + ts:   true,
		"rb-label-foo_service-" + ts: true,
		"rb-routing-" + ts:           true,
	}

	plan, err := NewBuilder(WithConstantNamer(ts)).BuildCmdfilePlan(configs, "/etc/vcl/routing.vcl", WithExistingVCLNames(mapKeys(existing)...))
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
			Name:      "foo_service",
			Hostnames: []string{"foo.com"},
			VclPath:   "/etc/vcl/foo.vcl",
			TLS:       []TLSEntry{{PEM: "/etc/certs/foo.pem"}},
		},
		{
			Name:      "bar_service",
			Hostnames: []string{"bar.com"},
			VclPath:   "/etc/vcl/bar.vcl",
			TLS:       []TLSEntry{{Cert: "/etc/certs/bar.crt", Key: "/etc/certs/bar.key"}},
		},
	}

	plan, err := NewBuilder(WithConstantNamer(ts)).BuildCmdfilePlan(configs, "/etc/vcl/routing.vcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := plan.String()

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

func TestBuildCmdfileTLSMD5Names(t *testing.T) {
	dir := t.TempDir()
	vclPath := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	pemPath := writeRoutesYAML(t, dir, "foo.pem", "pem data")
	cfg := VCLConfig{Name: "foo_service", Hostnames: []string{"foo.com"}, VclPath: vclPath, TLS: []TLSEntry{{PEM: pemPath}}}
	routingVCL, err := NewBuilder(WithMD5Namer()).BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	routingPath := writeRoutesYAML(t, dir, "routing.vcl", routingVCL)

	plan, err := NewBuilder(WithMD5Namer()).BuildCmdfilePlan([]VCLConfig{cfg}, routingPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	certSuffix := contentHash([]byte("pem data"))
	want := `tls.cert.load rb-cert-foo_service-0-` + certSuffix + ` "` + pemPath + `"`
	if !strings.Contains(plan.String(), want) {
		t.Fatalf("want %q in cmdfile:\n%s", want, plan.String())
	}
}

func TestBuildCmdfileNoVclPath(t *testing.T) {
	configs := []VCLConfig{{Name: "foo_service"}}
	_, err := NewBuilder().BuildCmdfilePlan(configs, "/etc/vcl/routing.vcl")
	if err == nil || !strings.Contains(err.Error(), "vclPath") {
		t.Fatalf("want vclPath validation error, got %v", err)
	}
}

func TestBuildGenerationValidation(t *testing.T) {
	_, err := NewBuilder().BuildRoutingVCL([]VCLConfig{{Name: "bad/name", Hostnames: []string{"foo.com"}}})
	if err == nil || !strings.Contains(err.Error(), "valid route name") {
		t.Fatalf("want route name validation error, got %v", err)
	}

	_, err = NewBuilder(WithConstantNamer("bad timestamp")).BuildRoutingVCL([]VCLConfig{{Name: "foo", Hostnames: []string{"foo.com"}}})
	if err == nil || !strings.Contains(err.Error(), "suffix") {
		t.Fatalf("want suffix validation error, got %v", err)
	}

	_, err = NewBuilder().BuildCmdfilePlan([]VCLConfig{{Name: "foo", Hostnames: []string{"foo.com"}, VclPath: "/etc/vcl/foo.vcl", TLS: []TLSEntry{{}}}}, "/etc/vcl/routing.vcl")
	if err == nil || !strings.Contains(err.Error(), "must specify pem or key+cert") {
		t.Fatalf("want TLS validation error, got %v", err)
	}
}

func TestCustomEntityNamer(t *testing.T) {
	namer := entityNamerFunc(func(prefix string, content []byte) (string, error) {
		return prefix + "custom", nil
	})
	got, err := NewBuilder(WithEntityNamer(namer)).BuildRoutingVCL([]VCLConfig{{Name: "foo", Hostnames: []string{"foo.com"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "return(vcl(rb-label-foo-custom));") {
		t.Fatalf("custom namer not used:\n%s", got)
	}
}

func TestNewBuilderDefault(t *testing.T) {
	plan, err := NewBuilder().BuildCmdfilePlan([]VCLConfig{{Name: "foo", Hostnames: []string{"foo.com"}, VclPath: "/etc/vcl/foo.vcl"}}, "/etc/vcl/routing.vcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(plan.String(), "rb-vcl-foo-") || strings.Contains(plan.String(), "rb-vcl-foo- ") {
		t.Fatalf("default builder did not produce a suffixed VCL name:\n%s", plan.String())
	}
}

func TestMD5NameChangesWhenVCLContentChanges(t *testing.T) {
	dir := t.TempDir()
	path := writeTempVCL(t, dir, "foo_service", []string{"foo.com"})
	cfg, err := ParseVCL(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewBuilder(WithMD5Namer()).BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, "// changed"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewBuilder(WithMD5Namer()).BuildRoutingVCL([]VCLConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("MD5 routing VCL did not change after route content changed")
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
