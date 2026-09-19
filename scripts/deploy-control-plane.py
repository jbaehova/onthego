#!/usr/bin/env python3
"""Update an existing control plane using only repository root .env credentials."""

import hashlib
import os
from pathlib import Path
import runpy
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
preflight = runpy.run_path(str(ROOT / "scripts/check-control-plane.py"))


def deploy():
    values = preflight["read_config"]()
    # Authenticate the configured endpoint before changing anything over SSH.
    preflight["check"](values)
    if values["CONTABO_VPS_DEFAULT_USER"] != "root":
        raise ValueError("deployment requires the configured root SSH account")
    binary = (ROOT / "dist/onthego-server-linux-amd64").read_bytes()
    if binary[:4] != b"\x7fELF" or binary[18:20] != b"\x3e\x00":
        raise ValueError("build the Linux amd64 server binary before deploying")
    environment = dict(os.environ, SSHPASS=values["CONTABO_VPS_PASSWORD"])
    ssh = ["sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "root@" + values["CONTABO_VPS_IP_ADDRESS"]]

    def remote(command, data=None):
        return subprocess.run(ssh + [command], input=data, env=environment, capture_output=True, check=True, timeout=90).stdout

    stage = "/var/lib/onthego/.deploy-" + uuid.uuid4().hex
    remote(f"set -eu; test -x /usr/local/bin/onthego-server; test -f /etc/onthego.env; test -f /etc/systemd/system/onthego.service; mkdir -m 700 {stage}; cp /usr/local/bin/onthego-server {stage}/previous; cp /etc/systemd/system/onthego.service {stage}/previous.service")
    changed = False
    try:
        remote(f"umask 077; cat > {stage}/onthego-server", binary)
        expected = hashlib.sha256(binary).hexdigest()
        actual = remote(f"sha256sum {stage}/onthego-server").decode().split()[0]
        if actual != expected:
            raise ValueError("uploaded server checksum mismatch")
        for name in ("onthego.service", "onthego-certificate-check.service", "onthego-certificate-check.timer"):
            remote(f"umask 077; cat > {stage}/{name}", (ROOT / "deploy/contabo" / name).read_bytes())
        remote(f"systemd-analyze verify {stage}/onthego.service {stage}/onthego-certificate-check.service {stage}/onthego-certificate-check.timer")
        changed = True
        remote(f"set -eu; install -m 755 {stage}/onthego-server /usr/local/bin/onthego-server.next; mv /usr/local/bin/onthego-server.next /usr/local/bin/onthego-server; install -m 644 {stage}/onthego.service /etc/systemd/system/onthego.service; systemctl daemon-reload; systemctl restart onthego")
        # A restarted process may need a moment to bind its listener.
        for attempt in range(5):
            try:
                preflight["check"](values)
                break
            except Exception:
                if attempt == 4:
                    raise
                time.sleep(1)
        remote(f"set -eu; install -m 644 {stage}/onthego-certificate-check.service {stage}/onthego-certificate-check.timer /etc/systemd/system/; systemctl daemon-reload; systemctl enable --now onthego-certificate-check.timer; systemctl start onthego-certificate-check.service; systemctl is-active --quiet onthego")
        print("PASS: server deployed, external health verified, daily certificate check enabled")
        print("server_sha256=" + expected)
    except Exception:
        if changed:
            remote(f"set -eu; install -m 755 {stage}/previous /usr/local/bin/onthego-server.next; mv /usr/local/bin/onthego-server.next /usr/local/bin/onthego-server; install -m 644 {stage}/previous.service /etc/systemd/system/onthego.service; systemctl daemon-reload; systemctl restart onthego")
            print("Restored previous server binary and service unit", file=sys.stderr)
        raise
    else:
        remote(f"rm -rf {stage}")


if __name__ == "__main__":
    try:
        deploy()
    except Exception as error:
        message = str(error) if type(error) is ValueError else type(error).__name__
        print(f"Deployment failed: {message}", file=sys.stderr)
        sys.exit(1)
