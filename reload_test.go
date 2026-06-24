package routebuilder

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varnish/varnish-go/adm"
	"github.com/varnish/varnish-go/vtest"
)

func writeRouteVCL(t *testing.T, dir, name string, hostnames []string) string {
	t.Helper()
	content := fmt.Sprintf("/* routing\n%s\n*/\nvcl 4.1;\nbackend default none;\nsub vcl_recv {\n    return(synth(200, %q));\n}\n", buildFrontmatter(name, hostnames), name)
	path := filepath.Join(dir, name+".vcl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadLabels loads each route VCL and creates its label. Must be called before
// activating the routing VCL, because Varnish 9 resolves label references at compile time.
func loadLabels(t *testing.T, v vtest.Varnish, configs []VCLConfig, ts string) {
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

func doGet(t *testing.T, v vtest.Varnish, host string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", v.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func testCertFiles(t *testing.T) (cert, key string) {
	t.Helper()
	var err error
	cert, err = filepath.Abs("testdata/test.crt")
	if err != nil {
		t.Fatal(err)
	}
	key, err = filepath.Abs("testdata/test.key")
	if err != nil {
		t.Fatal(err)
	}
	return
}

func doGetTLS(t *testing.T, tlsURL, sniHost string) *http.Response {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test-only self-signed cert
				ServerName:         sniHost,
			},
		},
	}
	req, err := http.NewRequest("GET", tlsURL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = sniHost
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func admConnect(t *testing.T, v vtest.Varnish) *adm.Conn {
	t.Helper()
	conn, err := adm.Connect(context.Background(), v.Name())
	if err != nil {
		t.Fatalf("adm.Connect: %v", err)
	}
	return conn
}

func TestRoutingTwoServices(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com", "www.foo.com"})
	barPath := writeRouteVCL(t, dir, "bar_service", []string{"bar.com"})

	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	barCfg, err := ParseVCL(barPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []VCLConfig{fooCfg, barCfg}

	routingVCL, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	cases := []struct {
		host    string
		wantSvc string
	}{
		{"foo.com", "foo_service"},
		{"www.foo.com", "foo_service"},
		{"bar.com", "bar_service"},
	}
	for _, tc := range cases {
		resp := doGet(t, v, tc.host)
		resp.Body.Close()
		if resp.Status != "200 "+tc.wantSvc {
			t.Errorf("host %s: want %q, got %q", tc.host, "200 "+tc.wantSvc, resp.Status)
		}
	}
}

func TestRoutingWildcard(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	wcPath := writeRouteVCL(t, dir, "wc_service", []string{"*.foo.com"})
	wcCfg, err := ParseVCL(wcPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []VCLConfig{wcCfg}

	routingVCL, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	cases := []struct {
		host       string
		wantStatus string
	}{
		{"sub.foo.com", "200 wc_service"},
		{"other.foo.com", "200 wc_service"},
	}
	for _, tc := range cases {
		resp := doGet(t, v, tc.host)
		resp.Body.Close()
		if resp.Status != tc.wantStatus {
			t.Errorf("host %s: want %q, got %q", tc.host, tc.wantStatus, resp.Status)
		}
	}

	noMatch := []string{"foo.com", "bar.com"}
	for _, host := range noMatch {
		resp := doGet(t, v, host)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("host %s: want 404, got %d", host, resp.StatusCode)
		}
	}
}

func TestRoutingNoMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}

	routingVCL, err := buildRoutingVCL([]VCLConfig{fooCfg}, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, []VCLConfig{fooCfg}, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	resp := doGet(t, v, "unknown.com")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown host: want 404, got %d", resp.StatusCode)
	}
}

func TestRoutingMultipleHostnames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	hostnames := []string{"svc.com", "www.svc.com", "api.svc.com"}
	svcPath := writeRouteVCL(t, dir, "svc", hostnames)
	svcCfg, err := ParseVCL(svcPath)
	if err != nil {
		t.Fatal(err)
	}

	routingVCL, err := buildRoutingVCL([]VCLConfig{svcCfg}, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, []VCLConfig{svcCfg}, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	for _, host := range hostnames {
		resp := doGet(t, v, host)
		resp.Body.Close()
		if resp.Status != "200 svc" {
			t.Errorf("host %s: want %q, got %q", host, "200 svc", resp.Status)
		}
	}
}

func TestReloadCertCleanup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certFile, keyFile := testCertFiles(t)

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	fooCfg.TLS = []TLSEntry{{Cert: certFile, Key: keyFile}}
	configs := []VCLConfig{fooCfg}

	v := vtest.New().VclString(`backend default none;`).TLSListener().AssertStart(t)
	defer v.Stop()

	// First reload: establishes rb-cert-* state.
	ts1 := newTimestamp()
	if err := reloadVarnish(context.Background(), admConnect(t, v), configs, ts1, os.Stderr); err != nil {
		t.Fatalf("first reloadVarnish: %v", err)
	}

	// Snapshot rb-cert-* IDs created by the first reload.
	firstCerts, err := admConnect(t, v).TLSCertList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := make(map[string]bool)
	for _, e := range firstCerts {
		if strings.HasPrefix(e.ID, "rb-cert-") {
			firstIDs[e.ID] = true
		}
	}
	if len(firstIDs) == 0 {
		t.Fatal("expected rb-cert-* IDs after first reload, got none")
	}

	// Second reload with the same config: should load new rb-cert-* IDs and discard the first ones.
	ts2 := newTimestamp()
	if err := reloadVarnish(context.Background(), admConnect(t, v), configs, ts2, os.Stderr); err != nil {
		t.Fatalf("second reloadVarnish: %v", err)
	}

	afterCerts, err := admConnect(t, v).TLSCertList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	afterIDs := make(map[string]bool)
	for _, e := range afterCerts {
		afterIDs[e.ID] = true
	}
	for id := range firstIDs {
		if afterIDs[id] {
			t.Errorf("old rb-cert-* ID %q still present after second reload", id)
		}
	}
}

func TestRoutingTLSCertLoad(t *testing.T) {
	t.Parallel()

	certFile, keyFile := testCertFiles(t)
	v := vtest.New().VclString(`backend default none;`).TLSListener().AssertStart(t)
	defer v.Stop()

	if _, err := v.Adm("tls.cert.load", certFile, "-k", keyFile); err != nil {
		t.Fatalf("tls.cert.load: %v", err)
	}
	if _, err := v.Adm("tls.cert.commit"); err != nil {
		t.Fatalf("tls.cert.commit: %v", err)
	}
}

func TestRoutingViaSNI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com", "www.foo.com"})
	barPath := writeRouteVCL(t, dir, "bar_service", []string{"bar.com"})

	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	barCfg, err := ParseVCL(barPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []VCLConfig{fooCfg, barCfg}

	routingVCL, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatal(err)
	}

	certFile, keyFile := testCertFiles(t)
	v := vtest.New().VclString(`backend default none;`).TLSListener().AssertStart(t)
	defer v.Stop()

	if _, err := v.Adm("tls.cert.load", certFile, "-k", keyFile); err != nil {
		t.Fatalf("tls.cert.load: %v", err)
	}
	if _, err := v.Adm("tls.cert.commit"); err != nil {
		t.Fatalf("tls.cert.commit: %v", err)
	}

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	cases := []struct {
		host    string
		wantSvc string
	}{
		{"foo.com", "foo_service"},
		{"www.foo.com", "foo_service"},
		{"bar.com", "bar_service"},
	}
	for _, tc := range cases {
		resp := doGetTLS(t, v.TLSURL, tc.host)
		resp.Body.Close()
		if resp.Status != "200 "+tc.wantSvc {
			t.Errorf("SNI %s: want %q, got %q", tc.host, "200 "+tc.wantSvc, resp.Status)
		}
	}
}

func TestReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts1 := time.Now().Format("2006-01-02T15-04-05_0")

	// Initial routing: foo_service (foo.com) + bar_service (bar.com)
	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	barPath := writeRouteVCL(t, dir, "bar_service", []string{"bar.com"})

	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	barCfg, err := ParseVCL(barPath)
	if err != nil {
		t.Fatal(err)
	}
	initialConfigs := []VCLConfig{fooCfg, barCfg}

	routingVCL1, err := buildRoutingVCL(initialConfigs, ts1)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, initialConfigs, ts1)
	activateRoutingVCL(t, v, dir, routingVCL1, ts1)

	// Verify initial routing
	resp := doGet(t, v, "foo.com")
	resp.Body.Close()
	if resp.Status != "200 foo_service" {
		t.Fatalf("initial foo.com: want %q, got %q", "200 foo_service", resp.Status)
	}
	resp = doGet(t, v, "bar.com")
	resp.Body.Close()
	if resp.Status != "200 bar_service" {
		t.Fatalf("initial bar.com: want %q, got %q", "200 bar_service", resp.Status)
	}

	// New config: foo_service updated, bar_service removed, baz_service added
	bazPath := writeRouteVCL(t, dir, "baz_service", []string{"baz.com"})
	bazCfg, err := ParseVCL(bazPath)
	if err != nil {
		t.Fatal(err)
	}
	newConfigs := []VCLConfig{fooCfg, bazCfg}

	ts2 := newTimestamp()
	if err := reloadVarnish(context.Background(), admConnect(t, v), newConfigs, ts2, os.Stderr); err != nil {
		t.Fatalf("reloadVarnish: %v", err)
	}

	resp = doGet(t, v, "foo.com")
	resp.Body.Close()
	if resp.Status != "200 foo_service" {
		t.Errorf("after reload foo.com: want %q, got %q", "200 foo_service", resp.Status)
	}

	resp = doGet(t, v, "baz.com")
	resp.Body.Close()
	if resp.Status != "200 baz_service" {
		t.Errorf("after reload baz.com: want %q, got %q", "200 baz_service", resp.Status)
	}

	resp = doGet(t, v, "bar.com")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("after reload bar.com: want 404, got %d", resp.StatusCode)
	}

	// Old VCLs should be cleaned up
	vcls, err := admConnect(t, v).VCLList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, e := range vcls {
		if e.State != "label" && strings.HasSuffix(name, ts1) {
			t.Errorf("old VCL %q still present after reload", name)
		}
	}
}

func TestRunReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []VCLConfig{fooCfg}

	routingVCL, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	routesYAML := routesYAMLFor(t, dir, "foo_service", []string{"foo.com"}, fooPath)

	var stdout, stderr strings.Builder
	code := Run([]string{"-reload", "-n", v.Name(), "-cmdfile", "none", "-vclfile", "none", routesYAML}, &stdout, &stderr)
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
		code := Run([]string{"-reload"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("bad routes file", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "bad.yaml", "routes: [unclosed\n")
		var stdout, stderr strings.Builder
		code := Run([]string{"-reload", "-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1, got %d", code)
		}
	})

	t.Run("connect fails", func(t *testing.T) {
		dir := t.TempDir()
		path := writeRoutesYAML(t, dir, "routes.yaml",
			"routes:\n  - name: foo\n    hostnames:\n      - foo.com\n")
		var stdout, stderr strings.Builder
		code := Run([]string{"-reload", "-n", "nonexistent_instance_xyz", "-cmdfile", "none", "-vclfile", "none", path}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("want exit 1 for bad instance, got %d", code)
		}
	})
}

func TestReloadRollbackWithTLS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certFile, keyFile := testCertFiles(t)

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).TLSListener().AssertStart(t)
	defer v.Stop()

	beforeVCLs, err := admConnect(t, v).VCLList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeCerts, err := admConnect(t, v).TLSCertList(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Two TLS entries: first valid (sets hasStagedTLS=true), second invalid (triggers rollback).
	// Stage 3 (vcl.inline) succeeds before stage 4 fails, so routingVCLName != "" in rollback.
	failConfigs := []VCLConfig{{
		Name:      fooCfg.Name,
		VclPath:   fooCfg.VclPath,
		Hostnames: fooCfg.Hostnames,
		TLS: []TLSEntry{
			{Cert: certFile, Key: keyFile},
			{Cert: "/nonexistent/cert.pem", Key: "/nonexistent/key.pem"},
		},
	}}

	ts := newTimestamp()
	err = reloadVarnish(context.Background(), admConnect(t, v), failConfigs, ts, os.Stderr)
	if err == nil {
		t.Fatal("expected reload to fail on bad TLS cert")
	}

	afterVCLs, err := admConnect(t, v).VCLList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name := range afterVCLs {
		if _, existed := beforeVCLs[name]; !existed {
			t.Errorf("orphaned VCL %q left after failed reload", name)
		}
	}
	for name := range beforeVCLs {
		if _, exists := afterVCLs[name]; !exists {
			t.Errorf("VCL %q accidentally removed during failed reload", name)
		}
	}

	afterCerts, err := admConnect(t, v).TLSCertList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterCerts) != len(beforeCerts) {
		t.Errorf("TLS cert count after rollback: want %d, got %d", len(beforeCerts), len(afterCerts))
	}
}

func TestReloadRollback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ts := time.Now().Format("2006-01-02T15-04-05_0")

	fooPath := writeRouteVCL(t, dir, "foo_service", []string{"foo.com"})
	fooCfg, err := ParseVCL(fooPath)
	if err != nil {
		t.Fatal(err)
	}
	configs := []VCLConfig{fooCfg}

	routingVCL, err := buildRoutingVCL(configs, ts)
	if err != nil {
		t.Fatal(err)
	}

	v := vtest.New().VclString(`backend default none;`).AssertStart(t)
	defer v.Stop()

	loadLabels(t, v, configs, ts)
	activateRoutingVCL(t, v, dir, routingVCL, ts)

	// Verify initial routing works
	resp := doGet(t, v, "foo.com")
	resp.Body.Close()
	if resp.Status != "200 foo_service" {
		t.Fatalf("initial foo.com: want %q, got %q", "200 foo_service", resp.Status)
	}

	// Snapshot VCL state before the failed reload attempt.
	before, err := admConnect(t, v).VCLList(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Reload with a config pointing to a non-existent file → stage 2 fails mid-loop.
	failConfigs := append(configs, VCLConfig{
		Name:      "bad_service",
		VclPath:   "/nonexistent/bad.vcl",
		Hostnames: []string{"bad.com"},
	})
	ts2 := newTimestamp()
	err = reloadVarnish(context.Background(), admConnect(t, v), failConfigs, ts2, os.Stderr)
	if err == nil {
		t.Fatal("expected reload to fail, but it succeeded")
	}

	// Old routing must still work
	resp = doGet(t, v, "foo.com")
	resp.Body.Close()
	if resp.Status != "200 foo_service" {
		t.Errorf("after failed reload foo.com: want %q, got %q", "200 foo_service", resp.Status)
	}

	// VCL list must be identical to before: no orphans, no accidental discards.
	after, err := admConnect(t, v).VCLList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name := range after {
		if _, existed := before[name]; !existed {
			t.Errorf("orphaned VCL %q left after failed reload", name)
		}
	}
	for name := range before {
		if _, exists := after[name]; !exists {
			t.Errorf("VCL %q accidentally removed during failed reload", name)
		}
	}
}
