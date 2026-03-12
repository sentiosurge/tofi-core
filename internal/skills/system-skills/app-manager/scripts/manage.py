#!/usr/bin/env python3
"""Tofi App Manager — CLI for managing apps via the Tofi API.

Usage:
    python3 manage.py list
    python3 manage.py get <app_id>
    python3 manage.py create --name "..." --prompt "..." [options]
    python3 manage.py update <app_id> [options]
    python3 manage.py delete <app_id>
    python3 manage.py activate <app_id>
    python3 manage.py deactivate <app_id>
    python3 manage.py run <app_id>

Environment:
    TOFI_API_URL  — API base URL (default: http://localhost:8080/api/v1)
    TOFI_TOKEN    — JWT auth token (required)
"""

import argparse
import json
import os
import sys
import urllib.request
import urllib.error

API_URL = os.environ.get("TOFI_API_URL", "http://localhost:8080/api/v1")
TOKEN = os.environ.get("TOFI_TOKEN", "")


def api(method, path, data=None):
    """Make an API request and return parsed JSON."""
    url = f"{API_URL}{path}"
    body = json.dumps(data).encode() if data else None
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN:
        req.add_header("Authorization", f"Bearer {TOKEN}")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode()
            if not raw:
                return {"status": "ok"}
            return json.loads(raw)
    except urllib.error.HTTPError as e:
        body = e.read().decode()
        print(f"ERROR {e.code}: {body}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)


def cmd_list(_args):
    apps = api("GET", "/apps")
    if not apps:
        print("No apps found.")
        return
    for app in apps:
        status = "active" if app.get("is_active") else "inactive"
        print(f"  [{status}] {app['id'][:8]}  {app['name']}")
        if app.get("description"):
            print(f"           {app['description']}")
    print(f"\nTotal: {len(apps)} app(s)")


def cmd_get(args):
    app = api("GET", f"/apps/{args.app_id}")
    print(json.dumps(app, indent=2, ensure_ascii=False))


def cmd_create(args):
    data = {"name": args.name, "prompt": args.prompt}
    if args.description:
        data["description"] = args.description
    if args.model:
        data["model"] = args.model
    if args.system_prompt:
        data["system_prompt"] = args.system_prompt
    if args.skills:
        data["skills"] = [s.strip() for s in args.skills.split(",")]
    if args.schedule:
        data["schedule_rules"] = json.loads(args.schedule)
    if args.capabilities:
        data["capabilities"] = json.loads(args.capabilities)

    result = api("POST", "/apps", data)
    print(f"Created app: {result.get('id', 'unknown')}")
    print(json.dumps(result, indent=2, ensure_ascii=False))


def cmd_update(args):
    data = {}
    if args.name:
        data["name"] = args.name
    if args.prompt:
        data["prompt"] = args.prompt
    if args.description:
        data["description"] = args.description
    if args.model:
        data["model"] = args.model
    if args.system_prompt:
        data["system_prompt"] = args.system_prompt
    if args.skills:
        data["skills"] = [s.strip() for s in args.skills.split(",")]
    if args.schedule:
        data["schedule_rules"] = json.loads(args.schedule)
    if args.capabilities:
        data["capabilities"] = json.loads(args.capabilities)

    if not data:
        print("Nothing to update (no options specified).")
        return

    result = api("PUT", f"/apps/{args.app_id}", data)
    print(f"Updated app: {args.app_id}")
    print(json.dumps(result, indent=2, ensure_ascii=False))


def cmd_delete(args):
    api("DELETE", f"/apps/{args.app_id}")
    print(f"Deleted app: {args.app_id}")


def cmd_activate(args):
    api("POST", f"/apps/{args.app_id}/activate")
    print(f"Activated app: {args.app_id}")


def cmd_deactivate(args):
    api("POST", f"/apps/{args.app_id}/deactivate")
    print(f"Deactivated app: {args.app_id}")


def cmd_run(args):
    result = api("POST", f"/apps/{args.app_id}/run")
    print(f"Triggered run for app: {args.app_id}")
    print(json.dumps(result, indent=2, ensure_ascii=False))


def main():
    if not TOKEN:
        print("ERROR: TOFI_TOKEN environment variable is required.", file=sys.stderr)
        sys.exit(1)

    parser = argparse.ArgumentParser(description="Tofi App Manager")
    sub = parser.add_subparsers(dest="command", required=True)

    # list
    sub.add_parser("list", help="List all apps")

    # get
    p = sub.add_parser("get", help="Get app details")
    p.add_argument("app_id")

    # create
    p = sub.add_parser("create", help="Create a new app")
    p.add_argument("--name", required=True)
    p.add_argument("--prompt", required=True)
    p.add_argument("--description", default="")
    p.add_argument("--model", default="")
    p.add_argument("--system-prompt", default="")
    p.add_argument("--skills", default="")
    p.add_argument("--schedule", default="")
    p.add_argument("--capabilities", default="")

    # update
    p = sub.add_parser("update", help="Update an existing app")
    p.add_argument("app_id")
    p.add_argument("--name", default="")
    p.add_argument("--prompt", default="")
    p.add_argument("--description", default="")
    p.add_argument("--model", default="")
    p.add_argument("--system-prompt", default="")
    p.add_argument("--skills", default="")
    p.add_argument("--schedule", default="")
    p.add_argument("--capabilities", default="")

    # delete
    p = sub.add_parser("delete", help="Delete an app")
    p.add_argument("app_id")

    # activate / deactivate / run
    for cmd in ("activate", "deactivate", "run"):
        p = sub.add_parser(cmd, help=f"{cmd.capitalize()} an app")
        p.add_argument("app_id")

    args = parser.parse_args()
    {
        "list": cmd_list,
        "get": cmd_get,
        "create": cmd_create,
        "update": cmd_update,
        "delete": cmd_delete,
        "activate": cmd_activate,
        "deactivate": cmd_deactivate,
        "run": cmd_run,
    }[args.command](args)


if __name__ == "__main__":
    main()
