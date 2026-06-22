package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/varnish/varnish-go/adm"
)

// vclRollback undoes staged VCL and TLS changes on failure before vcl.use.
// Order matters: routing VCL references labels, labels reference per-route VCLs.
func vclRollback(ctx context.Context, conn *adm.Conn, hasStagedTLS bool, routingVCLName string, loadedLabels, loadedVCLs []string, stderr io.Writer) {
	if hasStagedTLS {
		if err := conn.TLSCertRollback(ctx); err != nil {
			fmt.Fprintf(stderr, "warning: rollback TLS certs: %v\n", err)
		}
	}
	if routingVCLName != "" {
		if err := conn.VCLDiscard(ctx, routingVCLName); err != nil {
			fmt.Fprintf(stderr, "warning: rollback discard %s: %v\n", routingVCLName, err)
		}
	}
	for _, name := range loadedLabels {
		if err := conn.VCLDiscard(ctx, name); err != nil {
			fmt.Fprintf(stderr, "warning: rollback discard %s: %v\n", name, err)
		}
	}
	for _, name := range loadedVCLs {
		if err := conn.VCLDiscard(ctx, name); err != nil {
			fmt.Fprintf(stderr, "warning: rollback discard %s: %v\n", name, err)
		}
	}
}

func reloadVarnish(ctx context.Context, conn *adm.Conn, configs []VCLConfig, timestamp string, stderr io.Writer) error {
	// Stage 1: snapshot route-builder VCLs, labels, and cert IDs for cleanup.
	vclList, err := conn.VCLList(ctx)
	if err != nil {
		return fmt.Errorf("vcl.list: %w", err)
	}
	var snapshot []string
	for name := range vclList {
		for _, prefix := range []string{prefixRouting, prefixLabel, prefixVCL} {
			if strings.HasPrefix(name, prefix) {
				snapshot = append(snapshot, name)
				break
			}
		}
	}

	// Snapshot rb-cert-* cert IDs owned by a previous route-builder reload.
	// We only discard IDs we created (prefix rb-cert-); foreign certs are untouched.
	// TLSCertList failure is only fatal if the new config requires TLS.
	var hasTLSConfig bool
	for _, cfg := range configs {
		if len(cfg.TLS) > 0 {
			hasTLSConfig = true
			break
		}
	}
	var oldCertIDs []string
	oldCertEntries, certListErr := conn.TLSCertList(ctx)
	if certListErr != nil {
		if hasTLSConfig {
			return fmt.Errorf("tls.cert.list: %w", certListErr)
		}
		// No TLS listener and no new TLS config: nothing to clean up.
	} else {
		seen := make(map[string]bool)
		for _, e := range oldCertEntries {
			if strings.HasPrefix(e.ID, prefixCert) && !seen[e.ID] {
				oldCertIDs = append(oldCertIDs, e.ID)
				seen[e.ID] = true
			}
		}
	}

	var loadedVCLs []string
	var loadedLabels []string
	var routingVCLName string
	var hasStagedTLS bool

	// Stage 2: load each per-route VCL and create its timestamped label
	for _, cfg := range configs {
		if cfg.VclPath == "" {
			vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
			return fmt.Errorf("route %q: VclPath is empty after validation", cfg.Name)
		}
		vclName := fmt.Sprintf("%s%s-%s", prefixVCL, cfg.Name, timestamp)
		if err := conn.VCLLoad(ctx, vclName, cfg.VclPath, adm.VCLStateAuto); err != nil {
			vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
			return fmt.Errorf("vcl.load %s: %w", vclName, err)
		}
		loadedVCLs = append(loadedVCLs, vclName)
		labelName := fmt.Sprintf("%s%s-%s", prefixLabel, cfg.Name, timestamp)
		if err := conn.VCLLabel(ctx, labelName, vclName); err != nil {
			vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
			return fmt.Errorf("vcl.label %s: %w", labelName, err)
		}
		loadedLabels = append(loadedLabels, labelName)
	}

	// Stage 3: compile and load routing VCL inline with matching timestamp
	routingVCLName = prefixRouting + timestamp
	routingContent, err := buildRoutingVCL(configs, timestamp)
	if err != nil {
		vclRollback(ctx, conn, hasStagedTLS, "", loadedLabels, loadedVCLs, stderr)
		return fmt.Errorf("build routing VCL: %w", err)
	}
	if err := conn.VCLInline(ctx, routingVCLName, routingContent, adm.VCLStateAuto); err != nil {
		vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
		return fmt.Errorf("vcl.inline routing: %w", err)
	}

	// Stage 4: load TLS certs with stable rb-cert-<name>-<idx>-<ts> IDs.
	// Using an explicit ID means stage 8 can target exactly these certs by
	// prefix, and reloading the same cert file never collides with the new ID.
	for _, cfg := range configs {
		for i, t := range cfg.TLS {
			certID := fmt.Sprintf("%s%s-%d-%s", prefixCert, cfg.Name, i, timestamp)
			var opts []adm.TLSOption
			opts = append(opts, adm.TLSWithCertID(certID))
			if t.Key != "" {
				opts = append(opts, adm.TLSWithKeyFile(t.Key))
			}
			certFile := t.PEM
			if certFile == "" {
				certFile = t.Cert
			}
			if err := conn.TLSCertLoad(ctx, certFile, opts...); err != nil {
				vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
				return fmt.Errorf("tls.cert.load: %w", err)
			}
			hasStagedTLS = true
		}
	}

	// Stage 5: commit TLS certs
	if hasStagedTLS {
		if err := conn.TLSCertCommit(ctx); err != nil {
			vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
			return fmt.Errorf("tls.cert.commit: %w", err)
		}
		hasStagedTLS = false
	}

	// Stage 6: activate new routing VCL. TLS certs are already committed and
	// cannot be rolled back; this is the true point of no return. VCLs loaded
	// in stages 2–3 are still cleaned up on failure because they were never
	// activated and would otherwise be orphaned.
	if err := conn.VCLUse(ctx, routingVCLName); err != nil {
		vclRollback(ctx, conn, hasStagedTLS, routingVCLName, loadedLabels, loadedVCLs, stderr)
		return fmt.Errorf("vcl.use %s: %w", routingVCLName, err)
	}

	// Stage 7: discard old route-builder objects from snapshot, in dependency order.
	warnDiscard := func(name string, err error) {
		if err != nil {
			fmt.Fprintf(stderr, "warning: cleanup %s: %v\n", name, err)
		}
	}
	for _, prefix := range []string{prefixRouting, prefixLabel, prefixVCL} {
		for _, name := range snapshot {
			if strings.HasPrefix(name, prefix) {
				warnDiscard(name, conn.VCLDiscard(ctx, name))
			}
		}
	}

	// Stage 8: discard previous rb-cert-* entries now that the new certs are active.
	// Because we assigned explicit IDs in stage 4, the old IDs are distinct from
	// the new ones even when the same cert file is reloaded.
	var discarded bool
	for _, id := range oldCertIDs {
		if err := conn.TLSCertDiscard(ctx, id); err != nil {
			fmt.Fprintf(stderr, "warning: cleanup cert %s: %v\n", id, err)
		} else {
			discarded = true
		}
	}
	if discarded {
		if err := conn.TLSCertCommit(ctx); err != nil {
			fmt.Fprintf(stderr, "warning: cleanup cert commit: %v\n", err)
		}
	}

	return nil
}
