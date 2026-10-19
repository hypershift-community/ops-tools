# Contributing

Keep changes focused on the affected tool and explain their operational impact
in the pull request. Do not include credentials, tokens, private keys, or real
cloud configuration in examples or test data.

## Local development

Each tool has its own `go.mod` and `go.sum`. Use the toolchain version required
by that module and run build commands within its directory; there is no root
build target. See the root [README](README.md#build-and-test) for build commands
and the Azure tool's [README](cleanzones-azure/README.md) for its build example.

For Go source changes, format the changed files with `gofmt`. Run the affected
module's build and applicable tests; the webhook's existing tests run with
`go test ./...` from `aws-tag-lb-service-webhook/`. Include the commands and
results in the pull request, and state any checks you could not run. For
documentation-only changes, check links and verify usage claims against source.

Do not execute cleanup tools against cloud resources as part of local
validation. Read the [operational safety notes](README.md#operational-safety),
and describe changes to resource selection, permissions, or deletion behavior
explicitly. Keep existing attribution and license notices intact.
