"""Apply compatible plugin updates to an operator's registered deployment.

Deployment access lives in the operator's private home directory, never in a
project or community response. SSH uses existing keys and known_hosts. This
helper does not accept shell commands, download releases, or provision HTTPS.
"""
import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tarfile
import time

REPO = Path(__file__).resolve().parents[1]


def credential_path(service):
    return Path.home() / ".re-discipline/servers/credentials" / (hashlib.sha256(service.encode()).hexdigest() + ".json")


def read_private(path):
    info = path.lstat()
    if not path.is_file() or path.is_symlink() or info.st_size > 16384 or (os.name != "nt" and info.st_mode & 0o077):
        raise ValueError("Deployment registration must be a private regular file")
    return json.loads(path.read_text())


def save(path, data):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    tmp = path.with_name(path.name + ".tmp-" + str(os.getpid()))
    with tmp.open("w") as out:
        os.chmod(tmp, 0o600)
        json.dump(data, out)
    tmp.replace(path)


def ssh_command(host, source, values):
    if not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.@-]{0,253}", host):
        raise ValueError("Use an SSH config host alias (with an existing known-host entry)")
    # OpenSSH passes a remote shell string: quote code and JSON as shell words.
    encoded = base64.b64encode(json.dumps(values).encode()).decode()
    code = "import base64,json; values=json.loads(base64.b64decode(" + repr(encoded) + "));" + source
    return ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
            host, "python3 -c " + shlex.quote(code)]


REGISTER = '''
from pathlib import Path
state=Path(values['state']).expanduser().resolve()
config=json.loads((state/'installation.json').read_text())
owner=json.loads(Path(config['owner_file']).read_text())
assert owner['service']==values['service'] and owner['installation']==str(state)
assert config.get('public_url')==values['service'] and config.get('automatic_updates') is True
owner['installation']=str(state)
print(json.dumps(owner))
'''

REMOTE_UPDATE = '''
import os,sys,tarfile,subprocess,tempfile,shutil
from pathlib import Path
state=Path(values['state']).expanduser().resolve()
config=json.loads((state/'installation.json').read_text())
assert config.get('public_url')==values['service']
allowed={'bin/build.json','bin/server/linux-amd64/community','bin/server/linux-arm64/community','server/Dockerfile','scripts/community-local.py'}
with tempfile.TemporaryDirectory(prefix='re-discipline-update-') as folder:
 root=Path(folder); seen=set(); total=0
 with tarfile.open(fileobj=sys.stdin.buffer,mode='r|') as archive:
  for member in archive:
   assert member.name in allowed and member.isfile() and member.name not in seen
   total+=member.size; assert total<=150*1024*1024
   seen.add(member.name); destination=root/member.name; destination.parent.mkdir(parents=True,exist_ok=True)
   with destination.open('wb') as out: shutil.copyfileobj(archive.extractfile(member),out)
   destination.chmod(0o755 if member.name.endswith('/community') else 0o600)
 assert seen==allowed
 subprocess.run([sys.executable,str(root/'scripts/community-local.py'),'up','--automatic','--name',config['name'],'--state-dir',str(state)],check=True)
'''


def payload():
    # Only shipped software crosses SSH. No project, corpus, .git or credentials.
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as archive:
        for name in ["bin/build.json", "bin/server/linux-amd64/community", "bin/server/linux-arm64/community",
                     "server/Dockerfile", "scripts/community-local.py"]:
            archive.add(REPO / name, arcname=name, recursive=False)
    return output.getvalue()


def ensure(service, explicit=False):
    path = credential_path(service)
    if not path.exists():
        return {"status": "unmanaged"}
    owner = read_private(path)
    if owner.get("service") != service:
        raise ValueError("Owner registration does not match the requested origin")
    target = json.loads((REPO / "bin/build.json").read_text())
    status_path = path.with_suffix(".update.json")
    if status_path.exists() and not explicit:
        last = read_private(status_path)
        if last.get("target") == target and time.time() - last.get("checked_at", 0) < (2 if last.get("status") == "busy" else 300):
            return last
    if owner.get("ssh"):
        command = ssh_command(owner["ssh"], REMOTE_UPDATE, {"state": owner["installation"], "service": service})
        data = payload()
    else:
        state = Path(owner["installation"])
        config = read_private(state / "installation.json")
        if (config.get("public_url") or f"http://127.0.0.1:{config['port']}") != service:
            raise ValueError("Installation belongs to a different service")
        command = [sys.executable, str(REPO / "scripts/community-local.py"), "up", "--automatic",
                   "--name", config["name"], "--state-dir", str(state)]
        data = None
    try:
        result = subprocess.run(command, input=data, capture_output=True, timeout=180, check=True)
        report = json.loads(result.stdout)
    except (subprocess.SubprocessError, ValueError) as error:
        details = getattr(error, "stderr", b"") or b""
        if isinstance(details, bytes): details = details.decode("utf-8", "replace")
        save(path.with_suffix(".update-log.json"), {"error": type(error).__name__, "output": details[-65536:]})
        # Do not echo remote output: deployments can print private configuration.
        report = {"status": "failed", "message": "Managed update failed; the previous application is retained or restored on the next attempt. Inspect private deployment logs."}
    report.update(target=target, checked_at=time.time())
    save(status_path, report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["ensure", "register"])
    parser.add_argument("--service", required=True)
    parser.add_argument("--ssh", help="Existing SSH config host alias")
    parser.add_argument("--remote-state", help="Existing managed installation directory on the server")
    parser.add_argument("--retry", action="store_true")
    args = parser.parse_args()
    if args.command == "register":
        if not args.ssh or not args.remote_state or not args.service.startswith("https://"):
            parser.error("Remote registration requires --ssh, --remote-state and an HTTPS service origin")
        path = credential_path(args.service)
        if path.exists():
            raise ValueError("This service already has an owner registration; do not replace its identity")
        try:
            result = subprocess.run(ssh_command(args.ssh, REGISTER, {"state": args.remote_state, "service": args.service}),
                                    capture_output=True, timeout=20, check=True)
        except subprocess.SubprocessError as error:
            details = getattr(error, "stderr", b"") or b""
            if isinstance(details, bytes): details = details.decode("utf-8", "replace")
            save(path.with_suffix(".update-log.json"), {"error": type(error).__name__, "output": details[-65536:]})
            raise ValueError("Remote registration failed; inspect the private deployment log") from None
        owner = json.loads(result.stdout)
        if owner.get("service") != args.service or len(owner.get("token", "")) < 43:
            raise ValueError("Remote installation did not return a valid owner credential")
        owner["ssh"] = args.ssh
        save(path, owner)
        print(json.dumps({"registered": True, "service": args.service}))
    else:
        print(json.dumps(ensure(args.service, args.retry)))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError):
        print("Managed update failed; inspect the private installation and SSH configuration.", file=sys.stderr)
        sys.exit(1)
