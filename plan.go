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

// WithEntryRenderer configures the renderer used by BuildVCLProgram.
func WithEntryRenderer(renderer EntryRenderer) BuilderOption {
	return func(b *Builder) {
		b.entryRenderer = renderer
	}
}

// WithEntryNameSpec configures the generated entry VCL object name used by
// BuildVCLProgram. If spec.Content is empty, BuildVCLProgram uses the rendered
// entry VCL bytes as the naming content.
func WithEntryNameSpec(spec EntityNameSpec) BuilderOption {
	return func(b *Builder) {
		b.entryName = spec
	}
}

// Builder generates routing VCL, cmdfiles, cleanup plans, and live reloads
// using a configured naming strategy.
type Builder struct {
	namer         EntityNamer
	entryRenderer EntryRenderer
	entryName     EntityNameSpec
}

// NewBuilder constructs a Builder. By default it uses ConstantNamer(NewTimestamp()).
func NewBuilder(opts ...BuilderOption) *Builder {
	b := &Builder{namer: ConstantNamer(NewTimestamp()), entryRenderer: defaultEntryRenderer, entryName: EntityNameSpec{Prefix: PrefixRouting}}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// EntityNameSpec describes a generated Varnish entity name.
type EntityNameSpec struct {
	// Name is an optional exact object name. If set, Prefix/Suffix/Content are ignored.
	Name string

	// Prefix and Suffix are combined with the Builder's EntityNamer output.
	Prefix string
	Suffix string

	// Content is passed to the EntityNamer. MD5Namer hashes this.
	Content []byte
}

// VCLTargetSpec describes a VCL object and optional label loaded by a VCL program.
type VCLTargetSpec struct {
	VCLName   EntityNameSpec
	VCLPath   string
	LabelName *EntityNameSpec
	Hostnames []string
}

// EntryVCLSpec describes the entry VCL loaded and activated by a VCL program.
type EntryVCLSpec struct {
	Name    EntityNameSpec
	VCLPath string
	Content []byte
}

// VCLProgramSpec describes a complete VCL program: targets plus active entry VCL.
type VCLProgramSpec struct {
	Targets []VCLTargetSpec
	Entry   EntryVCLSpec
}

// ResolvedVCLTarget is a target after entity names have been resolved.
type ResolvedVCLTarget struct {
	VCLName   string
	LabelName string
	VCLPath   string
	Hostnames []string
}

// ResolvedEntryVCL is an entry VCL after its entity name has been resolved.
type ResolvedEntryVCL struct {
	Name    string
	VCLPath string
	Content []byte
}

// EntryRenderer renders entry VCL content from resolved VCL target names.
type EntryRenderer func([]ResolvedVCLTarget) ([]byte, error)

type cmdfileOptions struct {
	existingVCLNames map[string]bool
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
		if opts.existingVCLNames == nil {
			opts.existingVCLNames = make(map[string]bool, len(names))
		}
		for _, name := range names {
			opts.existingVCLNames[name] = true
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

// ProgramResult contains all outputs from BuildVCLProgram.
type ProgramResult struct {
	EntryVCL        string
	CmdfilePlan     CmdfilePlan
	ManagedVCLNames map[string]bool
	Targets         []ResolvedVCLTarget
	Entry           ResolvedEntryVCL
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

func (b *Builder) vclProgramTargets(configs []VCLConfig) ([]VCLTargetSpec, error) {
	targets := make([]VCLTargetSpec, len(configs))
	for i, cfg := range configs {
		content, err := b.routeContent(cfg)
		if err != nil {
			return nil, err
		}
		labelName := EntityNameSpec{Prefix: PrefixLabel + cfg.Name + "-", Content: content}
		targets[i] = VCLTargetSpec{
			VCLName:   EntityNameSpec{Prefix: PrefixVCL + cfg.Name + "-", Content: content},
			VCLPath:   cfg.VclPath,
			LabelName: &labelName,
			Hostnames: cfg.Hostnames,
		}
	}
	return targets, nil
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

func (b *Builder) tlsCertLoadCommand(cfg VCLConfig, index int, t TLSEntry) (string, error) {
	certID, err := b.tlsCertID(cfg, index, t)
	if err != nil {
		return "", fmt.Errorf("route %q tls entry %d: %w", cfg.Name, index, err)
	}
	if t.PEM != "" {
		return "tls.cert.load " + certID + " " + quoteCLIArg(t.PEM), nil
	}
	return "tls.cert.load " + certID + " " + quoteCLIArg(t.Cert) + " -k " + quoteCLIArg(t.Key), nil
}

func (b *Builder) tlsCertLoadCommands(cfg VCLConfig) ([]string, error) {
	commands := make([]string, 0, len(cfg.TLS))
	for i, t := range cfg.TLS {
		command, err := b.tlsCertLoadCommand(cfg, i, t)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func (b *Builder) resolveEntityName(spec EntityNameSpec) (string, error) {
	if spec.Name != "" {
		return spec.Name, nil
	}
	name, err := b.namer.Name(spec.Prefix, spec.Content)
	if err != nil {
		return "", err
	}
	return name + spec.Suffix, nil
}

func (b *Builder) entityNameNeedsContent(spec EntityNameSpec) bool {
	return spec.Name == "" && spec.Content == nil && namerRequiresContent(b.namer)
}

func (b *Builder) resolveEntityNameWithContent(spec EntityNameSpec, content []byte) (string, error) {
	if b.entityNameNeedsContent(spec) {
		spec.Content = content
	}
	return b.resolveEntityName(spec)
}

// ResolveVCLTargets resolves target VCL and label object names.
func (b *Builder) ResolveVCLTargets(targets []VCLTargetSpec) ([]ResolvedVCLTarget, error) {
	resolved := make([]ResolvedVCLTarget, len(targets))
	for i, target := range targets {
		var content []byte
		var err error
		if b.entityNameNeedsContent(target.VCLName) || (target.LabelName != nil && b.entityNameNeedsContent(*target.LabelName)) {
			content, err = readRequiredContent(target.VCLPath, fmt.Sprintf("target %d VCLPath", i))
			if err != nil {
				return nil, err
			}
		}
		vclName, err := b.resolveEntityNameWithContent(target.VCLName, content)
		if err != nil {
			return nil, fmt.Errorf("target %d vcl name: %w", i, err)
		}
		var labelName string
		if target.LabelName != nil {
			labelName, err = b.resolveEntityNameWithContent(*target.LabelName, content)
			if err != nil {
				return nil, fmt.Errorf("target %d label name: %w", i, err)
			}
		}
		resolved[i] = ResolvedVCLTarget{VCLName: vclName, LabelName: labelName, VCLPath: target.VCLPath, Hostnames: target.Hostnames}
	}
	return resolved, nil
}

func (b *Builder) resolveEntry(entry EntryVCLSpec) (ResolvedEntryVCL, error) {
	if entry.VCLPath == "" {
		return ResolvedEntryVCL{}, fmt.Errorf("entry VCLPath is required")
	}
	if b.entityNameNeedsContent(entry.Name) {
		content, err := readRequiredContent(entry.VCLPath, "entry VCLPath")
		if err != nil {
			return ResolvedEntryVCL{}, err
		}
		entry.Name.Content = content
		entry.Content = content
	}
	name, err := b.resolveEntityName(entry.Name)
	if err != nil {
		return ResolvedEntryVCL{}, fmt.Errorf("entry name: %w", err)
	}
	return ResolvedEntryVCL{Name: name, VCLPath: entry.VCLPath, Content: entry.Content}, nil
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

func managedProgramVCLNames(targets []ResolvedVCLTarget, entry ResolvedEntryVCL) map[string]bool {
	keep := map[string]bool{entry.Name: true}
	for _, target := range targets {
		keep[target.VCLName] = true
		if target.LabelName != "" {
			keep[target.LabelName] = true
		}
	}
	return keep
}

func existingVCLName(opts cmdfileOptions, name string) bool {
	return opts.existingVCLNames != nil && opts.existingVCLNames[name]
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

	targets, err := b.vclProgramTargets(configs)
	if err != nil {
		return CmdfilePlan{}, err
	}
	result, err := b.BuildVCLProgram(targets, routingPath, options...)
	if err != nil {
		return CmdfilePlan{}, err
	}
	plan := result.CmdfilePlan
	for _, cfg := range configs {
		commands, err := b.tlsCertLoadCommands(cfg)
		if err != nil {
			return CmdfilePlan{}, err
		}
		plan.TLSCommands = append(plan.TLSCommands, commands...)
	}
	if len(plan.TLSCommands) > 0 {
		plan.TLSCommands = append(plan.TLSCommands, "tls.cert.commit")
	}
	return plan, nil
}

// BuildVCLProgramPlan creates the Varnish CLI command plan for an arbitrary VCL program.
func (b *Builder) BuildVCLProgramPlan(spec VCLProgramSpec, options ...CmdfileOption) (CmdfilePlan, error) {
	targets, err := b.ResolveVCLTargets(spec.Targets)
	if err != nil {
		return CmdfilePlan{}, err
	}
	entry, err := b.resolveEntry(spec.Entry)
	if err != nil {
		return CmdfilePlan{}, err
	}
	opts := applyCmdfileOptions(options)
	return vclProgramPlanFromResolved(targets, entry, opts)
}

func vclProgramPlanFromResolved(targets []ResolvedVCLTarget, entry ResolvedEntryVCL, opts cmdfileOptions) (CmdfilePlan, error) {
	var plan CmdfilePlan

	for _, target := range targets {
		if target.VCLPath == "" {
			return CmdfilePlan{}, fmt.Errorf("target %q: VCLPath is required", target.VCLName)
		}
		if !existingVCLName(opts, target.VCLName) {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.load "+target.VCLName+" "+quoteCLIArg(target.VCLPath))
		}
		if target.LabelName != "" && !existingVCLName(opts, target.LabelName) {
			plan.RouteCommands = append(plan.RouteCommands, "vcl.label "+target.LabelName+" "+target.VCLName)
		}
	}
	if !existingVCLName(opts, entry.Name) {
		plan.RoutingCommands = append(plan.RoutingCommands, "vcl.load "+entry.Name+" "+quoteCLIArg(entry.VCLPath))
	}
	plan.UseCommand = "vcl.use " + entry.Name
	return plan, nil
}

// BuildVCLProgram renders and plans an arbitrary VCL program using the builder's EntryRenderer.
func (b *Builder) BuildVCLProgram(targets []VCLTargetSpec, entryPath string, options ...CmdfileOption) (ProgramResult, error) {
	if b.entryRenderer == nil {
		return ProgramResult{}, fmt.Errorf("entry renderer is required")
	}
	resolvedTargets, err := b.ResolveVCLTargets(targets)
	if err != nil {
		return ProgramResult{}, err
	}
	entryVCL, err := b.entryRenderer(resolvedTargets)
	if err != nil {
		return ProgramResult{}, err
	}
	entrySpec := EntryVCLSpec{Name: b.entryName, VCLPath: entryPath, Content: entryVCL}
	if entrySpec.Name.Name == "" && entrySpec.Name.Content == nil {
		entrySpec.Name.Content = entryVCL
	}
	entry, err := b.resolveEntry(entrySpec)
	if err != nil {
		return ProgramResult{}, err
	}
	plan, err := vclProgramPlanFromResolved(resolvedTargets, entry, applyCmdfileOptions(options))
	if err != nil {
		return ProgramResult{}, err
	}
	return ProgramResult{
		EntryVCL:        string(entryVCL),
		CmdfilePlan:     plan,
		ManagedVCLNames: managedProgramVCLNames(resolvedTargets, entry),
		Targets:         resolvedTargets,
		Entry:           entry,
	}, nil
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
