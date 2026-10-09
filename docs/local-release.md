# Local release preparation

Run from the repository root with Go 1.26.9 or newer and the tools listed in
[Build](../README.md#build). No GitHub Actions, Docker, tag, commit, or publishing
step is needed.

## Verify the source

```sh
gofmt -l cmd internal
make lint test fuzz vuln build
git diff --check
```

Formatting must produce no filenames. Lint includes vet and staticcheck.
`make test` uses `-race` if `go env CGO_ENABLED` is `1`; otherwise record the race
test skip. A C compiler is required for race tests. The four fuzz targets each
run for five seconds. `govulncheck` needs access to the Go vulnerability database;
a failed download is not a clean scan. Cluster examples require a separate
disposable cluster and are outside this local gate.

## Windows PowerShell

These commands build both commands for the current Go target (`go env GOOS GOARCH`),
normally Windows on this host. Run them with `GOOS` and `GOARCH` unset for a native
build. Release binaries disable CGO and omit debug symbols; source paths are
trimmed. Output stays in the ignored `dist/local/` directory.

```powershell
$releaseDir = 'dist/local'
$savedCGO = $env:CGO_ENABLED
try {
  $env:CGO_ENABLED = '0'
  go build -p 2 -trimpath -ldflags '-s -w' -o "$releaseDir/" ./cmd/...
  if ($LASTEXITCODE -ne 0) { throw 'Release build failed' }
  $names = 'egress-proof.exe', 'egress-proof-probe.exe'
  $sums = foreach ($name in $names) {
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath "$releaseDir/$name").Hash
    '{0}  {1}' -f $hash.ToLowerInvariant(), $name
  }
  $sums | Set-Content -LiteralPath "$releaseDir/SHA256SUMS" -Encoding ascii
} finally {
  $env:CGO_ENABLED = $savedCGO
}
```

Verify the manifest and smoke-test the CLI without Kubernetes:

```powershell
Get-Content dist/local/SHA256SUMS | ForEach-Object {
  $expected, $name = $_ -split '  ', 2
  $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath "dist/local/$name").Hash
  if ($actual.ToLowerInvariant() -ne $expected) { throw "Checksum mismatch: $name" }
}
./dist/local/egress-proof.exe --help
./dist/local/egress-proof.exe --spec examples/egress.yaml --validate
```

## Linux and macOS

With `GOOS` and `GOARCH` unset, build for the local OS and architecture:

```sh
CGO_ENABLED=0 go build -p 2 -trimpath -ldflags='-s -w' -o dist/local/ ./cmd/...
```

On Linux, generate and verify the manifest:

```sh
(cd dist/local && sha256sum egress-proof egress-proof-probe > SHA256SUMS)
(cd dist/local && sha256sum -c SHA256SUMS)
```

On macOS, use the system checksum utility:

```sh
(cd dist/local && shasum -a 256 egress-proof egress-proof-probe > SHA256SUMS)
(cd dist/local && shasum -a 256 -c SHA256SUMS)
```

Smoke-test either native Unix build:

```sh
./dist/local/egress-proof --help
./dist/local/egress-proof --spec examples/egress.yaml --validate
```

The manifest lists exactly the two newly built binaries, avoiding stale files in
`bin/` or other release directories. SHA256SUMS detects corruption; it is not a
signature. Record the source revision, dirty-tree status, `go version`, and
`go env GOOS GOARCH` with any artifacts you distribute. Local builds include
uncommitted changes and carry no new release tag or version.

The Kubernetes probe image must still contain a Linux build for the nodes'
architecture; see the [Windows-to-Linux probe recipe](../README.md#build).
Native Windows/macOS builds do not add support for Windows target pods.
The existing tagged-release archive names and `checksums.txt` manifest remain
unchanged; these local binaries use the separate `SHA256SUMS` manifest.
