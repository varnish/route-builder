package routebuilder

import (
	"crypto/md5"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// EntityNamer names generated Varnish entities from a caller-provided prefix
// and optional content bytes.
type EntityNamer interface {
	Name(prefix string, content []byte) (string, error)
}

type contentRequiringNamer interface {
	RequiresContent() bool
}

type entityNamerFunc func(prefix string, content []byte) (string, error)

func (f entityNamerFunc) Name(prefix string, content []byte) (string, error) {
	return f(prefix, content)
}

type constantNamer struct {
	suffix string
}

// ConstantNamer returns an EntityNamer that appends a constant suffix and
// ignores content. Passing NewTimestamp() as suffix preserves route-builder's
// traditional timestamped naming behavior.
func ConstantNamer(suffix string) EntityNamer {
	return constantNamer{suffix: suffix}
}

func (n constantNamer) Name(prefix string, content []byte) (string, error) {
	if err := validateObjectNamePart("suffix", n.suffix); err != nil {
		return "", err
	}
	return prefix + n.suffix, nil
}

type md5Namer struct{}

// MD5Namer returns an EntityNamer that appends the MD5 hash of content.
func MD5Namer() EntityNamer {
	return md5Namer{}
}

func (n md5Namer) Name(prefix string, content []byte) (string, error) {
	if content == nil {
		return "", fmt.Errorf("content is required for MD5 naming")
	}
	return prefix + contentHash(content), nil
}

func (n md5Namer) RequiresContent() bool { return true }

// BuilderOption configures a Builder.
type BuilderOption func(*Builder)

// WithEntityNamer configures the namer used for generated VCL objects and TLS
// certificate IDs.
func WithEntityNamer(namer EntityNamer) BuilderOption {
	return func(b *Builder) {
		if namer != nil {
			b.namer = namer
		}
	}
}

// WithConstantNamer configures a constant-suffix namer.
func WithConstantNamer(suffix string) BuilderOption {
	return WithEntityNamer(ConstantNamer(suffix))
}

// WithMD5Namer configures a content-addressed MD5 namer.
func WithMD5Namer() BuilderOption {
	return WithEntityNamer(MD5Namer())
}

// Builder generates routing VCL, cmdfiles, cleanup plans, and live reloads
// using a configured naming strategy.
type Builder struct {
	namer EntityNamer
}

// NewBuilder constructs a Builder. By default it uses ConstantNamer(NewTimestamp()).
func NewBuilder(opts ...BuilderOption) *Builder {
	b := &Builder{namer: ConstantNamer(NewTimestamp())}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

type cmdfileOptions struct {
	ExistingVCLNames map[string]bool
}

// CmdfileOption configures cmdfile planning.
type CmdfileOption interface {
	applyCmdfileOption(*cmdfileOptions)
}

type cmdfileOptionFunc func(*cmdfileOptions)

func (f cmdfileOptionFunc) applyCmdfileOption(opts *cmdfileOptions) {
	f(opts)
}

// WithExistingVCLNames configures BuildCmdfilePlan to skip vcl.load/vcl.label
// commands whose target VCL object names already exist. The final vcl.use
// command is always emitted so the requested routing VCL is activated.
func WithExistingVCLNames(names ...string) CmdfileOption {
	return cmdfileOptionFunc(func(opts *cmdfileOptions) {
		if opts.ExistingVCLNames == nil {
			opts.ExistingVCLNames = make(map[string]bool, len(names))
		}
		for _, name := range names {
			opts.ExistingVCLNames[name] = true
		}
	})
}

func applyCmdfileOptions(options []CmdfileOption) cmdfileOptions {
	var opts cmdfileOptions
	for _, option := range options {
		if option != nil {
			option.applyCmdfileOption(&opts)
		}
	}
	return opts
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

// BuildResult contains all outputs from a combined Builder.Build call.
type BuildResult struct {
	RoutingVCL      string
	CmdfilePlan     CmdfilePlan
	ManagedVCLNames map[string]bool
}

type routeObjectNames struct {
	VCL   string
	Label string
}

func contentHash(data []byte) string {
	return fmt.Sprintf("%x", md5.Sum(data))
}

func namerRequiresContent(namer EntityNamer) bool {
	requiring, ok := namer.(contentRequiringNamer)
	return ok && requiring.RequiresContent()
}

func readRequiredContent(path string, subject string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s path is required for content-based naming", subject)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s for content-based naming: %w", subject, err)
	}
	return data, nil
}

func (b *Builder) routeContent(cfg VCLConfig) ([]byte, error) {
	if !namerRequiresContent(b.namer) {
		return nil, nil
	}
	return readRequiredContent(cfg.VclPath, fmt.Sprintf("route %q vclPath", cfg.Name))
}

func (b *Builder) routeObjectNamesForConfig(cfg VCLConfig) (routeObjectNames, error) {
	content, err := b.routeContent(cfg)
	if err != nil {
		return routeObjectNames{}, err
	}
	vclName, err := b.namer.Name(PrefixVCL+cfg.Name+"-", content)
	if err != nil {
		return routeObjectNames{}, err
	}
	labelName, err := b.namer.Name(PrefixLabel+cfg.Name+"-", content)
	if err != nil {
		return routeObjectNames{}, err
	}
	return routeObjectNames{VCL: vclName, Label: labelName}, nil
}

func (b *Builder) routeObjectNamesForConfigs(configs []VCLConfig) ([]routeObjectNames, error) {
	names := make([]routeObjectNames, len(configs))
	for i, cfg := range configs {
		name, err := b.routeObjectNamesForConfig(cfg)
		if err != nil {
			return nil, err
		}
		names[i] = name
	}
	return names, nil
}

func (b *Builder) routingVCLName(routingVCL []byte) (string, error) {
	return b.namer.Name(PrefixRouting, routingVCL)
}

func (b *Builder) tlsCertContent(t TLSEntry) ([]byte, error) {
	if !namerRequiresContent(b.namer) {
		return nil, nil
	}
	if t.PEM != "" {
		return readRequiredContent(t.PEM, "tls pem")
	}
	cert, err := readRequiredContent(t.Cert, "tls cert")
	if err != nil {
		return nil, err
	}
	key, err := readRequiredContent(t.Key, "tls key")
	if err != nil {
		return nil, err
	}
	content := make([]byte, 0, len(cert)+len(key)+1)
	content = append(content, cert...)
	content = append(content, 0)
	content = append(content, key...)
	return content, nil
}

func (b *Builder) tlsCertID(cfg VCLConfig, index int, t TLSEntry) (string, error) {
	content, err := b.tlsCertContent(t)
	if err != nil {
		return "", err
	}
	return b.namer.Name(PrefixCert+cfg.Name+"-"+strconv.Itoa(index)+"-", content)
}

// ManagedVCLNames returns the VCL object names this builder manages for the
// given config set and routing VCL content. TLS certificate IDs are intentionally
// not included because they are managed through tls.cert.* commands, not vcl.*.
func (b *Builder) ManagedVCLNames(configs []VCLConfig, routingVCL []byte) (map[string]bool, error) {
	names, err := b.routeObjectNamesForConfigs(configs)
	if err != nil {
		return nil, err
	}
	routingName, err := b.routingVCLName(routingVCL)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{routingName: true}
	for _, name := range names {
		keep[name.VCL] = true
		keep[name.Label] = true
	}
	return keep, nil
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
func (b *Builder) BuildCmdfilePlan(configs []VCLConfig, routingPath string, options ...CmdfileOption) (CmdfilePlan, error) {
	if routingPath == "" {
		return CmdfilePlan{}, fmt.Errorf("routingPath is required")
	}
	for i, cfg := range configs {
		if err := validateCmdfileConfig(cfg); err != nil {
			return CmdfilePlan{}, fmt.Errorf("route %d: %w", i, err)
		}
	}

	routeNames, err := b.routeObjectNamesForConfigs(configs)
	if err != nil {
		return CmdfilePlan{}, err
	}
	routingVCL, err := b.BuildRoutingVCL(configs)
	if err != nil {
		return CmdfilePlan{}, err
	}
	routingName, err := b.routingVCLName([]byte(routingVCL))
	if err != nil {
		return CmdfilePlan{}, err
	}
	opts := applyCmdfileOptions(options)

	var plan CmdfilePlan
	for _, cfg := range configs {
		for i, t := range cfg.TLS {
			certID, err := b.tlsCertID(cfg, i, t)
			if err != nil {
				return CmdfilePlan{}, fmt.Errorf("route %q tls entry %d: %w", cfg.Name, i, err)
			}
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

	for i, cfg := range configs {
		vclName := routeNames[i].VCL
		labelName := routeNames[i].Label
		if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[vclName] {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.load "+vclName+" "+quoteCLIArg(cfg.VclPath))
		}
		if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[labelName] {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.label "+labelName+" "+vclName)
		}
	}

	if opts.ExistingVCLNames == nil || !opts.ExistingVCLNames[routingName] {
		plan.RoutingCommands = append(plan.RoutingCommands, "vcl.load "+routingName+" "+quoteCLIArg(routingPath))
	}
	plan.UseCommand = "vcl.use " + routingName
	return plan, nil
}

// Build creates routing VCL, cmdfile plan, and managed VCL keep set in one call.
func (b *Builder) Build(configs []VCLConfig, routingPath string, opts ...CmdfileOption) (BuildResult, error) {
	routingVCL, err := b.BuildRoutingVCL(configs)
	if err != nil {
		return BuildResult{}, err
	}
	plan, err := b.BuildCmdfilePlan(configs, routingPath, opts...)
	if err != nil {
		return BuildResult{}, err
	}
	keep, err := b.ManagedVCLNames(configs, []byte(routingVCL))
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{RoutingVCL: routingVCL, CmdfilePlan: plan, ManagedVCLNames: keep}, nil
}
