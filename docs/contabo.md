# Contabo operations

The native control plane listens on HTTPS port **443** by default. The systemd unit runs as `onthego` with only `CAP_NET_BIND_SERVICE` available. There is no Docker dependency. An explicit `ONTHEGO_CONTROL_PLANE_LISTEN` in `/etc/onthego.env` overrides the default; use `:443` on this VPS.

## One deployment target

Keep local credentials only in the repository root `.env`, with permission `0600`. Do not create or source `vps.env`. Both operations scripts read the root `.env` directly, independent of the current directory and inherited environment variables. They never print credentials or the VPS address.

Required root keys are `CONTABO_VPS_IP_ADDRESS`, `CONTABO_VPS_DEFAULT_USER`, `CONTABO_VPS_PASSWORD`, `ONTHEGO_CONTROL_PLANE_URL`, and `ONTHEGO_CONTROL_PLANE_TLS_SHA256`. The URL must resolve to the configured VPS. The SSH account for deployment is `root`; the service account remains `onthego`.

```sh
python3 scripts/check-control-plane.py
```

This checks the target, certificate pin, certificate validity, and external `/v1/health`. It fails when fewer than 30 days remain. It does not prove Daytona execution or LLM access.

## Update the existing installation

Requirements: Python 3, OpenSSL, OpenSSH, `sshpass`, Go, and a previously verified SSH host key in `known_hosts`. The update command deliberately refuses an uninstalled or unhealthy endpoint. It does not provision a new machine or rotate secrets.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/onthego-server-linux-amd64 ./cmd/onthego-server
python3 scripts/deploy-control-plane.py
```

The script verifies the pinned external endpoint before opening SSH, checks the uploaded binary SHA-256, validates the systemd units, replaces the binary atomically, and checks external health again. On failure after replacement, it restores the previous binary and service unit. It leaves `/etc/onthego.env`, project data, and TLS material intact. A failed update preserves its root-only staging directory for diagnosis.

The installed runtime environment `/etc/onthego.env` is `0600 root:root`. The TLS key is `0600 onthego:onthego`. The public certificate is `0644`. Only the control-plane runtime secrets belong on the server; npm, Daytona, and OpenAI credentials are not copied by this updater.

## Certificate monitoring and rotation

The updater installs `onthego-certificate-check.timer`. Its daily check fails when the certificate has less than 30 days left. This creates a failed systemd unit and journal entry; it does **not** send a notification. Monitor it with your existing server monitoring or inspect:

```sh
systemctl status onthego-certificate-check.timer
systemctl status onthego-certificate-check.service
journalctl -u onthego-certificate-check.service
```

Automatic certificate replacement is intentionally not enabled: the CLI pins the certificate's DER SHA-256, so replacing it without updating clients breaks login and synchronization. Plan a coordinated maintenance window:

1. Generate replacement TLS material on the VPS. Keep the previous certificate and key in a root-only backup until validation passes.
2. Obtain the replacement certificate fingerprint over the verified SSH connection using `openssl x509 -in NEW_CERT -outform DER | openssl dgst -sha256`. Never fetch a replacement pin from an unverified HTTPS connection.
3. Install the new certificate and key with the permissions above. Restart `onthego`.
4. Update `ONTHEGO_CONTROL_PLANE_TLS_SHA256` in each client's root `.env`. Run the external check, then `onthego login` and `onthego status`.
5. If validation fails, restore both the previous TLS files and client pin. Keep the current pin until the planned rotation; the renewal procedure does not run during ordinary deployment.

Clients using the updated source reject expired and not-yet-valid pinned certificates as well as incorrect pins. The published `0.1.2` CLI checks the fingerprint but does not enforce certificate dates.

## Acceptance scope

Treat control-plane validation and Daytona validation separately. A health response and working login do not prove that `pass`, live `status` summaries, or verified `pull` work. See [the feedback validation record](testing/2026-09-19-vps-feedback.md) for the current environment's evidence and remaining limitations.
