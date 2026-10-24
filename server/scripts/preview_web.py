"""Local-only UI fixture server. Build web first; never use this as the service."""
import argparse
import json
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

COMMUNITY = '00000000-0000-4000-8000-000000000001'
USER = '00000000-0000-4000-8000-000000000002'
LIBRARY = dict(id=COMMUNITY, name='DOOM 2016 Research', slug='doom-2016-research', visibility='private', role='owner', policy_version=1, policy=dict(scope='Reverse engineering DOOM (2016): engine behavior, SnapMap systems, file formats, and evidence-backed discoveries. Findings should apply to the game itself and be reproducible by another investigator.', exclusions=['Local development environments and personal infrastructure', 'Unverified assumptions without stated applicability'], mode='trusted'))
MARKDOWN = '''# How the game applies time scaling

The simulation uses a **scaled frame interval**. Rendering and input sampling remain independent of this interval.

## Applicability

Observed in the retail PC build. Follow the call from the frame update to the simulation step:

```cpp
simulationDelta = frameDelta * timeScale;
```

| System | Effect |
| --- | --- |
| Simulation | Scaled frame interval |
| Input | Unscaled sampling |

> Repeat the observation with a fixed frame limit before comparing results.

## Next investigation

Compare multiplayer timing with the single-player update path.
'''
DOC = dict(source_path='docs/engine/time-scaling.md', markdown=MARKDOWN, kind='behavior', grade='confirmed', build='Retail PC', evidence=[dict(label='Frame-update trace', excerpt='The frame interval is multiplied before entering the simulation step.')])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port', type=int, default=4180)
    parser.add_argument('--role', choices=['owner', 'reader', 'contributor', 'public'], default='owner')
    parser.add_argument('--status', choices=['active','disputed','refuted','superseded'], default='active')
    args = parser.parse_args()
    library = {**LIBRARY, 'role': args.role if args.role != 'public' else '', 'visibility': 'public' if args.role == 'public' else 'private'}
    invites = []
    submissions = [dict(id='submission-1', author=USER, state='needs_review', digest='fixture', policy_version=1, document=DOC, checks=[dict(message='Document is portable and within the community scope.')]), dict(id='submission-2', author=USER, state='accepted', digest='fixture-2', policy_version=1, document={**DOC, 'markdown': '# SnapMap event routing\n\nA previously accepted finding.'}, checks=[])]

    class Handler(SimpleHTTPRequestHandler):
        def __init__(self, *a, **kw):
            super().__init__(*a, directory=str(Path(__file__).resolve().parents[1] / 'web/dist'), **kw)

        def log_message(self, *_):
            pass

        def send_json(self, value, status=200):
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(value).encode())

        def do_GET(self):
            if self.path == '/v1/config':
                return self.send_json(dict(github_auth=True))
            if self.path == '/v1/auth/session':
                return self.send_json(dict(session=None if args.role == 'public' else dict(user=dict(id=USER, login='researcher', email='fixture@example.test'))))
            if not self.path.startswith('/assets/') and self.path != '/theme.js':
                self.path = '/index.html'
            super().do_GET()

        def do_POST(self):
            if self.path != '/v1/operations':
                return self.send_json({})
            req = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            action, data = req['action'], req.get('data', {})
            if action == 'community.list': return self.send_json([library])
            if action == 'community.get': return self.send_json(library)
            if action == 'community.update':
                library.update(data)
                library['policy_version'] += 1
                return self.send_json(library)
            if action == 'usage': return self.send_json(dict(findings=12543, submission_bytes=32277992, limits=dict(bytes_per_community=104857600, communities_per_owner=5, members_per_community=50, submissions_per_user_day=100)))
            if action == 'member.list': return self.send_json([dict(user_id=USER, login='researcher', role='owner'), dict(user_id='collaborator', login='fellow-investigator', role='contributor')])
            if action == 'invite.list': return self.send_json(invites)
            if action == 'invite.create':
                invites.append(dict(id='fixture-invite', role=data['role'], expires_at='2099-01-01T00:00:00Z', revoked=False))
                return self.send_json(dict(id='fixture-invite', url=f'http://127.0.0.1:{args.port}/join#fixture-invite'))
            if action == 'invite.revoke':
                invites.clear()
                return self.send_json({})
            if action == 'submission.list':
                items = [s for s in submissions if not data.get('state') or s['state'] == data['state']]
                if data.get('group'):
                    items = [s for s in items if (s['state'] in ['accepted', 'rejected']) == (data['group'] == 'completed')]
                    return self.send_json(dict(items=items, next=''))
                return self.send_json(items)
            if action == 'submission.packet': return self.send_json(dict(token='fixture-token', submission=submissions[0], comparisons=[], limited=False, plan=dict(mode='create', effect='Publish a separate claim, retaining this contributor authorship.')))
            if action == 'health.list': return self.send_json(dict(items=[], next='', progress=dict(total=12543, checked=12540, pending=3, incomplete=0, open=0)))
            if action in ['finding.relations', 'finding.contributions']: return self.send_json([])
            if action == 'submission.review':
                submissions[0]['state'] = 'accepted' if data['decision'] == 'accept' else 'changes_requested'
                return self.send_json(submissions[0])
            if action == 'submission.create':
                if 'items' in data: return self.send_json(dict(items=[dict(idempotency_key=i['idempotency_key'], submission=dict(id='fixture-' + str(n), state='queued')) for n, i in enumerate(data['items'])]))
                return self.send_json(submissions[0])
            if action == 'query': return self.send_json(dict(hits=[] if data['query'] == 'no-match' else [dict(finding_id='finding-1', revision='r1', title='How the game applies time scaling', snippet='The simulation uses a scaled frame interval. Rendering and input sampling remain independent of this interval.', kind='behavior', grade='confirmed'), dict(finding_id='finding-2', revision='r2', title='SnapMap event routing and execution order', snippet='Event delivery follows the graph connection order. Each receiving node processes the message before advancing the queue.', kind='system', grade='supported')]))
            if action in ['finding.get', 'finding.history']: return self.send_json([dict(finding_id=data['id'], revision='r1', author='researcher', document=DOC, deleted=False, sequence=14, status=args.status, reason='A later controlled measurement disproved the original assumption.' if args.status != 'active' else '', assessment_evidence=[dict(label='Controlled measurement', excerpt='The isolated test produced a different result under the same conditions.')])])
            if action == 'finding.candidates': return self.send_json(dict(candidates=[]))
            if action == 'token.list': return self.send_json([dict(id='fixture-device', label='Research workstation', expires_at='2099-01-01T00:00:00Z', revoked=False)])
            if action in ['token.revoke', 'member.remove', 'member.set']: return self.send_json({})
            self.send_json(dict(error='Unsupported preview operation'), 400)

    print(f'Fixture UI: http://127.0.0.1:{args.port}/communities/{COMMUNITY}', flush=True)
    ThreadingHTTPServer(('127.0.0.1', args.port), Handler).serve_forever()


if __name__ == '__main__':
    main()
