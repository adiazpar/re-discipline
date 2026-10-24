"""Install, inspect, or stop a local single-owner community server using Docker Compose.

The server is bound to loopback with persistent PostgreSQL storage. The plugin
automatically discovers its private, origin-scoped owner credential. No corpus
is uploaded by this helper. Use --setup-only for health-only verification.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

REPO = Path(__file__).resolve().parents[1]
POSTGRES = "postgres:17-bookworm@sha256:051f7b7b3abdd564d5d1bd1e8c4b9c1b6e77087d1dd22020ede611c096a272e0"


def run(command, capture=False, timeout=900):
    return subprocess.run(command, check=True, text=True, timeout=timeout,
                          stdout=subprocess.PIPE if capture else sys.stderr,
                          stderr=subprocess.PIPE if capture else sys.stderr)


def save(path, text):
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as file:
        os.chmod(temporary, 0o600)
        file.write(text)
    temporary.replace(path)


def composition(state, name, port, owner=None):
    image = "re-discipline-server-local:" + name
    base = {"image": image, "networks": ["database"]}
    bootstrap = dict(base, profiles=["setup"], env_file=[str(state / ".env.admin")],
                     working_dir="/bootstrap", volumes=[{"type": "bind", "source": str(state), "target": "/bootstrap"}])
    if hasattr(os, "getuid"):
        bootstrap["user"] = f"{os.getuid()}:{os.getgid()}"
    else:
        bootstrap["user"] = "0:0"
    app = dict(base, command=["serve" if owner else "serve-setup"], restart="unless-stopped",
               env_file=[{"path": str(state / ".env.runtime"), "required": False}],
               environment={"PUBLIC_URL": f"http://127.0.0.1:{port}", "LISTEN_ADDR": ":8080", "GOMEMLIMIT": "384MiB"},
               ports=[f"127.0.0.1:{port}:8080"], networks=["database", "frontend"],
               depends_on={"database": {"condition": "service_healthy"}}, read_only=True,
               tmpfs=["/tmp:size=268435456,mode=1777"], cap_drop=["ALL"],
               security_opt=["no-new-privileges:true"], mem_limit="512m", pids_limit=128)
    if owner:
        app["environment"].update(SERVER_OPERATOR_ID=owner["owner_id"], SERVER_OPERATOR_TOKEN=owner["token"])
    database = {"image": POSTGRES, "restart": "unless-stopped",
                "environment": {"POSTGRES_DB": "community", "POSTGRES_PASSWORD_FILE": "/run/secrets/postgres_password", "POSTGRES_INITDB_ARGS": "--auth-host=scram-sha-256"},
                "secrets": ["postgres_password"], "volumes": ["database:/var/lib/postgresql/data"],
                "networks": ["database"], "mem_limit": "650m",
                "healthcheck": {"test": ["CMD-SHELL", "pg_isready -U postgres -d community"], "interval": "2s", "timeout": "5s", "retries": 30}}
    return {"name": "re-discipline-" + name,
            "services": {"database": database, "bootstrap": bootstrap, "app": app},
            "networks": {"database": {"internal": True}, "frontend": {}},
            "volumes": {"database": {}},
            "secrets": {"postgres_password": {"file": str(state / "postgres_password")}}}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def health(url, mode):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(url + "/healthz", timeout=5) as response:
        data = response.read(8193)
    if len(data) > 8192:
        raise ValueError("Health response exceeds limit")
    result = json.loads(data)
    if result.get("ok") is not True or result.get("protocol") != 1 or result.get("mode") != mode or result.get("community_enabled") is not (mode != "setup"):
        raise ValueError("Expected a healthy re-discipline server in " + mode + " mode")
    return result


def local_owner(state, url, existing):
    directory = Path.home() / ".re-discipline" / "servers" / "credentials"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(directory, 0o700)
    path = directory / (hashlib.sha256(url.encode()).hexdigest() + ".json")
    if path.exists():
        owner = json.loads(path.read_text())
        if owner.get("service") != url or owner.get("installation") != str(state):
            raise ValueError("This local origin already belongs to another installation; choose a different port")
        uuid.UUID(owner["owner_id"])
        if len(owner.get("token", "")) < 43:
            raise ValueError("Saved owner credential is invalid; restore it instead of replacing its identity")
        os.chmod(path, 0o600)
    else:
        if (existing or {}).get("owner_file"):
            raise ValueError("Saved owner credential is missing; restore it instead of creating a new owner")
        owner = {"service": url, "owner_id": str(uuid.uuid4()), "token": secrets.token_urlsafe(32),
                 "installation": str(state)}
        save(path, json.dumps(owner, indent=2) + "\n")
    return owner, str(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["up", "status", "stop"])
    parser.add_argument("--context", help="Local Docker context; use orbstack on this Mac")
    parser.add_argument("--name", default="local")
    parser.add_argument("--port", type=int, help="Host port for a new installation (default: 7346)")
    parser.add_argument("--state-dir", type=Path, help="Private persistent setup directory")
    parser.add_argument("--setup-only", action="store_true", help="up only: expose health checks instead of owner operations")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,39}", args.name) or (args.port is not None and not 1024 <= args.port <= 65535):
        parser.error("Use a lowercase name and an unprivileged port (1024–65535)")
    state = (args.state_dir or Path.home() / ".re-discipline" / "servers" / args.name).expanduser().resolve()
    config = state / "installation.json"
    existing = json.loads(config.read_text()) if config.exists() else None
    context = args.context or (existing or {}).get("context") or run(["docker", "context", "show"], capture=True).stdout.strip()
    docker = ["docker", "--context", context]
    info = json.loads(run(["docker", "context", "inspect", context], capture=True).stdout)[0]
    if not info["Endpoints"]["docker"]["Host"].startswith(("unix://", "npipe://")):
        raise ValueError("This installer is local-only; select a local Docker context")
    if existing and (existing["context"] != context or existing["name"] != args.name):
        raise ValueError("Saved installation belongs to a different context or name")
    if existing and args.port is not None and args.port != existing["port"]:
        raise ValueError("Saved installation uses a different port; use a new name for another installation")
    port = existing["port"] if existing else (args.port or 7346)
    url = f"http://127.0.0.1:{port}"
    mode = "setup" if args.setup_only else "ready"
    compose = docker + ["compose", "--project-name", "re-discipline-" + args.name, "--file", str(state / "compose.json")]
    if args.command != "up":
        if not existing:
            raise ValueError("No saved installation; run up first")
        if args.command == "stop":
            run(compose + ["stop"])
            print(json.dumps({"stopped": True, "data_preserved": True, "state_dir": str(state)}))
            return
        print(json.dumps({"service": url, "state_dir": str(state), **health(url, existing.get("mode", "setup"))}))
        return

    if not existing and state.exists():
        partial_files = {"postgres_password", ".env.admin", ".env.runtime"}
        if any(p.name not in partial_files for p in state.iterdir()):
            raise ValueError("State directory is not an empty or interrupted installation; choose a dedicated directory")
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(state, 0o700)
    password_file, admin_file = state / "postgres_password", state / ".env.admin"
    if existing and (not password_file.exists() or not admin_file.exists()):
        raise ValueError("Saved installation is missing credentials; restore them instead of resetting the database")
    if not password_file.exists():
        save(password_file, secrets.token_urlsafe(32) + "\n")
    if not admin_file.exists():
        password = password_file.read_text().strip()
        save(admin_file, "DATABASE_URL=postgres://postgres:" + password + "@database:5432/community?sslmode=disable\n")
    owner, owner_file = (None, (existing or {}).get("owner_file")) if args.setup_only else local_owner(state, url, existing)
    save(config, json.dumps({"context": context, "name": args.name, "port": port, "mode": mode,
                             "owner_file": owner_file}, indent=2) + "\n")
    save(state / "compose.json", json.dumps(composition(state, args.name, port, owner), indent=2) + "\n")
    run(docker + ["build", "--tag", "re-discipline-server-local:" + args.name, "--file", str(REPO / "server/Dockerfile"), str(REPO)])
    run(compose + ["up", "--detach", "--wait", "--wait-timeout", "90", "database"])
    run(compose + ["run", "--rm", "--no-deps", "bootstrap", "migrate"])
    if not (state / ".env.runtime").exists():
        run(compose + ["run", "--rm", "--no-deps", "bootstrap", "bootstrap-runtime"])
    run(compose + ["up", "--detach", "app"])
    deadline = time.monotonic() + 60
    while True:
        try:
            result = health(url, mode)
            break
        except (OSError, ValueError):
            if time.monotonic() >= deadline:
                raise RuntimeError("Server health did not become ready; inspect this installation's Compose logs") from None
            time.sleep(1)
    print(json.dumps({"service": url, "state_dir": str(state), **result}))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Local server setup failed: {error}", file=sys.stderr)
        sys.exit(1)
