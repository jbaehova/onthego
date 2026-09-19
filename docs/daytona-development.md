# Developing the Daytona adapter

The Daytona adapter is in [`internal/transport/daytona`](../internal/transport/daytona). Its production entry points remain `New`, `Probe`, `PassWorkload`, `Observe`, `Logs`, `Stop`, and `Download`.

## Read the integration in order

1. [`doc.go`](../internal/transport/daytona/doc.go) explains the Daytona package boundary.
2. [`daytona.go`](../internal/transport/daytona/daytona.go) contains the actual SDK calls and runtime checks.
3. [`sdk_contract_test.go`](../internal/transport/daytona/sdk_contract_test.go) runs the official SDK against local HTTP fixtures.
4. [`daytona_test.go`](../internal/transport/daytona/daytona_test.go) verifies the receiver platform requirement.
5. [`internal/cli/root.go`](../internal/cli/root.go) coordinates redaction, state synchronization, summaries, and verified pull.

## Run the offline Daytona suite

```sh
go test ./internal/transport/daytona
go test -race ./internal/transport/daytona
go doc ./internal/transport/daytona
```

These tests need neither a Daytona account nor a running sandbox. They do not read the repository root `.env`. Fixture credentials are obvious dummy values, and the HTTP transport rejects any host other than its local test server.

## What the SDK contract tests cover

| Test scenario | Contract verified |
| --- | --- |
| Completed Daytona command with a matching receipt | Preserve successful state, receiver PID, and artifact digests. |
| Completed command with a different transfer ID | Reject unrelated remote result metadata. |
| Completed command with a different request digest | Reject a result for another workload request. |
| Command with no exit code | Keep the run in `RUNNING`; do not retrieve a result receipt. |
| Command with a nonzero exit code | Report `FAILED`; do not accept successful artifact metadata. |
| Streamed artifact download | Use Daytona's real SDK decoder and create a private local file. |
| Existing local artifact | Refuse to replace the user's local work. |
| Process log retrieval | Preserve stdout and stderr returned by Daytona. |
| Owned process session cleanup | Delete the ONTHEGO process session rather than the sandbox. |
| Unowned process session | Reject the request before contacting the SDK. |
| Artifact outside the project directory | Reject the path before contacting the SDK. |
| Receiver platform mismatch | Reject a non-Linux-amd64 receiver before upload. |

## Why use the real SDK in fixtures

The fixture constructs `daytonasdk.NewClientWithConfig` with a local endpoint and passes that client to the existing adapter. Requests still pass through Daytona's generated API client, Toolbox client, and SDK response conversion.

This matters for process exit codes and streamed files. Daytona's SDK exposes process status as a map, while its current `DownloadFileStream` implementation sends a bulk-download request and decodes a multipart file response. The fixtures reproduce that protocol instead of returning an already-decoded byte slice.

The tests verify ONTHEGO's use of the pinned SDK. They do not establish service uptime, account permissions, region capacity, or live sandbox provisioning.

## Inspecting a live Daytona handoff

Use the normal five-command flow from an initialized project with the required credentials in its root `.env`:

```sh
onthego init
onthego login
onthego pass
onthego status --json
onthego pull --output ./handoff-result.md
```

Record the transfer ID and the three Daytona execution identifiers from the receipt. Check that status reports `SUCCEEDED` without a refresh error. A live summary additionally requires a configured OpenAI API key and no summary error. Successful pull must retain the artifact only after the receipt hash check passes.

For the previously completed live flow and its artifact digest, use the [existing validation record](testing/2026-09-19-vps-feedback.md). Offline fixture success should never be reported as a new live Daytona run.

## Change discipline

Keep the five public commands stable. SDK documentation, package comments, and `_test.go` files can expand the integration's visibility without adding production requests or new runtime dependencies.

When changing actual Daytona behavior, distinguish sandbox lifecycle changes from process session changes. Preserve the caller's context, the local redaction boundary, and receipt identity checks. Exercise live provisioning only when the change needs it.

GPU resource plumbing is documented separately from completed features. Adding SDK calls or a fixture for a future capability does not promote GPU fine-tuning, serving, or Hugging Face connectors out of the backlog.
