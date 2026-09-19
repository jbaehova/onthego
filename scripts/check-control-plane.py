#!/usr/bin/env python3
"""Read only this repository's root .env; verify target, TLS and external health."""

import hashlib
import hmac
import http.client
import json
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]


def read_config():
    path = ROOT / ".env"
    if path.stat().st_mode & 0o077:
        raise ValueError("root .env must have permission 0600")
    values = {}
    for number, line in enumerate(path.read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, raw = line.partition("=")
        key, raw = key.strip(), raw.strip()
        if not separator or not re.fullmatch(r"[A-Z_][A-Z_0-9]*", key) or key in values:
            raise ValueError(f"invalid or duplicate root .env key on line {number}")
        try:
            values[key] = json.loads(raw) if raw.startswith('"') else raw
        except ValueError:
            raise ValueError(f"invalid root .env value on line {number}") from None
    return values


def check(values):
    target = values["CONTABO_VPS_IP_ADDRESS"]
    url = urlsplit(values["ONTHEGO_CONTROL_PLANE_URL"])
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        raise ValueError("control plane URL must be an HTTPS origin")
    addresses = {item[4][0] for item in socket.getaddrinfo(url.hostname, url.port or 443, type=socket.SOCK_STREAM)}
    if target not in addresses:
        raise ValueError("control plane URL does not resolve to the root .env VPS target")
    expected = values["ONTHEGO_CONTROL_PLANE_TLS_SHA256"].replace(":", "").lower()
    if not re.fullmatch(r"[a-f0-9]{64}", expected):
        raise ValueError("invalid TLS SHA-256 pin")
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE  # Explicit pin and validity checked before HTTP.
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    connection = http.client.HTTPSConnection(url.hostname, url.port or 443, context=context, timeout=15)
    try:
        connection.connect()
        der = connection.sock.getpeercert(binary_form=True)
        if not hmac.compare_digest(hashlib.sha256(der).hexdigest(), expected):
            raise ValueError("control plane certificate fingerprint mismatch")
        dates = subprocess.run(["openssl", "x509", "-inform", "DER", "-noout", "-startdate", "-enddate"], input=der, capture_output=True, check=True).stdout.decode()
        import time
        validity = dict(line.split("=", 1) for line in dates.splitlines())
        now = time.time()
        if not ssl.cert_time_to_seconds(validity["notBefore"]) <= now <= ssl.cert_time_to_seconds(validity["notAfter"]):
            raise ValueError("control plane certificate is expired or not yet valid")
        remaining = int((ssl.cert_time_to_seconds(validity["notAfter"]) - now) / 86400)
        connection.request("GET", "/v1/health")
        response = connection.getresponse()
        payload = json.loads(response.read(65536))
        if response.status != 200 or payload.get("ok") is not True or payload.get("schema_version") != 1:
            raise ValueError("control plane health check failed")
        print(f"PASS: root .env target, TLS pin and external health; port {url.port or 443}; certificate has {remaining} days remaining")
        if remaining < 30:
            raise ValueError("certificate rotation is due within 30 days")
    finally:
        connection.close()


if __name__ == "__main__":
    try:
        check(read_config())
    except Exception as error:
        # Network exceptions can contain the private VPS address. Print no raw exception.
        message = str(error) if type(error) is ValueError else type(error).__name__
        print(f"FAIL: {message}", file=sys.stderr)
        sys.exit(1)
