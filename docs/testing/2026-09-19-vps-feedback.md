# VPS feedback follow-up, 2026-09-19

## What the independent report established

The supplied report tested a different VPS from the earlier demo. That target initially had no ONTHEGO installation. Its author installed the service, synchronized the root `.env` target, moved HTTPS from 8443 to 443, and disabled the accidental service on the previous host. Those historical actions are reported evidence, not actions repeated in this follow-up.

The report verified login, pin mismatch rejection, authentication failures, and state persistence. It explicitly did not test Daytona execution, a live LLM summary, or artifact download. Earlier demo results could not establish end-to-end readiness on the replacement VPS.

## Findings confirmed against the current repository and VPS

| Finding | Evidence | Resolution |
| --- | --- | --- |
| Repository default disagreed with deployed port | Server source defaulted to 8443; the current VPS listens on 443 | Default changed to 443; README corrected |
| Checked-in service lacked low-port capability | Live unit had `cap_net_bind_service`; repository unit did not | Added `AmbientCapabilities` and `CapabilityBoundingSet` with only `CAP_NET_BIND_SERVICE` |
| Multiple env files could select different targets | Root `.env` currently matches the live VPS, but deployment had no checked-in guard | Added root-only preflight and deployment scripts; URL must resolve to the configured VPS and present the expected TLS pin |
| `vps.env` escaped the existing filename rules | Git ignored `.env` and `.env.*`, and capture's secret classifier did not cover `*.env` | Added `*.env` to repository and init ignore rules, plus capture's secret classification |
| Pin verification ignored certificate dates | The TLS callback compared SHA-256 only | Updated client rejects expired and future certificates even when the pin matches |
| Certificate expiry had no automated check | Live certificate expires 2027-09-19 06:37:56 UTC | Installed a daily systemd check with a 30-day threshold; documented coordinated rotation |

The current VPS was already `enabled`, `active`, and `running` before the update, with `NRestarts=0`. It ran under `onthego`. `/etc/onthego.env` was `0600 root:root`, and `/var/lib/onthego/tls.key` was `0600 onthego:onthego`. Root `.env` was `0600`.

## Source and deployment validation

- `go test ./...` and `go vet ./...` passed.
- TLS tests cover a valid pin, an incorrect pin, an expired certificate with the correct pin, and a future certificate with the correct pin.
- Capture regression test confirms an untracked `vps.env` is omitted and ordinary explicit inclusion is rejected.
- Preflight checks confirmed stale shell target variables are ignored, unsafe root `.env` permissions are rejected, and duplicate keys are rejected.
- Root credential values were checked against tracked and nonignored candidate files; no matches were found.
- The updater verified the target, uploaded the Linux amd64 server, compared its SHA-256, validated the systemd units, restarted the service, and passed external pinned health verification.
- The daily certificate timer was enabled and its first expiry check succeeded.

Deployed server SHA-256:

```text
e1a1285e59343041209f727b781bdd78910ca447b5eca1cf9b336a4f6c324e04
```

## Live end-to-end result on the replacement VPS

Completed at approximately 2026-09-19 06:48 UTC with the newly built CLI. An isolated client data directory preserved the user's existing login and local state. Credentials came from the current root `.env` only.

| Step | Observed result |
| --- | --- |
| `login` | Exit 0 against the current pinned control plane |
| `pass` | Exit 0; new Daytona sandbox and process receipt created |
| `status` | Exit 0; Daytona `SUCCEEDED`, no `refresh_error`, no `summary_error` |
| LLM summary | Nonempty live response from `gpt-5.6-luna` with reasoning effort `high` |
| `pull` | Exit 0; local artifact retained after receipt hash verification |

Transfer ID: `3438d472-f01b-4b5c-b6e6-af7c6381615b`.

Sandbox ID: `f57cc427-a44c-4682-b392-a7a91fbdea1b`.

Process command ID: `b4520604-3ab8-43fd-a543-288b1dd6f2f5`.

The downloaded `HANDOFF.md` and its remote receipt both have SHA-256:

```text
becb38edb431365e6be7a118c77ae3dd29e53dc06a3a18f349c070d55119d0b3
```

The tested default `agent-sync` workload copies the handoff artifact. This verifies context transfer, remote execution, state synchronization, a live summary, and verified artifact retrieval. It does not establish autonomous remote coding or GPU execution.

## Remaining operating constraints

The expiry timer records a failed unit and journal entry; it does not deliver an external notification. Certificate replacement still requires a coordinated client pin update. Automatic renewal is not claimed.

The existing-installation updater requires Python 3, OpenSSL, OpenSSH, `sshpass`, and a verified SSH host key. It does not provision a blank VPS. It preserves runtime credentials and project data.

These changes were initially verified from source while npm remained at `0.1.2`. They are included in the `0.1.3` release.
