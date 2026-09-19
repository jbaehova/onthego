# Phase 1 implementation record

Updated 2026-09-19.

## Complete

- The public CLI exposes `init`, `login`, `pass`, `pull`, and `status`.
- `onthego init` creates a stable project identity and safe default `agent-sync` configuration without overwriting an existing project.
- Project commands refuse an uninitialized repository and direct the user to `onthego init`.
- `onthego pass` automatically scans Codex, Cursor, Hermes, and Claude without agent or session options.
- Every detected session for the current Git project is included while evidence from other workspaces is rejected.
- Context export redacts tokens, authorization headers, common secret assignments, and private keys.
- `HANDOFF.md` and the context envelope contain evidence hashes and source offsets.
- The Daytona adapter uses the official Go SDK and uploads the installed static receiver binary.
- A live Daytona CPU workload completed with a verified result hash.
- `pull` validates the remote artifact hash before retaining the local file.
- The Contabo control plane is deployed as a native systemd service with a dedicated user and TLS.
- Live control plane login succeeds with certificate fingerprint pinning.
- `status` refreshes redacted Daytona logs and synchronizes state through the control plane.
- A live GPT-5.6 Luna request with reasoning effort `high` produced the status summary.
- The root `.env` is permission `0600` and is the only repository location containing actual secret values.
- Git ignores `AGENTS.md`, `.agents/`, `.env` files, local state, npm credentials, archives, build output, logs, and editor metadata.
- `go test ./...` and `go vet ./...` pass.

## Release readiness

- GitHub Actions tests the project and builds Linux amd64 plus macOS arm64 binaries.
- The public repository is `https://github.com/jbaehova/onthego`.
- GitHub Actions run `35426890735` passed tests, vet, both platform builds, npm publishing, and GitHub Release creation on commit `15634ad`.
- The workflow assembles and validates an npm package before publishing.
- The installed npm binary dispatches to the correct bundled Go executable.
- The desired unscoped package name `onthego` is already owned by another npm account. The prepared package is `@jbaehova/onthego` and still installs the `onthego` command.
- The repository `NPM_TOKEN` secret is configured with a granular npm token that can publish from CI with two-factor authentication bypass enabled.
- `@jbaehova/onthego@0.1.2` is published on npm with GitHub Actions provenance from commit `15634ad`.
- A fresh public-registry install of `0.1.2` exposes exactly the five Phase 1 commands, and repeated `onthego init` preserves the same project ID.
- GitHub Release `v0.1.2` contains the Linux amd64 and macOS arm64 binaries with matching checksum files.
- The release workflow uploads each checksum from one artifact path so retrying a release cannot create duplicate asset uploads.

## Backlog

- GPU fine-tuning integration.
- GPU model serving integration.
- Hugging Face connector and immutable revision workflow.
- Additional platform binaries beyond Linux amd64 and macOS arm64.
