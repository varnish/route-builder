package routebuilder

import (
	"fmt"
	"sort"
	"strings"
)

// CmdfileOptions controls cmdfile planning.
type CmdfileOptions struct {
	// ExistingVCLNames is an optional set of already-loaded VCL object names.
	// When present, BuildCmdfilePlan skips vcl.load/vcl.label commands whose
	// target object names already exist. The final vcl.use command is always
	// emitted so the requested routing VCL is activated.
	ExistingVCLNames map[string]bool
}

// CmdfilePlan is the ordered command plan needed to load route-builder state.
type CmdfilePlan struct {
	TLSCommands     []string
	RouteCommands   []string
	RoutingCommands []string
	UseCommand      string
}

// Commands returns the complete command list in Varnish-safe dependency order.
func (p CmdfilePlan) Commands() []string {
	commands := make([]string, 0, len(p.TLSCommands)+len(p.RouteCommands)+len(p.RoutingCommands)+1)
	commands = append(commands, p.TLSCommands...)
	commands = append(commands, p.RouteCommands...)
	commands = append(commands, p.RoutingCommands...)
	if p.UseCommand != "" {
		commands = append(commands, p.UseCommand)
	}
	return commands
}

// String serializes the plan as a Varnish CLI cmdfile.
func (p CmdfilePlan) String() string {
	commands := p.Commands()
	if len(commands) == 0 {
		return ""
	}
	return strings.Join(commands, "\n") + "\n"
}

// RoutingVCLName returns the generated routing VCL object name.
func RoutingVCLName(timestamp string) string {
	return PrefixRouting + timestamp
}

// RouteVCLName returns the generated per-route VCL object name.
func RouteVCLName(cfg VCLConfig, timestamp string) string {
	return fmt.Sprintf("%s%s-%s", PrefixVCL, cfg.Name, timestamp)
}

// RouteLabelName returns the generated per-route label object name.
func RouteLabelName(cfg VCLConfig, timestamp string) string {
	return fmt.Sprintf("%s%s-%s", PrefixLabel, cfg.Name, timestamp)
}

// TLSCertID returns the generated TLS certificate ID for a route TLS entry.
func TLSCertID(cfg VCLConfig, index int, timestamp string) string {
	return fmt.Sprintf("%s%s-%d-%s", PrefixCert, cfg.Name, index, timestamp)
}

// ManagedVCLNames returns the VCL object names route-builder manages for the
// given config set and timestamp. TLS certificate IDs are intentionally not
// included because they are managed through tls.cert.* commands, not vcl.*.
func ManagedVCLNames(configs []VCLConfig, timestamp string) map[string]bool {
	keep := map[string]bool{RoutingVCLName(timestamp): true}
	for _, cfg := range configs {
		keep[RouteVCLName(cfg, timestamp)] = true
		keep[RouteLabelName(cfg, timestamp)] = true
	}
	return keep
}

// IsManagedVCLName reports whether name is a VCL object name owned by
// route-builder's standard prefixes.
func IsManagedVCLName(name string) bool {
	return strings.HasPrefix(name, PrefixRouting) || strings.HasPrefix(name, PrefixLabel) || strings.HasPrefix(name, PrefixVCL)
}

// CleanupVCLNamesFromNames returns stale route-builder VCL object names in
// discard order: routing VCLs first, then labels, then per-route VCLs.
func CleanupVCLNamesFromNames(names []string, keep map[string]bool) []string {
	var routing, labels, vcls []string
	for _, name := range names {
		if keep[name] || !IsManagedVCLName(name) {
			continue
		}
		switch {
		case strings.HasPrefix(name, PrefixRouting):
			routing = append(routing, name)
		case strings.HasPrefix(name, PrefixLabel):
			labels = append(labels, name)
		case strings.HasPrefix(name, PrefixVCL):
			vcls = append(vcls, name)
		}
	}
	sort.Strings(routing)
	sort.Strings(labels)
	sort.Strings(vcls)
	out := make([]string, 0, len(routing)+len(labels)+len(vcls))
	out = append(out, routing...)
	out = append(out, labels...)
	out = append(out, vcls...)
	return out
}

// CleanupCommandsFromNames returns vcl.discard commands for stale route-builder
// VCL objects in dependency-safe order.
func CleanupCommandsFromNames(names []string, keep map[string]bool) []string {
	stale := CleanupVCLNamesFromNames(names, keep)
	commands := make([]string, len(stale))
	for i, name := range stale {
		commands[i] = "vcl.discard " + name
	}
	return commands
}

// BuildCmdfilePlan creates the Varnish CLI command plan for the given configs.
func BuildCmdfilePlan(configs []VCLConfig, routingPath string, timestamp string, opts CmdfileOptions) (CmdfilePlan, error) {
	if routingPath == "" {
		return CmdfilePlan{}, fmt.Errorf("routingPath is required")
	}
	if err := validateGenerationTimestamp(timestamp); err != nil {
		return CmdfilePlan{}, err
	}
	for i, cfg := range configs {
		if err := validateCmdfileConfig(cfg); err != nil {
			return CmdfilePlan{}, fmt.Errorf("route %d: %w", i, err)
		}
	}

	var plan CmdfilePlan
	for _, cfg := range configs {
		for i, t := range cfg.TLS {
			certID := TLSCertID(cfg, i, timestamp)
			if t.PEM != "" {
				plan.TLSCommands = append(plan.TLSCommands, "tls.cert.load "+certID+" "+quoteCLIArg(t.PEM))
			} else {
				plan.TLSCommands = append(plan.TLSCommands, "tls.cert.load "+certID+" "+quoteCLIArg(t.Cert)+" -k "+quoteCLIArg(t.Key))
			}
		}
	}
	if len(plan.TLSCommands) > 0 {
		plan.TLSCommands = append(plan.TLSCommands, "tls.cert.commit")
	}

	for _, cfg := range configs {
		vclName := RouteVCLName(cfg, timestamp)
		labelName := RouteLabelName(cfg, timestamp)
		if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[vclName] {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.load "+vclName+" "+quoteCLIArg(cfg.VclPath))
		}
		if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[labelName] {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.label "+labelName+" "+vclName)
		}
	}

	routingName := RoutingVCLName(timestamp)
	if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[routingName] {
		plan.RoutingCommands = append(plan.RoutingCommands, "vcl.load "+routingName+" "+quoteCLIArg(routingPath))
	}
	plan.UseCommand = "vcl.use " + routingName
	return plan, nil
}

// BuildCmdfileWithExisting generates cmdfile content while skipping VCL object
// loads and labels that already exist in existingVCLNames.
func BuildCmdfileWithExisting(configs []VCLConfig, routingPath string, timestamp string, existingVCLNames map[string]bool) (string, error) {
	plan, err := BuildCmdfilePlan(configs, routingPath, timestamp, CmdfileOptions{ExistingVCLNames: existingVCLNames})
	if err != nil {
		return "", err
	}
	return plan.String(), nil
}
