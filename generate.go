package routebuilder

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

type routingVCLData struct {
	Configs   []VCLConfig
	Timestamp string
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

var routingTmpl = template.Must(template.New("routing").Funcs(template.FuncMap{
	"hostCond": func(hosts []string) string {
		parts := make([]string, len(hosts))
		for i, h := range hosts {
			parts[i] = hostnameToVCL(h)
		}
		return strings.Join(parts, " || ")
	},
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
{{- range .Configs}}
    if ({{hostCond .Hostnames}}) {
        return(vcl({{$.PrefixLabel}}{{.Name}}-{{$.Timestamp}}));
    }
{{- end}}
    return(synth(404, "No route matched"));
}
`))

type routingVCLDataInternal struct {
	Configs     []VCLConfig
	Timestamp   string
	PrefixLabel string
}

// BuildRoutingVCL generates the routing VCL content for the given configurations.
// It validates route names, hostnames, and timestamp because those values are
// embedded into VCL object names and host match expressions.
func BuildRoutingVCL(configs []VCLConfig, timestamp string) (string, error) {
	if err := validateGenerationTimestamp(timestamp); err != nil {
		return "", err
	}
	for i, cfg := range configs {
		if err := validateRoutingConfig(cfg); err != nil {
			return "", fmt.Errorf("route %d: %w", i, err)
		}
	}
	var buf bytes.Buffer
	data := routingVCLDataInternal{
		Configs:     configs,
		Timestamp:   timestamp,
		PrefixLabel: PrefixLabel,
	}
	if err := routingTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// quoteCLIArg wraps a Varnish CLI argument in double quotes, escaping backslash
// and double-quote characters. Prevents malformed cmdfiles when paths contain spaces.
func quoteCLIArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// BuildCmdfile generates the cmdfile content for the given configurations. It
// validates route names, vclPath, TLS entries, and timestamp because those
// values are embedded into Varnish CLI object names and commands.
func BuildCmdfile(configs []VCLConfig, routingPath string, timestamp string) (string, error) {
	return BuildCmdfileWithExisting(configs, routingPath, timestamp, nil)
}
