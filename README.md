# ops-tools

Standalone operational and CI cleanup tools extracted from
[openshift/hypershift](https://github.com/openshift/hypershift).

## Tools

Each directory is a separate Go module and command. There is no root Go module
or shared build target.

| Directory | Purpose | Go version declared in `go.mod` |
| --- | --- | --- |
| [`cleanroles`](cleanroles/) | Clean up selected AWS IAM roles and associated policies/instance profiles using VPC-derived infrastructure IDs. | 1.24.4 |
| [`cleanzones`](cleanzones/) | Clean up selected AWS Route 53 private zones and records in hard-coded public zones using VPC-derived infrastructure IDs. | 1.25.7 |
| [`cleanzones-azure`](cleanzones-azure/) | Clean up matching Azure DNS records using resource-group-derived infrastructure IDs. | 1.26.3 |
| [`aws-tag-lb-service-webhook`](aws-tag-lb-service-webhook/) | Mutate Kubernetes LoadBalancer Services to add `red-hat-managed=true` to the AWS additional-resource-tags annotation. | 1.23 |

The AWS modules retain their inherited `github.com/openshift/hypershift/...`
module paths. Build them from their local directories rather than assuming an
install path under this repository's name.

## Build and test

Use a Go toolchain satisfying the selected module's `go.mod`. Run commands from
that module, not the repository root. For example, the webhook Dockerfile uses
this build command:

```sh
cd aws-tag-lb-service-webhook
go build -o app
go test ./...
```

The webhook has unit tests in `main_test.go`; the cleanup tools currently have
no test files. The `cleanroles` and `cleanzones` Dockerfiles use `go build .`
inside their respective directories. For Azure build and flag examples, see
[`cleanzones-azure/README.md`](cleanzones-azure/README.md).

## Operational safety

These are environment-specific tools, not general-purpose garbage collectors.
Cleanup can permanently delete IAM or DNS resources. Review the source's
selection rules and confirm the cloud account/subscription, permissions, region,
resource groups, and DNS zones before any run. Infrastructure-ID matching is a
heuristic, not proof that a resource is unused; an incomplete inventory can
expose active resources to deletion. Never run cleanup as a build or test step.

- **`cleanroles`:** Uses the AWS SDK default credential chain and scans VPCs in
  `us-east-1`, `us-east-2`, `us-west-1`, `us-west-2`, and `eu-west-1`. It filters
  roles by age and hard-coded name prefixes/suffixes. Deletion is enabled by
  default; explicitly pass `--dry-run` to preview candidates without deleting.
- **`cleanzones`:** Uses the AWS SDK default credential chain with region
  `us-east-1` and hard-coded zone selection. It deletes immediately: `dryRun` is
  a source constant set to `false`, not a command-line flag. There is no runtime
  dry-run option. Do not run the binary simply to inspect usage.
- **`cleanzones-azure`:** Uses Azure `DefaultAzureCredential`. The required flags
  are `-subscription-id`, `-dns-zone-rg`, and `-dns-zone-name`. Dry-run defaults
  to `true`; add `-verbose` to see individual candidates. `-infra-rg` narrows
  the resource-group inventory used to identify active infrastructure. Matching
  records can still be selected when no active infrastructure IDs are found;
  inspect candidates before setting `-dry-run=false`.
- **Webhook:** Serves HTTPS on port `8443` at `/mutate-service`, using
  `/etc/webhook/certs/tls.crt` and `/etc/webhook/certs/tls.key`. The inherited
  manifests use OpenShift serving-certificate and CA-injection annotations.

The AWS tools include IAM policy JSON and CronJob/ServiceAccount manifests; the
webhook includes Deployment, Service, and MutatingWebhookConfiguration manifests.
These contain inherited image references, account-specific roles, namespaces,
and schedules. Review and adapt them before deployment; they do not establish
CI infrastructure for this repository. The cleanup CronJobs do not enable
dry-run mode.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local development and review guidance.

## License and provenance

The extracted tools originate in the upstream `contrib/cleanroles`,
`contrib/cleanzones`, `contrib/cleanzones-azure`, and
`contrib/aws-tag-lb-service-webhook` directories. Their extraction commits and
inherited history are retained in this repository.

[LICENSE](LICENSE) contains the Apache License, Version 2.0, copied unchanged
from [openshift/hypershift at commit
`15866e16f4169c8cf0601ed69d5f7ed4fe8eb00f`](https://github.com/openshift/hypershift/blob/15866e16f4169c8cf0601ed69d5f7ed4fe8eb00f/LICENSE).
Preserve inherited copyright, license, and attribution notices when modifying
or redistributing these tools. Dependencies retain their own licenses.
