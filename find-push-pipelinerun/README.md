# find-push-pipelinerun

Find on-push Konflux PipelineRuns for a merged GitHub pull request's merge
commit. Query the current Kubernetes cluster, falling back to KubeArchive when
a list is empty or fails. Optionally display downstream Releases, their release
PipelineRuns, and expected destination images.

This directory is an independent Go module; there is no root build target.

## Build, run, and install

Use Go 1.25.7 or newer. From this repository's root:

```sh
cd find-push-pipelinerun
go build -o /tmp/find-push-pipelinerun .
go run . --help
go install .
```

`go install .` installs into `GOBIN`, or `$(go env GOPATH)/bin` by default.
Add that directory to `PATH`. Build/install from this checkout until the module
is published; no HyperShift checkout or root `go.mod` is needed.

## Usage

```text
find-push-pipelinerun [OPTIONS] PR [COMPONENT]
```

Pass options **before** positional arguments (Go's flag parser stops at the
first positional argument). `PR` accepts a full GitHub pull request URL,
`owner/repo#number`, or a bare number. A bare number infers the repository from
the **current directory's `origin` remote**, not from this tool's source repo;
Git must be installed for that form. Prefer an explicit repository, especially
when running inside ops-tools or a fork.

```sh
find-push-pipelinerun openshift/hypershift#8908
find-push-pipelinerun --release openshift/hypershift#8908 hypershift-operator
find-push-pipelinerun --release --watch https://github.com/openshift/hypershift/pull/8908
# From an openshift/hypershift checkout with the matching origin:
find-push-pipelinerun 8908
```

`COMPONENT` filters build PipelineRuns by resource-name prefix and Releases by
prefix of the `appstudio.openshift.io/component` label.
`--release` (`-r`) shows Releases, release plan names, status, and expected
destination images derived from the ReleasePlan (or ReleasePlanAdmission)
mapping and Snapshot source digest. These are expected destinations, not proof
that registry publication succeeded. Unavailable mappings/snapshots leave the
image empty. Release PipelineRuns are selected by release-name label in the
release engineering namespace.

`--watch` (`-w`) polls until builds reach terminal status. With `--release`,
it waits for builds first, then for Release creation and terminal Release
status; it does not independently wait for every release PipelineRun. Use
Ctrl-C to stop. Missing Releases or repeated query failures can keep polling
indefinitely. Failed terminal statuses also end watching: a zero exit code
means the query/watch completed, **not** that builds/releases succeeded.
If no push PipelineRuns are found initially, the command exits without watching.
`--help` (`-h`) prints usage without contacting APIs.

## Authentication and configuration

Use an already-authenticated kubeconfig for the intended Konflux cluster.
Client-go uses standard kubeconfig loading (`KUBECONFIG`, otherwise
`~/.kube/config`) and its current context; there is no context-selection flag.
Credential exec plugins referenced by that kubeconfig must be installed and
trusted. The command sends GET requests only, but may execute those plugins.
No `oc`, `kubectl`, `jq`, or GitHub CLI is required.

The identity needs list access to Tekton `tekton.dev/v1` PipelineRuns in the
tenant namespace and, with `--release`, `appstudio.redhat.com/v1alpha1` Releases
there and PipelineRuns in the release engineering namespace. Resolving images
also needs get access to ReleasePlans and Snapshots in the tenant namespace,
and ReleasePlanAdmissions in their referenced namespaces. KubeArchive access
uses the **same kubeconfig-derived HTTP client and credentials** as Kubernetes;
only point it at a trusted, compatible endpoint. Do not store credentials in
this repository or pass tokens in command-line arguments.

| Variable | Purpose / default |
| --- | --- |
| `KUBECONFIG` | Standard client-go kubeconfig file list; defaults to `~/.kube/config`. |
| `GITHUB_TOKEN` | Optional GitHub API bearer token; needed for private repositories or higher rate limits. Use a read-only token with appropriate repository access. |
| `KONFLUX_NAMESPACE` | Build PipelineRuns and Releases; `crt-redhat-acm-tenant`. |
| `RELENG_NAMESPACE` | Release PipelineRuns; `rhtap-releng-tenant`. |
| `KUBEARCHIVE_HOST` | Trusted archive API base URL; `https://kubearchive-api-server-product-kubearchive.apps.stone-prd-rh01.pg1f.p1.openshiftapps.com`. |
| `WATCH_INTERVAL` | Positive integer seconds between polls; `15` if unset, invalid, or non-positive. |

Namespace/host variables that are unset or empty use their defaults (an empty
`KUBEARCHIVE_HOST` does not disable fallback). The GitHub API is
`https://api.github.com`; GitHub Enterprise is not configurable. API clients
default to a 30-second HTTP timeout. The supplied defaults target the original
HyperShift Konflux environment, not an arbitrary cluster.

## Local checks

From this module directory:

```sh
gofmt -l *.go   # should print nothing; use gofmt -w on files you change
go mod tidy -diff
go build ./...
go test -race ./...
go vet ./...
```

Tests use local HTTP servers and fake query responses; they require no live
cluster, cloud account, or registry. Do not run operational commands against
real infrastructure as a build/test step. No golangci-lint configuration is
copied from HyperShift's aggregate tools module; `gofmt` and `go vet` are the
module-local formatting/static checks.

## Provenance and license

Originally authored by **Antoni Segura Puimedon** in
[openshift/hypershift PR #8935](https://github.com/openshift/hypershift/pull/8935),
from `hack/tools/find-push-pipelinerun/` at
[`91f7e2601ce694015314aff5664184084af8181c`](https://github.com/openshift/hypershift/tree/91f7e2601ce694015314aff5664184084af8181c/hack/tools/find-push-pipelinerun).
All six Go implementation files and four test files are retained. Migration
changes are limited to standalone packaging/documentation and tested release
query diagnostics/watch completion fixes; no HyperShift scripts are removed.

Dependencies are selected from that commit's `hack/tools/go.mod` and pruned
with `go mod tidy`, retaining source checksums where present. Tidy adds two
historical module-metadata checksums (`github.com/creack/pty v1.1.9/go.mod` and
`github.com/kr/pretty v0.2.1/go.mod`), not runtime upgrades. In particular, client-go
is effectively **v0.34.2**, despite a v0.34.3 require, because the source module
replaces Kubernetes modules with v0.34.2. This module retains those effective
versions rather than upgrading Kubernetes during extraction. Unrelated tools,
their dependencies, and aggregate-module lint configuration are not included.

The [Apache-2.0 license](LICENSE) is copied unchanged from the same source
commit. Existing source notices are preserved; dependencies retain their own
licenses. This tool-local copy keeps redistribution self-contained without
depending on a pending repository-wide license change.
