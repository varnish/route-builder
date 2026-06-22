// Package routebuilder provides a library for managing Varnish Cache routing.
//
// It parses per-service VCL files (with YAML frontmatter) or YAML routes manifests,
// generates the Varnish cmdfile and routing VCL needed for host-based dispatch,
// and can reload a running Varnish instance via its admin socket.
//
// # Basic Usage
//
//	// Parse VCL files with routing frontmatter
//	cfg, err := routebuilder.ParseVCL("service.vcl")
//
//	// Or parse a YAML routes manifest
//	configs, err := routebuilder.ParseRoutes("routes.yaml")
//
//	// Validate configurations
//	if err := routebuilder.ValidateConfigs(configs); err != nil {
//	    log.Fatal(err)
//	}
//
//	// Generate outputs
//	timestamp := routebuilder.NewTimestamp()
//	vcl, _ := routebuilder.BuildRoutingVCL(configs, timestamp)
//	cmdfile, _ := routebuilder.BuildCmdfile(configs, "/etc/varnish/routing.vcl", timestamp)
//
//	// Write files
//	routebuilder.WriteFileAtomic("/etc/varnish/routing.vcl", vcl)
//	routebuilder.WriteFileAtomic("/etc/varnish/cmdfile", cmdfile)
//
//	// Or reload a running Varnish instance
//	conn, _ := adm.Connect(ctx, "")
//	routebuilder.ReloadVarnish(ctx, conn, configs, timestamp, os.Stderr)
package routebuilder

// Prefix constants for Varnish object names managed by route-builder.
// These prefixes allow route-builder to identify and clean up its own objects
// without affecting user-created VCLs, labels, or certificates.
const (
	// PrefixRouting is the prefix for routing VCL names.
	PrefixRouting = "rb-routing-"

	// PrefixLabel is the prefix for VCL label names.
	PrefixLabel = "rb-label-"

	// PrefixVCL is the prefix for per-route VCL names.
	PrefixVCL = "rb-vcl-"

	// PrefixCert is the prefix for TLS certificate IDs.
	PrefixCert = "rb-cert-"
)
