package routebuilder

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
        return(vcl(rb-label-{{.Name}}-{{$.Timestamp}}));
    }
{{- end}}
    return(synth(404, "No route matched"));
}
`))

var cmdfileTmpl = template.Must(template.New("cmdfile").Funcs(template.FuncMap{
	"q": quoteCLIArg,
}).Parse(
	`{{.TLSLines}}{{range .Configs}}vcl.load rb-vcl-{{.Name}}-{{$.Timestamp}} {{q .VclPath}}
vcl.label rb-label-{{.Name}}-{{$.Timestamp}} rb-vcl-{{.Name}}-{{$.Timestamp}}
{{end}}vcl.load rb-routing-{{.Timestamp}} {{q .RoutingPath}}
vcl.use rb-routing-{{.Timestamp}}
`))

type cmdfileData struct {
	Configs     []VCLConfig
	RoutingPath string
	Timestamp   string
	TLSLines    string
}

func buildRoutingVCL(configs []VCLConfig, timestamp string) (string, error) {
	var buf bytes.Buffer
	if err := routingTmpl.Execute(&buf, routingVCLData{Configs: configs, Timestamp: timestamp}); err != nil {
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

func buildCmdfile(configs []VCLConfig, routingPath string, timestamp string) (string, error) {
	var lines []string
	for _, cfg := range configs {
		for i, t := range cfg.TLS {
			certID := fmt.Sprintf("%s%s-%d-%s", prefixCert, cfg.Name, i, timestamp)
			if t.PEM != "" {
				lines = append(lines, "tls.cert.load "+certID+" "+quoteCLIArg(t.PEM))
			} else {
				lines = append(lines, "tls.cert.load "+certID+" "+quoteCLIArg(t.Cert)+" -k "+quoteCLIArg(t.Key))
			}
		}
	}
	var tlsLines string
	if len(lines) > 0 {
		tlsLines = strings.Join(lines, "\n") + "\ntls.cert.commit\n"
	}
	var buf bytes.Buffer
	if err := cmdfileTmpl.Execute(&buf, cmdfileData{
		Configs:     configs,
		RoutingPath: routingPath,
		Timestamp:   timestamp,
		TLSLines:    tlsLines,
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func WriteFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".route-builder-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, writeErr := f.WriteString(content)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(tmp)
		return writeErr
	}
	if syncErr != nil {
		os.Remove(tmp)
		return syncErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func writeOutput(path, content string, stdout io.Writer) error {
	if path == "-" {
		_, err := fmt.Fprint(stdout, content)
		return err
	}
	return WriteFileAtomic(path, content)
}
