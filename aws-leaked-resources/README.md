# cleanleaked

Find potential AWS leaks from HyperShift CI, correlate them with archived Prow
runs, and review the evidence before cleanup.

## Quick Start

From the repository root, run the read-only investigation with the AWS account
and region you intend to inspect:

```bash
cd aws-leaked-resources
make flow REGION=us-east-1
```

This builds `bin/cleanleaked` and runs `flow`. You can also run the built binary
directly: `./bin/cleanleaked flow --region us-east-1`. The flow runs the AWS
scan, looks up Prow evidence, and saves `scan.json`, `proofs.json`, and
`report.html` together under `output/flow-*`. It prints counts for leaked,
eligible, and unproven sets. If any leaks pass the proof gate, it also prints
an interactive cleanup command. Review the HTML before running that command.
**The flow never executes it.**

Use `make pipeline REGION=us-east-1` to include a cleanup preview. It saves the
reports under `output/pipeline.*` and never deletes resources. To run the stages
separately, replace each JSON path below with the path printed by the previous
stage. The scan and proof files usually have different timestamps.

```bash
make scan REGION=us-east-1
make prove INPUT=output/scan-SCAN_TIMESTAMP.json
make report INPUT=output/scan-SCAN_TIMESTAMP.json PROOF=output/proofs-PROOF_TIMESTAMP.json
make dry-run REGION=us-east-1 PROOF=output/proofs-PROOF_TIMESTAMP.json
```

## Classification Gates

The scan groups tagged VPCs by infra ID, then applies the following gates to
each infra set. The first matching gate determines its verdict.

| Gate | Check | Verdict if matched |
|------|-------|--------------------|
| **0. Protection** | `do-not-delete=true` on any VPC, protected VPC name, or protected username in the infra ID or VPC name | **PROTECTED** |
| **1. Age** | An expiry tag has not expired, or the oldest known VPC is younger than `--min-age` | **TOO_YOUNG** |
| **1. Age** | No usable VPC creation timestamp | **UNCERTAIN** |
| **2. OIDC/S3** | An OIDC discovery document is found in a checked CI bucket | **ACTIVE** |
| **3. EC2 instances** | An owned instance is pending, running, stopping, stopped, or shutting down | **ACTIVE** |
| **3. EC2 instances** | The instance lookup fails | **UNCERTAIN** |
| **4. CI pattern** | The infra ID does not match a known CI pattern | **UNCERTAIN** |
| **5. No gate matched** | Old enough, no discovery document or matching instances found, and matches a CI pattern | **LEAKED** |

### Protection (Gate 0)

Three layers of protection, checked in order:

1. **Tag-based**: VPC has `hypershift.openshift.io/do-not-delete=true`
2. **Name-based**: VPC name is `hypershift-ci-2-vpc`, `hypershift-ci-3-vpc`, or `hypershift-ci-metrics-vpc`
3. **User-based**: the infra ID or VPC name contains a protected team member username

An infra set classified as `PROTECTED` is never selected for deletion.

### Age (Gate 1)

An unexpired `expirationDate` on any VPC blocks the entire set. An expired tag
does not itself establish age. The scan uses the oldest known VPC timestamp in
the set for `--min-age` (default `24h`). For each VPC it uses `creation_date`, or
falls back to the earliest endpoint creation time, then the earliest network
interface attachment time.

### CI Pattern Match (Gate 4)

Known CI test patterns:
- Lowercase hex infra IDs with **at least 16 characters**. This classification
  heuristic is broader than the exact 20-character hash used to prove CLI jobs.
- [Known test prefixes](prow.go), such as `create-cluster-*`, `node-pool-*`,
  `control-plane-upgrade-*`, `karpenter-*`, and `request-serving-isolation-*`.

InfraIDs that don't match (e.g., `hc1-*`, `clust-*`, `dev-*`) are classified as `UNCERTAIN`.

## Verdicts

| Verdict | Meaning | Deletable? |
|---------|---------|------------|
| `PROTECTED` | Management cluster, developer resource, or `do-not-delete` tag | Never |
| `TOO_YOUNG` | Created less than `--min-age` ago or `expirationDate` not reached | No |
| `ACTIVE` | OIDC discovery document found or non-terminated EC2 instances found | No |
| `UNCERTAIN` | Origin or safety checks are inconclusive | No |
| `LEAKED` | Old enough, matches a CI pattern, and no active signals were found | Only with sufficient Prow proof by default |

## Delete Modes

| Command | Behavior |
|---------|----------|
| `delete` | Prompt once, then delete only eligible LEAKED sets |
| `delete --confirm` | Skip prompts, not proof or safety checks |
| `delete --interactive` | Show full resource inventory per infra set, prompt for each |
| `delete --dry-run` | Show what would be deleted, delete nothing |

`delete` always scans AWS again. By default, an infra set must still be `LEAKED`
and match a unique archived run with state `failure`, `error`, or `aborted`.
A Prow match never upgrades an `ACTIVE`, `PROTECTED`, `TOO_YOUNG`, or `UNCERTAIN`
set to `LEAKED`. `--confirm` only disables prompts; it does not change eligibility.
The proof must apply to the same region, VPC IDs and VPC owners. A 10-character
hash without a matching tag additionally needs a creation log containing an
exact VPC or VPC endpoint ID for each VPC; artifact names are not enough.
The command does not contact Prow. Only use a proof file generated by `prove`
that you trust; the file is not signed.

`--dry-run` applies the same proof gate without making any deletion requests.
Both cleanup and dry-run list each leak skipped for insufficient proof by infra
ID, with a reason, at the end. These skips do not cause a nonzero exit status.
If no sets pass the gate, none are deleted. Use `--require-proof=false` only
after manual review; it bypasses Prow proof, not the fresh AWS verdict.

### Cascade Order

For each selected infra set, cleanup attempts Route 53 zones, IAM OIDC
providers, and IAM roles before cleaning up its VPCs. Each VPC is handled in
this order:

1. ELBs/NLBs
2. VPC Endpoint Services (by infraID tag)
3. VPC Endpoints
4. NAT Gateways → poll every 5 seconds for up to 1 minute for draining gateways
5. Elastic IPs (only those from the NAT gateways)
6. Internet Gateways
7. Route Tables (non-main only)
8. Network Interfaces
9. Subnets
10. Security Groups (non-default only, revoke rules first)
11. VPC

Most delete calls retry up to 10 times with a 2-second delay. Ctrl+C stops
further work at cancellation checks; it cannot undo requests AWS accepted.

## Flags

### `flow` command

| Flag | Default | Description |
|------|---------|-------------|
| `--region` | `us-east-1` | AWS region (`REGION` in Make) |
| `--min-age` | `24h` | Minimum VPC age (`MIN_AGE` in Make) |
| `--target` | *(none)* | Restrict the scan and suggested deletion to one infra ID or VPC ID (`TARGET` in Make) |
| `--output-dir` | `output` | Parent directory for per-run reports (`OUTPUT_DIR` in Make) |
| `--verbose`, `-v` | `false` | Show detailed AWS scan progress, including each leaked infra set, instead of only step headings |

`flow` and `prove` also accept `--prow-window` (default `6h` either side of the
timestamp, maximum `168h`), `--prow-timeout` (default `2m`),
and `--prow-verify-artifacts` (default `true`). For every command, use `--help`
to see its full flag list.

### `scan` command

| Flag | Default | Description |
|------|---------|-------------|
| `--region` | `us-east-1` | AWS region |
| `--min-age` | `24h` | Minimum VPC age to consider for deletion |
| `--target` | *(none)* | Scan a single infraID or VPC ID instead of all VPCs |
| `--output` | `table` | Output format: `table` or `json` |
| `--output-dir` | `output` | Directory for timestamped report files (empty to disable) |

### `delete` command

| Flag | Default | Description |
|------|---------|-------------|
| `--region` | `us-east-1` | AWS region |
| `--min-age` | `24h` | Minimum VPC age to consider for deletion |
| `--target` | *(none)* | Delete a single infraID or VPC ID instead of all leaked |
| `--dry-run` | `false` | Show what would be deleted without deleting |
| `--confirm` | `false` | No prompts, delete eligible LEAKED sets immediately |
| `--interactive`, `-i` | `false` | Prompt before each infra set with full inventory |
| `--limit` | `0` | Max infra sets to delete (0 = no limit) |
| `--proof` | *(none)* | JSON report produced by `prove` |
| `--require-proof` | `true` | Only delete candidates with sufficient Prow proof |
| `--output` | `table` | Output format: `table` or `json` |
| `--output-dir` | `output` | Directory for timestamped report files (empty to disable) |

### `--target` usage

Accepts a VPC ID (`vpc-*`) or an infraID. If an infraID is given, it searches for a VPC with the Name tag `<infraID>-vpc`.
Targeted deletion refuses a selection that omits a VPC from the same infra set
because some cleanup operations (such as IAM and DNS) are shared by infra ID.

```bash
# By VPC ID
cleanleaked scan --target vpc-066231b8dc5a4400f

# By infraID
cleanleaked scan --target node-pool-78vcg

# Also accepts the -vpc suffix (stripped automatically)
cleanleaked scan --target node-pool-78vcg-vpc

# Preview a specific target with its proof report
cleanleaked delete --target 00ab3695c5f73d4354b9 --dry-run --proof output/proofs-PROOF_TIMESTAMP.json
```

## Prow Correlation

### Scan report format

Use `make scan REGION=us-east-1` or `cleanleaked scan --output json` to save a
machine-readable inventory in `output/scan-<timestamp>.json`. For JSON consumers,
read `.infraSets` for the inventory and
`.schemaVersion` (currently `1`) to identify the format. The report includes
its generation time, AWS region and minimum age. Each infra set can include
an `anchorTime` and `anchorSource`: the earliest known VPC creation time, or
an estimate based on the expiry tag. Missing anchors are omitted. These fields
guide the Prow lookup; they are not inputs to the AWS verdict.

The AWS scan collects local hints for `LEAKED` and `UNCERTAIN` infra sets:
- **Test type**: from the infraID pattern (e.g., `node-pool`, `create-cluster`, `e2e-generic`)
- **HC namespace**: from Route53 `service.ci` zone TXT records
- **Prow tag**: the recorded `hypershift.openshift.io/prow-job-id`, when present
- **Prow search URL**: a hint for known named test types, not evidence of a specific run

### Inspect candidates in Prow

```bash
make prove INPUT=output/scan-SCAN_TIMESTAMP.json
cleanleaked prove --input output/scan-SCAN_TIMESTAMP.json --infra-id 00ab3695c5f73d4354b9
```

`prove` reads a saved scan and does not call AWS. It looks up only `LEAKED`
and `UNCERTAIN` rows that have a usable timestamp and a Prow tag or supported
hash. It searches archived HyperShift periodics in `test-platform-results-public`
around the AWS timestamp (`--prow-window`, default 6h on either side). It first
uses the stored tag if present and consistent. Otherwise it compares the exact
20-character CLI infra ID (or the E2E v2 variant's 10-character suffix) with
SHA-256 of the original `PROW_JOB_ID` (ProwJob `metadata.name`, not `metadata.uid`;
no trailing newline).

The result includes the run, build ID, actual job state, and links. Where
available, it cites an artifact name and separately cites creation log records
that contain exact VPC or VPC endpoint IDs from the scan. An artifact filename
alone does not verify an AWS resource. Use `--prow-verify-artifacts=false` to
skip the corroboration step. Use `--json` to print the same JSON written to
`output/proofs-<timestamp>.json`.

An unresolved, ambiguous, or unavailable lookup stays explicit in the report.
Presubmits and jobs outside the retained public archive are not covered. A job
match establishes provenance; it does not establish that the AWS resources are
unused.

### Generate a browsable HTML report

```bash
make report INPUT=output/scan-TIMESTAMP.json PROOF=output/proofs-TIMESTAMP.json HTML=output/report.html
# Or use the CLI:
cleanleaked report --input output/scan-TIMESTAMP.json --proof output/proofs-TIMESTAMP.json --output output/report.html
```

Use the actual timestamp for each input; the scan and proof timestamps differ.
Open the generated file in a browser. The standalone Tokyo Night report has
the leaked sets on the main view and separate tabs for the other verdicts.
Expand a set for links to the AWS console, Prow runs, archived metadata, and
matching artifacts or creation logs. The command reads the saved reports
without calling AWS or Prow and does not overwrite existing output files.
Omit `--output` to write HTML to stdout.

## Safety

- Proof binds the selected infra set to a Prow run. It does not independently
  verify every dependent resource or whether an associated resource is shared.
  Review the interactive inventory and confirm the AWS account and region.
- **Default SG** (`GroupName=default`) is never deleted
- **Main route table** is never deleted (auto-deleted with VPC)
- **EIPs** are scoped: only EIPs from NAT gateways of the target VPC are released
- **ELBs** are filtered by VPC ID before deletion

## Development

```bash
make build    # Build binary
make test     # Run unit tests
make vet      # Run go vet
make lint     # Run golangci-lint
make verify   # Build, test, vet, and lint
make clean    # Remove binary
```

Use the Go version in `go.mod` for verification (for example,
`GOTOOLCHAIN=go1.26.3 make verify`). The verification targets do not scan or
modify cloud resources.
