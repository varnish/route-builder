# Agent Context — route-builder

## What this is

Go CLI that manages Varnish Cache routing. Reads per-service VCL files (with YAML frontmatter) or a YAML routes manifest, then generates the Varnish cmdfile and routing VCL needed for host-based dispatch, and optionally reloads a running Varnish instance via its admin socket.

---

## File map

| File | Role |
|---|---|
| `main.go` | Entry point — calls `run(os.Args[1:], ...)` and exits |
| `cmd.go` | Unified command handler: flag parsing, input detection, orchestration |
| `config_builder.go` | All config types, parsing, validation (`parseVCL`, `parseRoutes`, `validateConfig`, `checkDuplicate*`, `resolveTLSPaths`, `marshalRoutes`, `expandGlobs`, extension helpers) |
| `generate.go` | VCL/cmdfile template rendering (`buildRoutingVCL`, `buildCmdfile`) and atomic file writes (`writeFileAtomic`, `writeOutput`) |
| `reload.go` | 8-stage live reload via Varnish admin protocol (`reloadVarnish`, `vclRollback`) |
| `*_test.go` | Tests; `reload_test.go` spins up real Varnish instances via `vtest` |
| `testdata/` | Example VCL files (with frontmatter), generated cmdfile/routing VCL, and TLS cert+key for tests |

---

## Key types

```go
type TLSEntry struct {
    PEM  string `yaml:"pem"`           // single-file PEM bundle
    Key  string `yaml:"key"`           // key half of cert+key pair
    Cert string `yaml:"cert"`          // cert half of cert+key pair
}

type VCLConfig struct {
    Name       string     `yaml:"name"`
    Hostnames  []string   `yaml:"hostnames"`
    TLS        []TLSEntry `yaml:"tls"`
    VclPath    string     `yaml:"vclPath"` // path Varnish loads (cmdfile + adm.VCLLoad)
    SourceFile string     `yaml:"-"`     // where we read config from (error messages, dupe detection)
}
```

**VclPath vs SourceFile distinction is critical:**
- VCL input: both `VclPath` and `SourceFile` are set to the same absolute VCL file path.
- YAML input: `SourceFile` = the YAML file; `VclPath` = `routes[].vcl` field (always set — `vcl:` is required).
- `reload.go` panics if `VclPath` is empty (defensive assertion; should never trigger after validation).

---

## Flag conventions

| Special value | Meaning |
|---|---|
| `-` | stdout |
| `none` | suppress output entirely (skip writing the file; skip routing VCL lines in cmdfile) |

**`-timeout DURATION`** (default `30s`) sets a wall-clock deadline for the entire `-reload` operation, including the TCP dial to the admin socket (`adm.Connect` uses `DialContext`). Has no effect without `-reload`; a warning is printed to stderr if set without it.

**Constraint:** `-vclfile -` or `-vclfile none` requires `-cmdfile none` — enforced in `run()` before any I/O. Without this rule the cmdfile would contain an invalid routing VCL path.

---

## Input detection (cmd.go)

Extension-based, handled in the `switch` inside `run()`:
- Single `.yaml` or `.yml` → `parseRoutes()`; incompatible with `-yamlfile`
- One or more `.vcl` → `parseVCL()` per file; `-yamlfile` dumps the merged result
- Anything else (mixed, unknown extension) → error

Helpers `isYAMLFile`, `isVCLFile`, `allVCLFiles` live in `config_builder.go`.

Positional args are passed through `expandGlobs` before the switch — any arg containing `*`, `?`, or `[` is expanded via `filepath.Glob`. No match is a hard error. This allows systemd `ExecStart` lines to use globs directly without a shell wrapper.

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
...
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
    vclPath: /etc/varnish/foo.vcl      # required; relative paths resolved from YAML dir
    tls:
      - pem: /etc/ssl/foo.pem      # OR key+cert pair:
      - cert: /etc/ssl/foo.crt
        key:  /etc/ssl/foo.key
```

Parsed by `parseRoutes`. File existence is checked for `vcl:` and all TLS paths after resolution. `parseVCL` does NOT check TLS file existence (VCL files are trusted as already-deployed artifacts).

---

## Generated outputs

### routing VCL

`buildRoutingVCL` renders `routingTmpl`. Key behaviours:
- Imports `tls` VMOD
- Strips port from `req.http.host` in non-TLS requests (IPv4/hostname + IPv6 handled separately with regex)
- `return(vcl(label-<name>-<ts>))` dispatch
- Falls through to `synth(404)` if no route matches

### cmdfile

`buildCmdfile` renders `cmdfileTmpl`. Order matters for Varnish:
1. `tls.cert.load rb-cert-<name>-<idx>-<ts> <path>` lines (one per TLS entry, all routes; key-cert pairs get ` -k <keypath>`)
2. `tls.cert.commit` (if any TLS entries)
3. Per-route `vcl.load` + `vcl.label` (skipped when `VclPath == ""`)
4. `vcl.load routing-<ts> <routingPath>`
5. `vcl.use routing-<ts>`

---

## Reload flow (reload.go)

8 stages; rollback on failure before stage 6 (`vcl.use`) via `vclRollback`:

| Stage | Action | Rollback on fail |
|---|---|---|
| 1 | Snapshot existing route-builder VCL names + existing `rb-cert-*` cert IDs | n/a |
| 2 | `vcl.load` + `vcl.label` per route (skips empty VclPath) | discard loaded VCLs/labels |
| 3 | `vcl.inline` routing VCL | discard routing VCL + labels + VCLs |
| 4 | `tls.cert.load rb-cert-<name>-<idx>-<ts> <path>` per TLS entry | rollback certs + discard VCLs |
| 5 | `tls.cert.commit` | rollback certs + discard VCLs |
| 6 | `vcl.use` — **point of no return** | — |
| 7 | Discard old VCLs/labels from snapshot | warn only |
| 8 | Discard old `rb-cert-*` certs from snapshot + commit | warn only |

---

## Wildcard hostnames

Hostnames may contain `*` as a whole segment (e.g. `*.example.com`, `foo.*.com`). Partial wildcards like `fo*.com` are rejected by `validateHostname`.

- **Overlap detection** (`hostnamesOverlap`): two hostnames overlap if they have equal segment counts and no segment pair is "definitely different" (neither is `*` and they differ). Used by `checkDuplicateHostnames` and `findRoute`.
- **VCL generation** (`hostnameToVCL`): wildcard hostnames produce a regex condition (`req.http.host ~ "^...$"`) with `[^.]+` per `*` segment and `regexp.QuoteMeta` for literals. Exact hostnames use `==`.
- **`-test-route`** uses `hostnamesOverlap` to match the query host against route hostnames, so wildcards resolve correctly.
- **YAML quoting**: `*` is a YAML alias indicator — test helpers must quote wildcard hostnames with `%q` in YAML strings.

## Test infrastructure

`reload_test.go` uses `github.com/varnish/varnish-go/vtest` which starts real `varnishd` processes. Tests are parallel and self-contained (each gets its own instance).

**varnishd must be in PATH for these tests to pass.** Without it, all tests in `reload_test.go` will fail at `AssertStart`.

Helper functions in `reload_test.go`:
- `writeRouteVCL` — writes a VCL that returns `synth(200, <name>)` for easy HTTP assertions
- `loadLabels` / `activateRoutingVCL` — set up pre-existing Varnish state before testing reload
- `admConnect` — connects to a test instance via `adm.Connect(context.Background(), v.Name())`; returns `*adm.Conn`
- `doGet` / `doGetTLS` — fire HTTP/HTTPS requests at the test instance

Test cert (`testdata/test.crt` + `testdata/test.key`) is a self-signed CN=test cert used for TLS reload tests.

---

## Timestamp format

```go
now := time.Now()
timestamp := now.Format("2006-01-02T15-04-05") + fmt.Sprintf("_%09d", now.Nanosecond())
```

Used as a suffix for all Varnish object names (`vcl-<name>-<ts>`, `label-<name>-<ts>`, `routing-<ts>`). Nanosecond precision avoids collisions on rapid successive runs. Both `cmd.go` and `reload.go` generate their own timestamps independently — the cmdfile on disk and a live reload therefore use different timestamps (intentional: the cmdfile is for cold start only).

**`-reload` does not update the on-disk cmdfile or routing VCL.** After a live reload, the files on disk are stale until `route-builder` is run again without `-reload`. The systemd service handles this by running the file-generation step before `-reload` in `ExecReload`.

---

## varnish-go API (v0.1.0)

`github.com/varnish/varnish-go/adm` is the admin client library. Key points for this version:

- **Every method takes `context.Context` as its first argument.** The context deadline covers all I/O including the initial TCP dial (`ConnectRaw` uses `net.Dialer.DialContext`).
- `Connect(ctx, name string) (*adm.Conn, error)` — returns a pointer.
- `reloadVarnish(ctx, conn, ...)` and `vclRollback(ctx, conn, ...)` both accept a `ctx`; all internal calls pass it through.
- `TLSCertLoad(ctx, certFile string, opts ...TLSOption)` — use `adm.TLSWithCertID(id)` and `adm.TLSWithKeyFile(path)` options.

## Cert ID naming

All route-builder-managed TLS certs use the prefix `rb-cert-` so they can be identified and cleaned up without touching user-loaded certs:

```
rb-cert-<name>-<idx>-<ts>
```

- `<name>` — route name (e.g. `foo_service`)
- `<idx>` — 0-based index within the route's TLS entries
- `<ts>` — same nanosecond timestamp used for VCL object names

Stage 1 snapshots only `rb-cert-*` IDs. Stage 8 discards only those. This avoids any collision with certs loaded outside route-builder.

## resolveTLSPaths

`resolveTLSPaths(tls []TLSEntry, base string) []TLSEntry` is a **pure function** — it allocates a new `[]TLSEntry` and returns it. The caller's slice is not modified.

---

## Coverage notes (as of last run: ~86%)

Genuinely hard-to-reach gaps:
- `buildRoutingVCL` / `buildCmdfile` error paths — pre-compiled templates, effectively dead code
- `writeFileAtomic` write/close error paths — require OS-level fd injection
- `vclRollback` warning paths — require Varnish admin ops to fail mid-rollback
- `main()` — binary entry point, not unit-testable
