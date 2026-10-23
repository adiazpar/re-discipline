"""Exercise shipped artifacts and both MCP adapters without a model or account.

Run from any directory with Python 3. Tests use disposable copies of the fixture
and plugin, including installation paths with spaces, quotes and Unicode.
"""
import json
from contextlib import contextmanager
import os
import platform
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time


REPO = Path(__file__).resolve().parent.parent


def run(command, **kwargs):
    return subprocess.run(command, check=True, text=True, capture_output=True,
                          timeout=60, **kwargs)


def rpc(i, method, params):
    return {"jsonrpc": "2.0", "id": i, "method": method, "params": params}


def engine_command(command):
    # Python's Windows launcher selects the extensionless Unix script when it
    # exists beside the EXE. Both real hosts resolve the EXE themselves; the
    # --hosts checks below cover that behavior independently of this adapter.
    if os.name == "nt" and not Path(command).suffix:
        return command + ".exe"
    return command


@contextmanager
def workspace():
    directory = tempfile.TemporaryDirectory(prefix="re-discipline-smoke-")
    try:
        yield directory.name
    finally:
        # Windows may retain an executable lock briefly after process exit.
        # Retry removal without suppressing persistent cleanup failures.
        deadline = time.monotonic() + 2
        while True:
            try:
                directory.cleanup()
                break
            except PermissionError:
                if os.name != "nt" or time.monotonic() >= deadline:
                    raise
                time.sleep(.05)


def check_codex_host(codex, project, golden):
    """Query the installed plugin through Codex, without starting a model turn."""
    with tempfile.TemporaryFile(mode="w+") as errors:
        process = subprocess.Popen([codex, "app-server", "--stdio"], cwd=project,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=errors, text=True)
        messages = queue.Queue()

        def read_messages():
            for line in process.stdout:
                messages.put(json.loads(line))
            messages.put(None)

        reader = threading.Thread(target=read_messages, daemon=True)
        reader.start()
        sequence = 0

        def call(method, params):
            nonlocal sequence
            sequence += 1
            process.stdin.write(json.dumps(rpc(sequence, method, params)) + "\n")
            process.stdin.flush()
            deadline = time.monotonic() + 60
            while True:
                message = messages.get(timeout=max(0.01, deadline - time.monotonic()))
                assert message is not None, "Codex app-server exited before responding"
                if message.get("id") == sequence:
                    assert "error" not in message, message
                    return message["result"]

        try:
            call("initialize", {"clientInfo": {"name": "re-discipline-smoke", "version": "1"},
                                "capabilities": {"experimentalApi": True}})
            process.stdin.write('{"jsonrpc":"2.0","method":"initialized"}\n')
            process.stdin.flush()
            thread = call("thread/start", {"cwd": str(project), "ephemeral": True,
                                          "approvalPolicy": "never", "sandbox": "workspace-write"})
            thread_id = thread["thread"]["id"]
            for case in golden:
                response = call("mcpServer/tool/call", {"threadId": thread_id, "server": "re-search",
                    "tool": "query", "arguments": {"root": str(project), "query": case["q"],
                                                    "sources": "local", "limit": 1}})
                answer = json.loads(response["content"][0]["text"])
                assert answer["hits"][0]["path"] == case["expect"], (case, answer)
            skills = call("skills/list", {"cwds": [str(project)], "forceReload": True})
            found = [skill for entry in skills["data"] for skill in entry["skills"]
                     if "/re-discipline/" in skill["path"].replace("\\", "/")]
            assert len(found) == 10, found
            print(f"Real Codex host: 10 skills, {len(golden)}/{len(golden)} corpus answers through installed MCP plugin")
        except Exception:
            errors.seek(0)
            print(errors.read()[-4000:], file=sys.stderr)
            raise
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
            process.stdin.close()
            reader.join(timeout=2)
            process.stdout.close()


def main():
    print(f"Test process: {platform.system()} {platform.machine()}", flush=True)
    with workspace() as folder:
        work = Path(folder)
        plugin = work / "plugin space ' $ café"
        plugin.mkdir()
        for name in ["bin", ".claude-plugin", ".codex-plugin"]:
            shutil.copytree(REPO / name, plugin / name)
        project = work / "project space café"
        shutil.copytree(REPO / "retrieval/testdata/fixture", project,
                        ignore=shutil.ignore_patterns("index.db*", "index.lock"))
        binary = plugin / "bin" / ("re-search.exe" if os.name == "nt" else "re-search")
        version = json.loads((plugin / ".claude-plugin/plugin.json").read_text())["version"]
        assert run([str(binary), "--version"], cwd=work).stdout.strip() == version
        installed = json.loads(run([str(binary), "install-binary", "--root", str(project)], cwd=work).stdout)
        local_binary = project / installed["path"]
        arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine().lower()]
        artifact = (plugin / "bin/re-search.exe" if os.name == "nt" else
                    plugin / "bin" / f"{platform.system().lower()}-{arch}" / "re-search")
        assert local_binary.read_bytes() == artifact.read_bytes(), "Launcher selected the wrong native artifact"
        assert run([str(local_binary), "--version"], cwd=work).stdout.strip() == version
        # An immediate refresh must tolerate Windows releasing the exited
        # executable's image lock. The replacement has not been run yet, so
        # it can also safely stand in for a stale artifact in the next check.
        run([str(binary), "install-binary", "--root", str(project)], cwd=work)
        # Refresh a stale copy, then confirm running init from that copy is safe.
        local_binary.write_bytes(b"stale binary")
        run([str(binary), "install-binary", "--root", str(project)], cwd=work)
        run([str(local_binary), "install-binary", "--root", str(project)], cwd=work)
        assert run([str(local_binary), "--version"], cwd=work).stdout.strip() == version
        assert not list(local_binary.parent.glob(".re-search-*"))
        print("Project binary: automatic native selection, refresh, and self-install passed")
        bench = run([str(binary), "bench", "--root", str(project), "--json"], cwd=work)
        print("CLI corpus:", bench.stdout.strip())

        golden = [json.loads(line) for line in
                  (project / ".re-discipline/golden.jsonl").read_text().splitlines() if line]
        for provider in ["claude", "codex"]:
            manifest = json.loads((plugin / f".{provider}-plugin/plugin.json").read_text())
            config = json.loads((plugin / manifest["mcpServers"]).read_text())["mcpServers"]["re-search"]
            command = config["command"].replace("${CLAUDE_PLUGIN_ROOT}", str(plugin))
            cwd = plugin / config["cwd"] if "cwd" in config else work
            # Resolve relative executable paths against the configured child cwd,
            # as Codex does; Python/Windows otherwise uses its parent cwd.
            if not Path(command).is_absolute():
                command = str(cwd / command)
            requests = [rpc(1, "initialize", {"protocolVersion": "2024-11-05"}),
                        rpc(2, "tools/list", {})]
            for i, case in enumerate(golden, start=3):
                requests.append(rpc(i, "tools/call", {"name": "query", "arguments": {
                    "root": str(project), "query": case["q"], "sources": "local", "limit": 1}}))
            result = run([engine_command(command), *config["args"]], cwd=cwd,
                         input="".join(json.dumps(req) + "\n" for req in requests))
            responses = [json.loads(line) for line in result.stdout.splitlines()]
            assert len(responses) == len(requests), result.stdout
            assert responses[0]["result"]["serverInfo"]["version"] == version
            assert {t["name"] for t in responses[1]["result"]["tools"]} == {"query", "symbol", "community"}
            for response, case in zip(responses[2:], golden):
                answer = json.loads(response["result"]["content"][0]["text"])
                assert answer["hits"][0]["path"] == case["expect"], (provider, case, answer)
            print(f"{provider} MCP adapter: handshake, 3 tools, {len(golden)}/{len(golden)} golden answers at rank 1")

        env = dict(os.environ, CLAUDE_PLUGIN_ROOT=str(plugin))
        hook_input = json.dumps({"cwd": str(project)})
        claude_hook = json.loads((plugin / ".claude-plugin/hooks.json").read_text())["hooks"]["SessionStart"][0]["hooks"][0]
        hook_command = claude_hook["command"].replace("${CLAUDE_PLUGIN_ROOT}", str(plugin))
        result = run([engine_command(hook_command), *claude_hook["args"]], input=hook_input, cwd=work, env=env)
        assert "docs curated." in result.stdout, result.stdout
        codex_hook = json.loads((plugin / ".codex-plugin/hooks.json").read_text())["hooks"]["SessionStart"][0]["hooks"][0]
        shells = [["/bin/sh", "-c", codex_hook["command"]]]
        if os.name == "nt":
            # Let Python quote the complete command for cmd.exe rather than
            # applying C argv escaping to a nested cmd /c command string.
            result = run(codex_hook["commandWindows"], shell=True,
                         input=hook_input, cwd=work, env=env)
            assert "docs curated." in result.stdout, result.stdout
            shells = [["powershell.exe", "-NoProfile", "-Command", codex_hook["commandWindows"]]]
        for shell in shells:
            result = run(shell, input=hook_input, cwd=work, env=env)
            assert "docs curated." in result.stdout, result.stdout
        print("Claude exec hook and Codex shell hook: workspace summary passed")

        if "--hosts" in sys.argv:
            # CI installs both hosts and this marketplace before this check.
            # Verify the real Claude process launcher too, especially .exe
            # resolution on Windows where no POSIX shell may be required.
            claude = shutil.which("claude")
            codex = shutil.which("codex")
            assert claude and codex, "--hosts requires installed Claude and Codex CLIs"
            health = run([claude, "mcp", "list"], cwd=project).stdout
            assert any("plugin:re-discipline:re-search" in line and "Connected" in line
                       for line in health.splitlines()), health
            plugins = json.loads(run([codex, "plugin", "list", "--marketplace", "re-discipline", "--json"], cwd=project).stdout)
            assert any(p["name"] == "re-discipline" and p["enabled"] for p in plugins["installed"]), plugins
            print("Real hosts: Claude MCP connection and Codex installed-plugin discovery passed")
            check_codex_host(codex, project, golden)


if __name__ == "__main__":
    main()
