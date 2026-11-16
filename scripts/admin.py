#!/usr/bin/env python3
"""Local/tunnel-only dead-letter operations. ADMIN_TOKEN comes from environment."""
import argparse
import json
import os
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("action", choices=["list", "retry", "status"])
parser.add_argument("id", nargs="?", type=int)
parser.add_argument("--status", default="dead", choices=["dead", "pending", "done", "ignored"])
args = parser.parse_args()
token = os.environ.get("ADMIN_TOKEN", "")
if not token:
    parser.error("export ADMIN_TOKEN first")
if args.action == "retry" and args.id is None:
    parser.error("retry requires an update ID")
base = "http://127.0.0.1:8080/admin/jobs"
url = "http://127.0.0.1:8080/admin/status" if args.action == "status" else base + ("?status=" + args.status if args.action == "list" else f"/{args.id}/retry")
req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token},
                             method="POST" if args.action == "retry" else "GET")
try:
    with urllib.request.urlopen(req, timeout=15) as response:
        body = response.read()
        print(json.dumps(json.loads(body), ensure_ascii=False, indent=2) if body else response.status)
except urllib.error.HTTPError as error:
    raise SystemExit(f"HTTP {error.code}: use a dead job ID, and check ADMIN_TOKEN")
