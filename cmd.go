package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/varnish/varnish-go/adm"
)

// Version is set at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("route-builder", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	cmdfileOut := fs.String("cmdfile", "/etc/varnish/cmdfile", "output path for cmdfile (- for stdout, none to suppress)")
	vclfileOut := fs.String("vclfile", "/etc/varnish/routing.vcl", "output path for routing VCL (- for stdout, none to suppress)")
	yamlfileOut := fs.String("yamlfile", "", "write parsed config as YAML (- for stdout); incompatible with YAML input")
	instanceName := fs.String("n", "", "Varnish instance name (-n argument to varnishd)")
	doReload := fs.Bool("reload", false, "reload the running Varnish instance after generating files")
	reloadTimeout := fs.Duration("timeout", 30*time.Second, "wall-clock deadline for the entire -reload operation (covers connect through all stages)")
	testRouteURL := fs.String("test-route", "", "print which route handles this URL's hostname")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: route-builder [options] routes.yaml\n")
		fmt.Fprintf(stderr, "       route-builder [options] file.vcl [file.vcl ...]\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, "route-builder "+Version)
		return 0
	}

	files, err := expandGlobs(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fs.Usage()
		return 1
	}

	var configs []VCLConfig
	switch {
	case len(files) == 1 && isYAMLFile(files[0]):
		if *yamlfileOut != "" {
			fmt.Fprintln(stderr, "error: -yamlfile is incompatible with YAML input")
			return 1
		}
		var err error
		configs, err = parseRoutes(files[0])
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", files[0], err)
			return 1
		}

	case allVCLFiles(files):
		for _, f := range files {
			cfg, err := parseVCL(f)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", f, err)
				return 1
			}
			configs = append(configs, cfg)
		}

	default:
		fmt.Fprintln(stderr, "error: arguments must be a single .yaml/.yml file or one or more .vcl files")
		return 1
	}

	if err := checkDuplicateNames(configs); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := checkDuplicateHostnames(configs); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *testRouteURL != "" {
		u, err := url.Parse(*testRouteURL)
		if err != nil {
			fmt.Fprintf(stderr, "invalid URL %q: %v\n", *testRouteURL, err)
			return 1
		}
		host := u.Hostname()
		if host == "" {
			fmt.Fprintf(stderr, "no hostname in URL %q\n", *testRouteURL)
			return 1
		}
		cfg := findRoute(host, configs)
		if cfg == nil {
			fmt.Fprintf(stdout, "host %q: no route matched\n", host)
			return 1
		}
		fmt.Fprintf(stdout, "host %q -> %s (%s)\n", host, cfg.Name, cfg.VclPath)
		return 0
	}

	if (*vclfileOut == "-" || *vclfileOut == "none") && *cmdfileOut != "none" {
		fmt.Fprintln(stderr, "error: -vclfile - or -vclfile none requires -cmdfile none")
		return 1
	}

	if *yamlfileOut != "" {
		data, err := marshalRoutes(configs)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeOutput(*yamlfileOut, string(data), stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	timestamp := newTimestamp()

	if *vclfileOut != "none" {
		content, err := buildRoutingVCL(configs, timestamp)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeOutput(*vclfileOut, content, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *vclfileOut != "-" {
			fmt.Fprintf(stderr, "wrote %s\n", *vclfileOut)
		}
	}

	if *cmdfileOut != "none" {
		routingPath := *vclfileOut
		if *vclfileOut != "-" && *vclfileOut != "none" {
			var err error
			routingPath, err = filepath.Abs(*vclfileOut)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		content, err := buildCmdfile(configs, routingPath, timestamp)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeOutput(*cmdfileOut, content, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *cmdfileOut != "-" {
			fmt.Fprintf(stderr, "wrote %s\n", *cmdfileOut)
		}
	}

	// Warn if -timeout is set explicitly without -reload (it has no effect).
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" && !*doReload {
			fmt.Fprintln(stderr, "warning: -timeout has no effect without -reload")
		}
	})

	if *doReload {
		ctx, cancel := context.WithTimeout(context.Background(), *reloadTimeout)
		defer cancel()
		conn, err := adm.Connect(ctx, *instanceName)
		if err != nil {
			fmt.Fprintf(stderr, "connect to varnish: %v\n", err)
			return 1
		}
		defer conn.Close()
		if err := reloadVarnish(ctx, conn, configs, timestamp, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "reloaded %d route(s)\n", len(configs))
	}

	return 0
}

func newTimestamp() string {
	now := time.Now()
	return now.Format("2006-01-02T15-04-05") + fmt.Sprintf("_%09d", now.Nanosecond())
}

func findRoute(host string, configs []VCLConfig) *VCLConfig {
	host = strings.ToLower(host)
	for i := range configs {
		for _, h := range configs[i].Hostnames {
			if hostnamesOverlap(host, h) {
				return &configs[i]
			}
		}
	}
	return nil
}
