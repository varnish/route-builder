# Agent Context — route-builder

## What this is

Go module and CLI for managing Varnish Cache routing. The module root is the importable `github.com/varnish/route-builder` library (`package routebuilder`); the command-line binary lives in `cmd/route-builder`.

The tool reads per-service VCL files with YAML frontmatter, or a YAML routes manifest, then generates the Varnish cmdfile and routing VCL needed for host-based dispatch. It can also reload a running Varnish instance via the admin socket.

---

## File map

| File | Role |
|---|---|
| `routebuilder.go` | Package docs plus exported Varnish object prefix constants. |
| `config.go` | Public config types plus validation helpers (`ValidateConfigs`, duplicate hostname/name checks, TLS validation). |
| `parse.go` | Frontmatter and routes-manifest parsing (`ParseVCL`, `ParseRoutes`, `MarshalRoutes`). |
| `helpers.go` | Public CLI/library helpers (`ExpandGlobs`, file-extension helpers, `NewTimestamp`, `FindRoute`). |
| `generate.go` | Public VCL/cmdfile template rendering (`BuildRoutingVCL`, `BuildCmdfile`). |
| `reload.go` | Public 8-stage live reload via Varnish admin protocol (`ReloadVarnish`) plus rollback helper. |
| `write.go` | Public output helpers (`WriteFileAtomic`, `WriteOutput`). |
| `cmd/route-builder/main.go` | CLI entry point, flag parsing, input detection, and orchestration. |
| `*_test.go` | Library tests; `reload_test.go` spins up real Varnish instances via `vtest`. |
| `cmd/route-builder/*_test.go` | CLI tests. |
| `testdata/` | Example VCL files, generated output fixtures, and TLS cert/key for tests. |

---

## Install/build paths

Because the root package is now a library, install and release builds must target the command package:

```bash
go install github.com/varnish/route-builder/cmd/route-builder@latest
go build -o route-builder ./cmd/route-builder
```

Do not use `go install github.com/varnish/route-builder@latest` or `go build .` when the expected artifact is the CLI binary.

---

## Key types

```go
type TLSEntry struct {
    PEM  string `yaml:"pem"`  // single-file PEM bundle
    Key  string `yaml:"key"`  // key half of cert+key pair
    Cert string `yaml:"cert"` // cert half of cert+key pair
}

type VCLConfig struct {
    Name       string     `yaml:"name"`
    Hostnames  []string   `yaml:"hostnames"`
    TLS        []TLSEntry `yaml:"tls"`
    VclPath    string     `yaml:"vclPath"` // path Varnish loads (cmdfile + adm.VCLLoad)
    SourceFile string     `yaml:"-"`       // source used in error messages / duplicate detection
}
```

`VclPath` vs `SourceFile` is important:

- VCL input: both are set to the same absolute VCL file path.
- YAML input: `SourceFile` is the YAML file; `VclPath` is the route's VCL path, resolved relative to the YAML file when necessary.
- `ReloadVarnish` defensively errors if `VclPath` is empty after validation.

---

## CLI flag conventions

| Special value | Meaning |
|---|---|
| `-` | stdout |
| `none` | suppress that output entirely |

`-vclfile -` or `-vclfile none` requires `-cmdfile none`, because the cmdfile references the routing VCL by path.

`-timeout DURATION` only affects `-reload`; if set without `-reload`, the CLI warns that it has no effect.

---

## Input detection

The CLI handles input extension-based in `cmd/route-builder/main.go`:

- one `.yaml` or `.yml` file → `routebuilder.ParseRoutes`; incompatible with `-yamlfile`
- one or more `.vcl` files → `routebuilder.ParseVCL` for each; `-yamlfile` can dump the merged config
- mixed or unknown input → error

Positional args are passed through `routebuilder.ExpandGlobs` before the switch. Any arg containing `*`, `?`, or `[` is expanded with `filepath.Glob`; no match is a hard error. This lets systemd `ExecStart` lines use globs without a shell wrapper.

---

## VCL frontmatter format

The first line of a VCL file must be exactly `/* routing`, followed by YAML, closed by `*/`:

```vcl
/* routing
name: foo_service
hostnames:
  - foo.com
  - www.foo.com
tls:
  - pem: /etc/ssl/foo.pem
*/
vcl 4.1;
```

Parsed by `extractFrontMatter` → `unmarshalConfig` → `validateConfig`. Relative TLS paths are resolved relative to the VCL file's directory by `resolveTLSPaths`.

---

## YAML routes format

```yaml
routes:
  - name: foo_service
    hostnames:
      - foo.com
      - www.foo.com
    vclPath: /etc/varnish/foo.vcl
    tls:
      - pem: /etc/ssl/foo.pem
      - cert: /etc/ssl/foo.crt
        key:  /etc/ssl/foo.key
```

Parsed by `ParseRoutes`. File existence is checked for `vclPath` and all TLS paths after resolution. `ParseVCL` does not check TLS file existence; VCL files are treated as already-deployed artifacts.

---

## Generated outputs

### Routing VCL

`BuildRoutingVCL` renders `routingTmpl`:

- imports the `tls` VMOD
- uses SNI (`tls.authority()`) for TLS requests
- strips ports from non-TLS Host headers, including IPv6 bracket form
- dispatches with `return(vcl(rb-label-<name>-<timestamp>))`
- falls through to `synth(404, "No route matched")`

### Cmdfile

`BuildCmdfile` renders `cmdfileTmpl` in Varnish-safe dependency order:

1. `tls.cert.load rb-cert-<name>-<idx>-<timestamp> ...` lines
2. `tls.cert.commit` when any TLS certs were loaded
3. per-route `vcl.load rb-vcl-...` and `vcl.label rb-label-... rb-vcl-...`
4. `vcl.load rb-routing-<timestamp> <routingPath>`
5. `vcl.use rb-routing-<timestamp>`

---

## Reload flow

`ReloadVarnish` performs an 8-stage live reload; failures before stage 6 roll back staged changes:

| Stage | Action |
|---|---|
| 1 | Snapshot existing route-builder VCL names plus existing `rb-cert-*` cert IDs. |
| 2 | Load each per-route VCL and create its timestamped label. |
| 3 | Compile and load the routing VCL inline. |
| 4 | Load TLS certificates with explicit `rb-cert-*` IDs. |
| 5 | Commit TLS certificates. |
| 6 | Activate the new routing VCL with `vcl.use` — point of no return. |
| 7 | Discard old route-builder VCL objects from the snapshot. |
| 8 | Discard old `rb-cert-*` certs and commit cert cleanup. |

---

## Wildcard hostnames

Hostnames may contain `*` only as a whole segment, e.g. `*.example.com` or `foo.*.com`. Partial wildcards like `fo*.com` are rejected.

- Overlap detection (`hostnamesOverlap`) treats two hostnames as overlapping when they have equal segment counts and no segment pair is definitely different.
- VCL generation (`hostnameToVCL`) emits exact equality for exact hostnames and anchored regexes for wildcard hostnames.
- `FindRoute` uses the same overlap logic for `-test-route`.
- YAML test helpers should quote wildcard hostnames, since `*` is a YAML alias indicator.

---

## Test infrastructure

Run all packages from the repo root:

```bash
go test ./...
```

`reload_test.go` and some CLI tests use `github.com/varnish/varnish-go/vtest`, which starts real `varnishd` processes. `varnishd` must be in `PATH` for those tests to pass.

The test cert files in `testdata/test.crt` and `testdata/test.key` are self-signed fixtures for TLS reload tests.

---

## Timestamp format

`NewTimestamp` returns:

```text
2006-01-02T15-04-05_000000000
```

The timestamp is used in all route-builder Varnish object names (`rb-vcl-*`, `rb-label-*`, `rb-routing-*`, `rb-cert-*`). Nanosecond precision avoids collisions on rapid successive runs.

---

## varnish-go API notes

This project uses `github.com/varnish/varnish-go/adm` v0.1.0:

- admin methods take `context.Context` as their first argument
- `adm.Connect(ctx, name)` returns `*adm.Conn`
- `TLSCertLoad(ctx, certFile, opts...)` accepts `adm.TLSWithCertID` and `adm.TLSWithKeyFile`

---

## Coverage notes

Genuinely hard-to-reach gaps include:

- `BuildRoutingVCL` / `BuildCmdfile` error paths from precompiled templates
- `WriteFileAtomic` write/close error paths without OS-level fd injection
- rollback warning paths that require Varnish admin operations to fail mid-rollback
- CLI `main()` beyond testing `run()`
