# Egress Proof

Test Kubernetes egress from short-lived probe pods and keep the evidence. A Go CLI
using client-go; no operator, CRDs, or NetworkPolicy YAML inspection. It complements
Regionlock's policy checks and Private AI Platform Kit's egress controls with observed
DNS, TCP, and TLS results.

## Install

Download the archive for your OS and architecture from
[Releases](https://github.com/RamazanKara/egress-proof/releases), verify it against
`checksums.txt` (SHA-256), and put `egress-proof` / `egress-proof.exe` on your PATH.
Releases cover Linux, macOS, and Windows on amd64 and arm64; Windows uses ZIP,
the others tar.gz. Or install from source with Go 1.25+:

```sh
go install github.com/RamazanKara/egress-proof/cmd/egress-proof@v0.1.0
egress-proof --help
```

The tag-triggered release workflow also publishes
`ghcr.io/ramazankara/egress-proof:<version-without-v>` for Linux amd64/arm64 and
updates `:latest` for stable releases. It contains both `/egress-proof` and
`/egress-proof-probe`; the default entrypoint is the probe. To run the CLI in a
container, use `--entrypoint /egress-proof`. The existing v0.1.0 tag predates this
automation: archives and GHCR images require a new release tag containing it.
Until then, use the [local build](#build) and `egress-proof-probe:dev`.

## 2-minute example

With the CLI installed, a published probe image, and `kubectl` pointing at a
disposable cluster with an enforcing CNI (Calico or equivalent), run these from
the checkout. Cluster creation and image pulls are separate setup steps; stock
kind needs an enforcing CNI, as shown in the [kind example](#real-kind-run).

```sh
kubectl apply -f demo/workloads.yaml
kubectl -n egress-proof-demo wait --for=condition=Ready pods --all --timeout=120s
kubectl apply -f demo/policy.yaml
egress-proof --spec examples/egress.yaml --image ghcr.io/ramazankara/egress-proof:latest --out evidence
```

Expect **6 passed, 0 failed, 0 errors**, with `evidence/evidence.json` and
`evidence/junit.xml`. The demo allows DNS, a local service, and example.com TLS,
and blocks a second live service. It needs outbound DNS/HTTPS. Remove the policy
and rerun the same command: it should exit **1**, with two failed blocked checks.
Delete the demo with `kubectl delete -f demo/workloads.yaml` when finished.

## Build

Requires Go 1.25+, a Linux probe image, and a Kubernetes cluster with an enforcing
CNI such as [Calico](https://docs.tigera.io/calico/latest/getting-started/kubernetes/kind).
These commands use Windows Go and Docker in Ubuntu WSL:

```powershell
$env:GOMAXPROCS = '2'
go build -p 2 -o bin/egress-proof.exe ./cmd/egress-proof
go vet -p 2 ./...
go test -p 2 -parallel 2 ./...
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
workflow, which runs vet, tests, build, and golangci-lint on main pushes or dispatch.

For a local release check, use GoReleaser 2.15.4 with Docker Buildx available:

```sh
GOMAXPROCS=2 goreleaser check
GOMAXPROCS=2 goreleaser release --snapshot --clean --parallelism 1
```

Snapshots write archives and checksums to `dist/` and load platform-specific
container images locally without publishing. On Windows, use native Go/GoReleaser;
Docker and kind can run through `wsl.exe -d Ubuntu -e ...`, as below.
If Docker is only available in WSL, use
`--skip=docker` for the native snapshot and validate the image separately there.

## Real kind run

Validated on 2026-10-07 (Europe/Berlin), using Windows Go 1.26.3, kind 0.31.0,
Kubernetes 1.36.1, and Calico 3.33.0. Reproduce in a disposable cluster after building:

```powershell
New-Item -ItemType Directory -Force .work | Out-Null
$kind = (wsl.exe -d Ubuntu -e sh -lc 'command -v kind').Trim()
$kubectl = (wsl.exe -d Ubuntu -e sh -lc 'command -v kubectl').Trim()
function k { wsl.exe -d Ubuntu -e $kubectl --kubeconfig "$wslRoot/.work/kubeconfig" @args }
wsl.exe -d Ubuntu -e $kind create cluster --name egress-proof-mvp --image kindest/node:v1.36.1 --config "$wslRoot/demo/kind.yaml" --kubeconfig "$wslRoot/.work/kubeconfig"
wsl.exe -d Ubuntu -e docker update --cpus 2 --memory 3g --memory-swap 3g egress-proof-mvp-control-plane
k create -f https://raw.githubusercontent.com/projectcalico/calico/v3.33.0/manifests/calico.yaml
k wait --for=condition=Ready pods --all -n kube-system --timeout=180s
# Direct import works around kind 0.31's incompatibility with containerd config v4.
wsl.exe -d Ubuntu -e docker image save --output "$wslRoot/.work/probe.tar" egress-proof-probe:dev
wsl.exe -d Ubuntu -e docker cp "$wslRoot/.work/probe.tar" egress-proof-mvp-control-plane:/egress-proof-probe.tar
wsl.exe -d Ubuntu -e docker exec egress-proof-mvp-control-plane ctr --namespace k8s.io images import --platform linux/amd64 /egress-proof-probe.tar
k apply -f "$wslRoot/demo/workloads.yaml"
k wait --for=condition=Ready pods --all -n egress-proof-demo --timeout=90s
k apply -f "$wslRoot/demo/policy.yaml"
.\bin\egress-proof.exe --spec examples/egress.yaml --kubeconfig .work/kubeconfig --out evidence
```

Actual output:

```text
pass egress-proof-demo/client allowed.egress-proof-demo.svc.cluster.local (expected reachable, observed reachable)
pass egress-proof-demo/client allowed.egress-proof-demo.svc.cluster.local:8080 (expected reachable, observed reachable)
pass egress-proof-demo/client 10.96.0.50:8080 (expected reachable, observed reachable)
pass egress-proof-demo/client https://example.com (expected reachable, observed reachable)
pass egress-proof-demo/client denied.egress-proof-demo.svc.cluster.local:8080 (expected blocked, observed unreachable)
pass egress-proof-demo/client 10.96.0.51:8080 (expected blocked, observed unreachable)
6 passed, 0 failed, 0 errors; reports: evidence
```

Both blocked checks recorded `dial tcp 10.96.0.51:8080: i/o timeout`. Before applying
the policy, an all-reachable baseline passed 6/6. Removing the policy and rerunning
the unchanged example produced **4 passed, 2 failed, 0 errors**, exit **1**, and two
JUnit failures. All probe pods were removed in each run. The demo requires public
DNS/HTTPS access to example.com; other destinations are local fixtures.

```powershell
k delete -f "$wslRoot/demo/policy.yaml"
.\bin\egress-proof.exe --spec examples/egress.yaml --kubeconfig .work/kubeconfig --out evidence/policy-removed
# Expected exit 1. Delete only this disposable cluster when finished, including on failure.
wsl.exe -d Ubuntu -e $kind delete cluster --name egress-proof-mvp
```

This is point-in-time connectivity evidence: a timeout cannot distinguish a policy
drop from an outage. Use known-live destinations and reachable controls, as in the
demo. Probes wait two seconds for initial CNI setup; Kubernetes provides no universal
[policy convergence signal](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
Linux pod networking is supported; hostNetwork and Windows targets are rejected.
Application credentials, custom CA bundles, sidecars, and mesh configuration are not
cloned; injected probe containers or changed identity cause an error. An unset
readiness gate keeps probes out of ordinary Service endpoints; Services publishing
not-ready addresses need separate care. A controller owner reference prevents
ReplicaSets from adopting probes with matching labels.
