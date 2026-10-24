"""Load-test the remote search service using disposable corpora and 1,000 reader connections.

Requires an existing local installation from community-local.py. Never targets
its corpus: the test creates and drops a separate PostgreSQL database. Secrets
are passed by environment and never written to the report.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

REPO = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', default='orbstack')
    parser.add_argument('--state-dir', type=Path, default=Path.home() / '.re-discipline/servers/local')
    parser.add_argument('--users', type=int, default=1000)
    parser.add_argument('--documents', type=int, default=2000, help='Documents per community')
    parser.add_argument('--archive-mib', type=int, default=256, help='Synthetic evidence MiB for concurrent archive test')
    parser.add_argument('--requests', type=int, default=2000, help='Requests per concurrency phase')
    parser.add_argument('--report', type=Path)
    parser.add_argument('--cpus', type=int, default=2)
    parser.add_argument('--memory', type=int, default=512, help='Container memory in MiB')
    parser.add_argument('--workers', type=int, help='Override automatic workers for tuning experiments')
    args = parser.parse_args()
    if not (4 <= args.users <= 10000 and 1 <= args.documents <= 20000 and 100 <= args.requests <= 20000):
        parser.error('Use 4–10000 users, 1–20000 documents, and 100–20000 requests')
    if not (1 <= args.archive_mib <= 4096):
        parser.error('Use 1–4096 archive MiB')
    if not (1 <= args.cpus <= 16 and 128 <= args.memory <= 8192):
        parser.error('Use 1–16 CPUs and 128–8192 MiB')
    state = args.state_dir.expanduser().resolve()
    installation = json.loads((state / 'installation.json').read_text())
    if installation['context'] != args.context:
        parser.error('Context must match the local installation')
    docker = ['docker', '--context', args.context]
    info = json.loads(subprocess.check_output(docker + ['context', 'inspect', args.context]))[0]
    if not info['Endpoints']['docker']['Host'].startswith(('unix://', 'npipe://')):
        parser.error('Load tests require a local Docker context')
    subprocess.run(docker + ['build', '--target', 'tests', '--tag', 're-discipline-search-tests', '--file', str(REPO / 'server/Dockerfile'), str(REPO)], check=True)
    env = dict(os.environ)
    env.update(TEST_DATABASE_URL=(state / '.env.admin').read_text().strip().split('=', 1)[1], RUN_SEARCH_LOAD='1', LOAD_USERS=str(args.users), LOAD_DOCUMENTS=str(args.documents), LOAD_REQUESTS=str(args.requests), LOAD_ARCHIVE_MIB=str(args.archive_mib), GOMEMLIMIT=str(args.memory * 3 // 4) + 'MiB')
    if args.workers:
        env['MAX_ACTIVE_SEARCHES'] = str(args.workers)
    command = docker + ['run', '--rm', '--memory', str(args.memory) + 'm', '--cpus', str(args.cpus), '--network', 're-discipline-' + installation['name'] + '_database']
    for name in ['TEST_DATABASE_URL', 'RUN_SEARCH_LOAD', 'LOAD_USERS', 'LOAD_DOCUMENTS', 'LOAD_REQUESTS', 'LOAD_ARCHIVE_MIB', 'GOMEMLIMIT'] + (['MAX_ACTIVE_SEARCHES'] if args.workers else []):
        command += ['--env', name]
    command += ['re-discipline-search-tests', '/community-tests', '-test.run=^TestRemoteSearchLoad$', '-test.v', '-test.timeout=15m']
    process = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    report = None
    for line in process.stdout:
        if line.startswith('LOAD_REPORT='):
            report = json.loads(line.split('=', 1)[1])
        else:
            print(line, end='', flush=True)
    result = process.wait()
    if report:
        report['container_limits'] = {'cpus': args.cpus, 'memory_mib': args.memory, 'go_memory_target_mib': args.memory * 3 // 4}
        report['test_exit_code'] = result
        path = (args.report or state / 'search-load-report.json').resolve()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(report, indent=2) + '\n')
        print('Load report:', path)
    if result or not report:
        raise SystemExit(result or 1)


if __name__ == '__main__':
    main()
