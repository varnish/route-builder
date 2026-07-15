package routebuilder

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

type routingVCLData struct {
	Routes []routingRouteData
}

type routingRouteData struct {
	Hostnames []string
	LabelName string
}

func hostnameToVCL(h string) string {
	if !strings.Contains(h, "*") {
		return fmt.Sprintf(`req.http.host == %q`, h)
	}
	segs := strings.Split(h, ".")
	for i, seg := range segs {
		if seg == "*" {
			segs[i] = `[^.]+`
		} else {
			segs[i] = regexp.QuoteMeta(seg)
		}
	}
	return fmt.Sprintf(`req.http.host ~ "^%s$"`, strings.Join(segs, `\.`))
}

func hostCondition(hosts []string) string {
	parts := make([]string, len(hosts))
	for i, h := range hosts {
		parts[i] = hostnameToVCL(h)
	}
	return strings.Join(parts, " || ")
}

var routingTmpl = template.Must(template.New("routing").Funcs(template.FuncMap{
	"hostCond": hostCondition,
}).Parse(`vcl 4.1;

import tls;

backend default none;

sub vcl_recv {
    if (tls.is_tls()) {
        set req.http.host = tls.authority();
    } else if (req.http.host ~ "^\[") {
        set req.http.host = regsub(req.http.host, "^\[([^\]]+)\](:\d+)?$", "[\1]");
    } else {
        set req.http.host = regsub(req.http.host, ":\d+$", "");
    }
{{- range .Routes}}
    if ({{hostCond .Hostnames}}) {
        return(vcl({{.LabelName}}));
    }
{{- end}}
    return(synth(404, "No route matched"));
}
`))

// BuildRoutingVCL generates the routing VCL content for the given configurations.
func (b *Builder) BuildRoutingVCL(configs []VCLConfig) (string, error) {
	for i, cfg := range configs {
		if err := validateRoutingConfig(cfg); err != nil {
			return "", fmt.Errorf("route %d: %w", i, err)
		}
	}
	targets, err := b.vclProgramTargets(configs)
	if err != nil {
		return "", err
	}
	resolved, err := b.ResolveVCLTargets(targets)
	if err != nil {
		return "", err
	}
	entryVCL, err := b.entryRenderer(resolved)
	return string(entryVCL), err
}

func defaultEntryRenderer(targets []ResolvedVCLTarget) ([]byte, error) {
	routes := make([]routingRouteData, len(targets))
	for i, target := range targets {
		if len(target.Hostnames) == 0 {
			return nil, fmt.Errorf("target %q: hostnames are required for default entry renderer", target.VCLPath)
		}
		routes[i] = routingRouteData{Hostnames: target.Hostnames, LabelName: target.LabelName}
	}
	return renderRoutingVCL(routes)
}

func renderRoutingVCL(routes []routingRouteData) ([]byte, error) {
	var buf bytes.Buffer
	data := routingVCLData{Routes: routes}
	if err := routingTmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// quoteCLIArg wraps a Varnish CLI argument in double quotes, escaping backslash
// and double-quote characters. Prevents malformed cmdfiles when paths contain spaces.
func quoteCLIArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
