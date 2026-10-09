# Changelog

## Unreleased

### Added

- Offline `--validate` mode: check a spec without kubeconfig access, pods, or reports.
  Validation errors identify the file and YAML line, including destination indexes
  and fields inherited through YAML aliases or merges.
- `summary.md` alongside the existing JSON and JUnit evidence, with counts, cluster
  metadata, verdict explanations, probe stages, and run errors. Report text is
  escaped for Markdown tables and HTML.
- `--explain` prints verdict reasons and DNS/TCP/TLS stage details during a run,
  including why a successful TCP connection fails a blocked HTTPS expectation.
- Table-driven feature tests and fuzz tests for spec YAML, destinations, probe
  observations, and probe input JSON; `make fuzz` and `make vuln` local gates.
- Local release instructions with SHA256SUMS for Windows, Linux, and macOS.

### Changed

- `make test` uses the race detector when CGO is enabled and works without it when
  CGO is disabled.
- Consolidated verification and tag-triggered release jobs into one workflow;
  publishing now depends on successful verification.

Existing spec syntax, normal run console output, JSON/JUnit schemas, and exit codes
are unchanged. No runtime dependencies were added.
