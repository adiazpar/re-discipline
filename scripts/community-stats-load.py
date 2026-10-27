"""Compare remote search with and without summary traffic on a disposable Linux server.

Uses the local OrbStack installation's PostgreSQL service, but creates and drops
a separate database. The native load generator runs outside the server's CPU
limit. Temporary reader keys never enter the report. Requires Docker and Go.
"""
import argparse
import json
import os
from pathlib import Path
import runpy
import socket
import subprocess
import tempfile
import time
import uuid

from smoke import engine_command, run

REPO = Path(__file__).resolve().parents[1]


def summary_smoke(manifest):
    """Execute the summary skill's commands through both real MCP adapters."""
    fixture = json.loads(manifest.read_text())
    Adapter = runpy.run_path(str(REPO / 'scripts/community-smoke.py'))['Adapter']
    with tempfile.TemporaryDirectory(prefix='re-summary-smoke-') as temp:
        for provider in ('claude', 'codex'):
            root = Path(temp) / provider
            docs = root / '.re-discipline/docs'
            docs.mkdir(parents=True)
            source = fixture['Communities'][0]['Anchors'][0]
            (docs / 'finding.md').write_text(source['markdown'])
            adapter = Adapter(provider, root, fixture['URL'], reader=True)
            aliases = []
            try:
                for i in (0, 2):
                    community = fixture['Communities'][i]
                    alias = 'summary-' + str(i)
                    adapter.community('connect', community=community['ID'], alias=alias)
                    aliases.append(alias)
                command = [engine_command(str(REPO / 'bin/re-search'))]
                local = json.loads(run(command + ['stats', '--root', str(root), '--json'], cwd=root).stdout)
                assert local['documents'] == 1, local
                run(command + ['--version'], cwd=root)
                assert len(adapter.community('connections')['connections']) == 2
                for i, alias in zip((0, 2), aliases):
                    expected = fixture['Communities'][i]
                    first = adapter.community('community.stats', alias=alias)
                    assert first['community_id'] == expected['ID'] and first['documents'] == 512
                    assert first['knowledge_bytes'] == expected['Bytes'] and not first['cached']
                    second = adapter.community('community.stats', alias=alias)
                    assert second['cached'] and second['observed_at'] == first['observed_at']
                    adapter.tool('community', {'root': str(root), 'action': 'community.stats', 'alias': alias,
                                               'community': fixture['Communities'][1]['ID']}, error='conflicts')
                print(provider + ': summary workflow, scoped counts, persistent cache and conflicting destination rejection passed', flush=True)
            finally:
                for alias in aliases:
                    adapter.community('disconnect', alias=alias)
                assert not list((root / '.re-discipline/community').glob('summary-stats-*.json'))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state-dir', type=Path, default=Path.home() / '.re-discipline/servers/local')
    parser.add_argument('--seconds', type=int, default=15)
    parser.add_argument('--rounds', type=int, default=3)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--summary-smoke', action='store_true', help='Also exercise freshly built MCP adapters and OS-stored reader keys')
    args = parser.parse_args()
    if not (5 <= args.seconds <= 120 and 1 <= args.rounds <= 10):
        parser.error('Use 5–120 seconds and 1–10 rounds')
    state = args.state_dir.expanduser().resolve()
    installation = json.loads((state / 'installation.json').read_text())
    docker = ['docker', '--context', installation['context']]
    context = json.loads(subprocess.check_output(docker + ['context', 'inspect']))[0]
    if not context['Endpoints']['docker']['Host'].startswith(('unix://', 'npipe://')):
        parser.error('This test requires a local Docker context')
    image = 're-discipline-stats-tests'
    subprocess.run(docker + ['build', '--target', 'tests', '-t', image, '-f', str(REPO / 'server/Dockerfile'), str(REPO)], check=True)
    env = dict(os.environ, TEST_DATABASE_URL=(state / '.env.admin').read_text().strip().split('=', 1)[1])
    network = 're-discipline-' + installation['name'] + '_database'
    subprocess.run(docker + ['run', '--rm', '--network', network, '-e', 'TEST_DATABASE_URL', image,
                             '/community-tests', '-test.run=^TestCommunityStatistics', '-test.v'], env=env, check=True)
    report = args.report.expanduser().resolve()
    report.parent.mkdir(parents=True, exist_ok=True)
    name = 're-discipline-stats-load-' + uuid.uuid4().hex[:10]
    with tempfile.TemporaryDirectory(prefix='re-stats-load-') as temp:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        env.update(STATS_LOAD_FIXTURE='/fixture', STATS_LOAD_MODE='server',
                   STATS_LOAD_URL=f'http://127.0.0.1:{port}', GOMEMLIMIT='384MiB')
        cmd = docker + ['run', '--rm', '-d', '--name', name, '--network',
                        're-discipline-' + installation['name'] + '_frontend', '--network', network,
                        '--cpus', '2', '--memory', '512m', '-p', f'127.0.0.1:{port}:8080', '-v', temp + ':/fixture']
        for key in ['TEST_DATABASE_URL', 'STATS_LOAD_FIXTURE', 'STATS_LOAD_MODE', 'STATS_LOAD_URL', 'GOMEMLIMIT']:
            cmd += ['-e', key]
        cmd += [image, '/community-tests', '-test.run=^TestCommunityStatsLoadServer$', '-test.v', '-test.timeout=30m']
        subprocess.run(cmd, env=env, stdout=subprocess.DEVNULL, check=True)
        try:
            manifest = Path(temp) / 'fixture.json'
            deadline = time.monotonic() + 300
            while not manifest.exists():
                if time.monotonic() > deadline:
                    raise RuntimeError('Fixture startup timed out')
                running = subprocess.run(docker + ['inspect', '-f', '{{.State.Running}}', name], capture_output=True, text=True)
                if running.returncode or running.stdout.strip() != 'true':
                    subprocess.run(docker + ['logs', name])
                    raise RuntimeError('Fixture exited before readiness')
                time.sleep(1)
            print('Ready: 2 CPUs / 512 MiB, 4 communities, 1,000 reader keys, 2,048 documents.', flush=True)
            generator = dict(os.environ, STATS_LOAD_MANIFEST=str(manifest), STATS_LOAD_SECONDS=str(args.seconds),
                             STATS_LOAD_ROUNDS=str(args.rounds), STATS_LOAD_REPORT=str(report))
            subprocess.run(['go', '-C', str(REPO / 'server'), 'test', '-run', '^TestCommunityStatsLoadClient$', '-v', '-count=1', '-timeout=30m'], env=generator, check=True)
            if args.summary_smoke:
                summary_smoke(manifest)
            print('Report:', report, flush=True)
        finally:
            (Path(temp) / 'stop').touch()
            try:
                subprocess.run(docker + ['wait', name], timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            except subprocess.TimeoutExpired:
                subprocess.run(docker + ['rm', '-f', name], stdout=subprocess.DEVNULL)
            resources = Path(temp) / 'resources.json'
            if resources.exists():
                report.with_suffix('.resources.json').write_text(resources.read_text())


if __name__ == '__main__':
    main()
