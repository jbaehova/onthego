// Package daytona connects ONTHEGO handoffs to Daytona sandboxes through the
// official github.com/daytona/clients/sdk-go SDK.
//
// # Daytona execution boundary
//
// The CLI collects and redacts agent context locally. This package owns the
// Daytona API calls that move that context into a sandbox and start the remote
// receiver. It does not discover local agent sessions or generate LLM summaries.
//
// New constructs the Daytona SDK client using the SDK's authentication settings.
// Probe checks access to Daytona or to an explicitly configured sandbox.
// PassWorkload reuses that sandbox or asks Daytona to create one, uploads the
// context and receiver through Daytona FileSystem, then starts the receiver in
// a Daytona Process session. Its receipt retains the identifiers needed to
// inspect the same execution later.
//
// # Daytona process and artifact lifecycle
//
// Observe queries the Daytona process command and reads the receiver's receipt.
// A successful process exit alone is insufficient: the remote request digest
// and transfer ID must also agree with the local receipt.
//
// Logs retrieves Daytona process output. The CLI applies redaction before
// synchronizing that output or including it in a status summary.
//
// Download streams an artifact through Daytona FileSystem into a new local
// file. The workload pull layer verifies its SHA-256 against the receipt before
// retaining the result. Stop only accepts ONTHEGO-prefixed process sessions.
//
// # Supported scope
//
// The default agent-sync profile uses Daytona CPU execution to return HANDOFF.md.
// GPU resource fields exist in the adapter, but GPU fine-tuning, model serving,
// and Hugging Face integration remain unfinished backlog work.
//
// The SDK contract tests use a local HTTP fixture with the real Daytona SDK.
// They require no Daytona credentials and create no live Daytona resources.
package daytona
