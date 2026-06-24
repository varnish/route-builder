# route-builder

[![CI](https://github.com/varnish/route-builder/actions/workflows/ci.yml/badge.svg)](https://github.com/varnish/route-builder/actions/workflows/ci.yml)

Run multiple sites on a single [Varnish Cache](https://varnish.org/) instance — each with its own VCL, its own TLS certificates, and zero downtime when configuration changes.

Give every domain its own config file:

```yaml
# routes.yaml
routes:
  - name: foo_service
    hostnames:
      - foo.com
      - www.foo.com
    vclPath: /etc/varnish/foo.vcl
    tls:
      - cert: /etc/ssl/foo.crt
        key:  /etc/ssl/foo.key

  - name: bar_service
    hostnames:
      - bar.com
    vclPath: /etc/varnish/bar.vcl
```

Then generate and apply:

```bash
route-builder -reload routes.yaml
# wrote /etc/varnish/routing.vcl
# wrote /etc/varnish/cmdfile
# reloaded 2 route(s)
```

## Usage

```
route-builder [options] routes.yaml
route-builder [options] file.vcl [file.vcl ...]
```

Pass a single `.yaml`/`.yml` file, or one or more `.vcl` files with embedded routing config (see [VCL frontmatter](#vcl-frontmatter)).

## Options

| Flag | Default | Description |
|---|---|---|
| `-cmdfile PATH` | `/etc/varnish/cmdfile` | Output path for the Varnish cmdfile. Use `-` for stdout, `none` to suppress. |
| `-vclfile PATH` | `/etc/varnish/routing.vcl` | Output path for the routing VCL. Use `-` for stdout, `none` to suppress. |
| `-yamlfile PATH` | _(unset)_ | Write parsed config as YAML. Use `-` for stdout. Incompatible with YAML input. |
| `-reload` | false | After writing files, reload the running Varnish instance over the admin protocol. |
| `-timeout DURATION` | `30s` | Wall-clock deadline for the entire `-reload` operation. No effect without `-reload`. |
| `-n NAME` | _(default instance)_ | Varnish instance name (the `-n` argument passed to `varnishd`). |
| `-test-route URL` | _(unset)_ | Print which route handles this URL's hostname. Skips file output. |
| `-version` | | Print version and exit. |

**Note:** `-vclfile -` requires `-cmdfile none` — the cmdfile references the routing VCL by path, so both can't be written to stdout simultaneously.

## Installation

```bash
go install github.com/varnish/route-builder@latest
```

Or build from source:

```bash
git clone https://github.com/varnish/route-builder
cd route-builder
go build -o route-builder ./cmd/route-builder
```

## Library

The core logic is importable as a Go library:

```go
import rb "github.com/varnish/route-builder"
```

The binary lives at `cmd/route-builder` and is a thin wrapper around `rb.Run()`.

A systemd service unit is provided in [`contrib/systemd/`](contrib/systemd/) for production deployments.

## How it works

### Per-domain configuration

Each route maps one or more hostnames to a VCL file. route-builder validates the config (no duplicate names, no overlapping hostnames), then generates two files for Varnish:

- **routing VCL** — dispatches incoming requests to the right VCL label by hostname
- **cmdfile** — Varnish startup script that loads all VCL files and activates the router

```
routes.yaml  (or foo.vcl bar.vcl ...)
      │
      ▼
 []VCLConfig  ──────────────────┐
      │                         │
      ▼                         ▼
 routing.vcl                 cmdfile
      │                         │
      └──────────┬──────────────┘
                 ▼
        varnishd -f cmdfile    (or -reload to hot-swap)
```

### VCL frontmatter

If you'd rather keep routing config alongside the VCL itself, embed it as a comment block at the very top of each VCL file:

```vcl
/* routing
name: foo_service
hostnames:
  - foo.com
  - www.foo.com
tls:
  - cert: /etc/ssl/foo.crt
    key:  /etc/ssl/foo.key
*/
vcl 4.1;

backend default { .host = "10.0.0.1"; .port = "80"; }
```

Then pass the VCL files directly:

```bash
route-builder /etc/varnish/services/*.vcl
```

Use `-yamlfile` to export the parsed frontmatter to a YAML manifest if you want to migrate to the YAML workflow later.

### TLS

Each route can carry one or more TLS certificates — either a PEM bundle or a separate cert + key pair:

```yaml
tls:
  - pem: /etc/ssl/bundle.pem          # cert + key in one file
  - cert: /etc/ssl/foo.crt            # or separate files
    key:  /etc/ssl/foo.key
```

Relative paths are resolved from the YAML file's directory (or the VCL file's directory for frontmatter).

### Hostname matching

Wildcards are supported in individual domain segments:

```yaml
hostnames:
  - foo.com
  - www.foo.com
  - "*.api.foo.com"
```

Ports are stripped automatically. TLS requests use the SNI hostname.

### Live reload

`-reload` applies new configuration to a running Varnish instance without dropping traffic. route-builder performs an 8-stage atomic operation over the admin protocol:

1. Snapshot existing route-builder VCL names and TLS cert IDs
2. Load each per-route VCL and create its timestamped label
3. Compile and load the routing VCL inline
4. Load new TLS certificates
5. Commit new TLS certificates
6. Activate new routing VCL (`vcl.use`) — point of no return
7. Discard old VCL objects
8. Discard old TLS certificates

If any step before stage 6 fails, staged changes are rolled back and the running instance is left untouched. The cmdfile and routing VCL are written to disk first, so Varnish can replay them on a cold restart.

```bash
# Generate files and reload in one command
route-builder -reload routes.yaml

# Reload only, skip writing files
route-builder -reload -cmdfile none -vclfile none routes.yaml

# Debug: see what routing VCL would be generated
route-builder -vclfile - -cmdfile none routes.yaml

# Debug: check which route handles a hostname
route-builder -test-route https://www.foo.com routes.yaml
```
