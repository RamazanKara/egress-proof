# Egress Proof

Test Kubernetes egress from short-lived probe pods and keep the evidence. A Go CLI
using client-go; no operator, CRDs, or NetworkPolicy YAML inspection. It records
observed DNS, TCP, and TLS results.

## Install

Download the archive for your OS and architecture from
[Releases](https://github.com/RamazanKara/egress-proof/releases), verify it against
`checksums.txt` (SHA-256), and put `egress-proof` / `egress-proof.exe` on your PATH.
The v0.2.0 release has Linux, macOS, and Windows archives for amd64 and arm64;
Windows uses ZIP, the others tar.gz. Or install the released source:

```sh
go install github.com/RamazanKara/egress-proof/cmd/egress-proof@v0.2.0
egress-proof --help
```

The tag-triggered release workflow also publishes
`ghcr.io/ramazankara/egress-proof:<version-without-v>` for Linux amd64/arm64 and
updates `:latest` for stable releases. It contains both `/egress-proof` and
`/egress-proof-probe`; the default entrypoint is the probe. To run the CLI in a
container, use `--entrypoint /egress-proof`. The local development image is
`egress-proof-probe:dev`; see [Build](#build).

## Cluster example

With the CLI installed, a published probe image, and `kubectl` pointing at a
disposable cluster with an enforcing CNI (Calico or equivalent), run these from
the checkout. Cluster creation and image pulls are separate setup steps; stock
kind needs an enforcing CNI, as configured in
[the kind workflow example](examples/github/kind.yml).
These cluster-dependent recipes are outside the local unit-test gate; verify
them on your own disposable cluster before relying on the results.

```sh
kubectl apply -f demo/workloads.yaml
kubectl -n egress-proof-demo wait --for=condition=Ready pods --all --timeout=120s
kubectl apply -f demo/policy.yaml
egress-proof --spec examples/egress.yaml --image ghcr.io/ramazankara/egress-proof:latest --out evidence
```

With healthy fixtures and enforced policy, expect **6 passed, 0 failed, 0 errors**,
with `evidence/evidence.json` and `evidence/junit.xml`. The demo allows DNS,
a local service, and example.com TLS,
and blocks a second live service. It needs outbound DNS/HTTPS. Remove the policy
and rerun the same command: it should exit **1**, with two failed blocked checks.
Delete the demo with `kubectl delete -f demo/workloads.yaml` when finished.

## Build

Building this checkout requires Go 1.26.9 or newer. The local gate also requires
GNU Make, a C compiler for Go's race detector, and golangci-lint 2.12.2 on PATH:

```sh
make lint test build
go tool cover -func=coverage.out
```

`lint` runs vet and golangci-lint; `test` runs every package with `-race` and writes
`coverage.out`; `build` writes both commands to `bin/` for the current OS.
On Windows the CLI is `bin/egress-proof.exe`. Running it against a cluster requires
a Linux probe image and an enforcing CNI such as
[Calico](https://docs.tigera.io/calico/latest/getting-started/kubernetes/kind).
To build the Linux probe using Windows Go, with Docker in Ubuntu WSL:

```powershell
$env:GOMAXPROCS = '2'
$savedOS, $savedArch, $savedCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
try {
  $env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
  go build -p 2 -trimpath -o bin/egress-proof-probe ./cmd/egress-proof-probe
} finally {
  $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $savedOS, $savedArch, $savedCGO
}
$wslRoot = (wsl.exe -d Ubuntu -e wslpath -a $PWD.Path).Trim()
wsl.exe -d Ubuntu -e docker build -t egress-proof-probe:dev $wslRoot
```

Build the probe for your nodes' architecture. Load the image into kind, or publish
it to your own registry and pass its reference with `--image`.

## Spec

```yaml
namespace: payments
podSelector: app=api
reachable:
  - database.payments.svc.cluster.local
  - database.payments.svc.cluster.local:5432
  - https://example.com
blocked:
  - 192.0.2.10:443
```

`namespace` alone selects every running pod in that namespace. `podSelector` uses
Kubernetes label selector syntax; without `namespace`, it uses the kubeconfig's
namespace. An empty selection is an error. Every selected running pod is tested
sequentially, with its exact labels, service account, node, and DNS configuration.
Deleting pods and other egress-proof probes are excluded.

```powershell
.\bin\egress-proof.exe --spec examples/egress.yaml --kubeconfig .work/kubeconfig --out evidence
```

The kubeconfig flag is optional; normal `KUBECONFIG` / `~/.kube/config` loading applies.
Inside Kubernetes, the mounted service account credentials are used when no
kubeconfig is provided.
`--image` defaults to `egress-proof-probe:dev`; `--out` defaults to `evidence`.

| Destination | Check |
| --- | --- |
| DNS name | DNS resolution only |
| DNS name or IP with `:port` | DNS if needed, then TCP; bracket IPv6 addresses |
| `https://host[:port]/path` | DNS, TCP, verified TLS with SNI; no HTTP request or redirect |

Each destination has a five-second total budget, shared across all resolved
addresses. Reachability needs one successful address; any successful TCP connection
fails a blocked TCP/HTTPS expectation, including when its TLS handshake fails.
Only timeouts satisfy `blocked`. NXDOMAIN, connection refusal, missing routes,
certificate errors, incomplete probes, and API failures are errors, not passing
block evidence. DNS failure for a TCP/HTTPS destination is also inconclusive.

## Evidence format

Every validated run writes `evidence.json` and `junit.xml` under `--out`, including
failed runs. Use a different directory per run to retain history. JSON currently
has `schemaVersion: "1"`:

| Fields | Meaning |
| --- | --- |
| `specHash`, `spec` | `sha256:<hex>` hash of the original spec bytes and the effective spec |
| `cluster` | Kubeconfig context (empty in-cluster), API server, `kubeSystemUID`, Kubernetes version |
| `probeImage`, `startedAt`, `finishedAt` | Requested image and UTC run timestamps |
| `targets[]` | Source pod namespace/name/UID, labels, service account, node; probe pod/UID/image ID and `cleanedUp` |
| `checks[]` | Target, destination, `expected`, `verdict`, observed `outcome`, timestamps, resolved IPs and stages |
| `checks[].stages[]` | DNS/TCP/TLS stage `name`, `address`, `durationMs`, `success`, and optional `error` |
| `errors[]`, `summary` | Run errors and counts named `passed`, `failed`, `errors` |

Check verdicts are `pass`, `fail`, or `error`; outcomes are `reachable`,
`unreachable`, or `error`. JUnit has one test case per destination per target,
with observations as JSON in `system-out`, mismatches in `<failure>`, and
inconclusive checks or run failures in `<error>`. Suite properties include the
spec hash, cluster UID, and probe image. Exit codes:
**0** all expectations matched, **1** mismatch, **2** execution or inconclusive error.
Invalid flags/specs and output write failures may prevent evidence from being written.

## Scheduled in-cluster runs

The plain [CronJob manifest](deploy/cronjob.yaml) runs daily at 06:00 UTC, prevents
overlapping scheduled runs, and retains reports on a 1 GiB PVC. It uses the demo
namespace and an external ConfigMap so the same spec works locally and in-cluster.
After setting up the demo above:

```sh
kubectl -n egress-proof-demo create configmap egress-proof --from-file=egress.yaml=examples/egress.yaml --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/cronjob.yaml
kubectl -n egress-proof-demo create job --from=cronjob/egress-proof egress-proof-now
kubectl -n egress-proof-demo wait --for=condition=complete job/egress-proof-now --timeout=180s
kubectl -n egress-proof-demo logs job/egress-proof-now
```

For your workloads, change the namespace, spec, schedule, and deadline; pin both
image references to the same released version or digest. The demo client sleeps
for one hour; use long-lived workloads for ongoing schedules. The PVC needs a default
StorageClass (or an explicit class suitable for your cluster). Each run writes
`/evidence/<job-pod-name>/{evidence.json,junit.xml}`. Mount the `egress-proof-evidence`
claim in your exporter/reader as UID 65532 to collect these files; the CLI image
has no shell or `tar`. Job expiry preserves the PVC, so archive/prune old evidence
separately. Deleting the PVC can delete its reports. The runner needs API access
even when the target pods' egress is restricted.

The caller needs `get/list/create/delete` on pods, `get` on pods/log, `get` on the
`kube-system` namespace, and access to `/version`. The probe uses the target service
account with token mounting disabled. Pods have CPU/memory limits, an active deadline,
and are deleted after success, failure, or interruption; cleanup failure fails the run.
An API outage or hard kill can leave a stopped pod object named `egress-proof-*`
that needs manual deletion; a hard kill can also prevent writing the report.

## GitHub Actions against kind

Copy [examples/github/kind.yml](examples/github/kind.yml) into `.github/workflows/`
to enable the manually dispatched example. It builds the probe image, installs
Calico into kind, checks an all-reachable baseline, proves the policy, and verifies
that removing it fails the gate. Both evidence formats are uploaded even on failure,
and the disposable cluster is deleted. This stays separate from the one-job CI
workflow, which runs `make lint test build` on pushes or dispatch. GitHub Actions
availability is not required for the local gate. The separate
[release workflow](.github/workflows/release.yml) runs on `v*` tags using
[GoReleaser's configuration](.goreleaser.yml).

## Limits

This is point-in-time connectivity evidence: a timeout cannot distinguish a policy
drop from an outage. Use known-live destinations and reachable controls, as in the
demo. Probes wait two seconds for initial CNI setup; Kubernetes provides no universal
[policy convergence signal](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
Linux pod networking is supported; hostNetwork and Windows targets are unsupported.
Application credentials, custom CA bundles, sidecars, and mesh configuration are not
cloned. Admission changes to the probe's labels, service account, node, DNS settings,
command, environment, or containers cause an error. An unset
readiness gate keeps probes out of ordinary Service endpoints; Services publishing
not-ready addresses need separate care. A controller owner reference prevents
ReplicaSets from adopting probes with matching labels.
