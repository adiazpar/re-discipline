"""Create two labeled fixture communities and test them through both MCP adapters.

Requires an explicitly selected local-owner server. Leaves Doom 2016 and Godzilla
PS4 synthetic corpora on that server; uses disposable local project directories.
No real project files, model calls, browser login, or printed credentials.
"""
import argparse
import json
import os
from pathlib import Path
import tempfile
import time
import uuid

from smoke import REPO, engine_command, rpc, run


class Adapter:
    def __init__(self, provider, root, service, reader=False):
        manifest = json.loads((REPO / f".{provider}-plugin/plugin.json").read_text())
        config = json.loads((REPO / manifest["mcpServers"]).read_text())["mcpServers"]["re-search"]
        command = config["command"].replace("$" + "{CLAUDE_PLUGIN_ROOT}", str(REPO))
        self.cwd = REPO / config["cwd"] if "cwd" in config else root
        if not Path(command).is_absolute():
            command = str(self.cwd / command)
        self.command = [engine_command(command), *config["args"]]
        self.root, self.service, self.provider = root, service, provider
        self.env = dict(os.environ)
        if reader:
            self.env.update(RE_DISCIPLINE_SERVICE=service, RE_DISCIPLINE_TOKEN="")

    def tool(self, name, arguments, error=None):
        requests = [rpc(1, "initialize", {"protocolVersion": "2024-11-05"}),
                    rpc(2, "tools/call", {"name": name, "arguments": arguments})]
        result = run(self.command, cwd=self.cwd, env=self.env, input="".join(json.dumps(r) + "\n" for r in requests))
        response = json.loads(result.stdout.splitlines()[-1])["result"]
        text = response["content"][0]["text"]
        if error:
            assert response.get("isError") and error in text, response
            return
        assert not response.get("isError"), response
        return json.loads(text)

    def community(self, action, **kwargs):
        return self.tool("community", {"root": str(self.root), "service": self.service,
                                       "action": action, **kwargs})


def check_deletion(provider, base, service, survivors):
    root = base / (provider + "-delete-owner")
    reader_root = base / (provider + "-delete-reader")
    for project in [root, reader_root]:
        (project / ".re-discipline/docs").mkdir(parents=True)
    local = root / ".re-discipline/docs/local.md"
    local.write_text("Local research must survive server deletion.\n")
    owner, reader = Adapter(provider, root, service), Adapter(provider, reader_root, service, reader=True)
    created = owner.community("community.create", data={"name": "Disposable deletion fixture",
        "slug": "delete-fixture-" + str(uuid.uuid4()), "policy": {"scope": "Synthetic deletion test only."}})
    cid = created["id"]
    try:
        owner.community("connect", community=cid, alias="delete-fixture")
        grant = owner.community("access.create", alias="delete-fixture", data={"label": "Deletion test reader", "hours": 1})
        reader.community("connect", community=cid, alias="delete-fixture", data={"invitation": grant["invitation"]})
        document = {"source_path": "docs/delete.md", "kind": "fact", "grade": "direct", "build": "Deletion test fixture",
            "markdown": "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Deletion fixture\nThe deletefixturemarker exists only for this test.\n"}
        sub = owner.community("submission.create", alias="delete-fixture", data={"idempotency_key": str(uuid.uuid4()), "document": document})
        deadline = time.monotonic() + 30
        while sub["state"] == "queued" and time.monotonic() < deadline:
            time.sleep(.25)
            sub = owner.community("submission.get", alias="delete-fixture", data={"id": sub["id"]})
        assert sub["state"] == "accepted", sub
        assert len(reader.community("query", alias="delete-fixture", data={"query": "deletefixturemarker"})["hits"]) == 1
        reader.tool("community", {"root": str(reader_root), "action": "community.delete", "alias": "delete-fixture"}, error="operator credentials")
        preview = owner.community("community.delete", alias="delete-fixture")
        assert not preview["deleted"] and preview["phase"] == "preview"
        assert owner.community("connections")["connections"]
        ticket = preview["ticket"]
        owner.community("community.delete", alias="delete-fixture", data={"step": "lock", "ticket": ticket, "archive": True})
        reader.tool("community", {"root": str(reader_root), "action": "query", "alias": "delete-fixture", "data": {"query": "deletefixturemarker"}}, error="pending deletion")
        archived = owner.community("community.archive", alias="delete-fixture", path=str(base / (provider + "-knowledge.zst")), data={"ticket": ticket})
        assert archived["verified"] and archived["confirmation_required"] and not archived["deleted"], archived
        assert archived["summary"]["counts"]["revision"] == 1
        result = owner.community("community.delete", alias="delete-fixture", data={"step": "confirm", "ticket": ticket, "confirm": True})
        assert result["deleted"] and not result["cleanup_pending"] and result["local_disconnected"], result
        assert not owner.community("connections")["connections"]
        assert local.read_text() == "Local research must survive server deletion.\n"
        reader.tool("community", {"root": str(reader_root), "action": "query", "alias": "delete-fixture", "data": {"query": "deletefixturemarker"}}, error="resource unavailable")
        repeat = owner.community("community.delete", community=cid, data={"step":"confirm", "ticket":ticket, "confirm":True})
        assert repeat["already_absent"] and not repeat["cleanup_pending"], repeat
        remaining = {c["id"] for c in owner.community("community.list")}
        assert cid not in remaining and all(c["id"] in remaining for c in survivors)
        print(provider + ": preview, freeze, knowledge archive, confirmation, deletion and other-community preservation passed")
        return {"provider": provider, "deleted": True, "cleanup_pending": False, "local_disconnected": True, "archive_verified": True}
    finally:
        # Only this UUID was created by this test; never select existing corpora.
        if any(c["id"] == cid for c in owner.community("community.list")):
            owner.community("community.delete", community=cid, data={"step":"cancel"})
            clean = owner.community("community.delete", community=cid)
            owner.community("community.delete", community=cid, data={"step":"lock", "ticket":clean["ticket"], "archive":False})
            owner.community("community.delete", community=cid, data={"step":"confirm", "ticket":clean["ticket"], "confirm":True})
        if reader.community("connections")["connections"]:
            reader.community("disconnect", alias="delete-fixture")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", required=True)
    parser.add_argument("--report", type=Path, help="Optional JSON result file (contains no secrets)")
    args = parser.parse_args()
    assert args.service.startswith("http://127.0.0.1:"), "This fixture test requires a local server"
    report = {"service": args.service, "synthetic": True, "communities": [], "providers": []}
    with tempfile.TemporaryDirectory(prefix="re-discipline-two-communities-") as temp:
        base = Path(temp)
        roots = [base / "doom-project", base / "godzilla-project"]
        for root in roots:
            (root / ".re-discipline/docs").mkdir(parents=True)
        adapters = [Adapter(p, root, args.service) for p, root in zip(["claude", "codex"], roots)]
        health = adapters[0].community("server.ping")
        assert health["mode"] == "ready" and health["community_enabled"], health
        existing = {c["slug"]: c for c in adapters[0].community("community.list")}
        for adapter, name, slug, marker in zip(adapters, ["Doom 2016", "Godzilla PS4"],
                                              ["doom-2016", "godzilla-ps4"], ["doomfixturemarker", "godzillafixturemarker"]):
            scope = name + " reverse engineering. Synthetic integration fixtures only; not verified game findings."
            community = existing.get(slug)
            if community:
                assert community["policy"]["scope"] == scope, "Refusing to seed an existing research corpus"
            else:
                community = adapter.community("community.create", data={"name": name, "slug": slug, "policy": {"scope": scope}})
            assert community["visibility"] == "private" and community["policy"]["mode"] == "trusted"
            adapter.community("connect", community=community["id"], alias=slug, retrieval="remote")
            # Both communities deliberately use the same source namespace, path,
            # identifier and retry key, but different text/build applicability.
            document = {"source_namespace": "integration-fixtures", "source_path": "docs/clock.md",
                        "kind": "fact", "grade": "direct", "build": name + " synthetic fixture", "evidence": [],
                        "markdown": "---\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [fixtureclock]\n---\n# " + name +
                                    " synthetic clock fixture\nThis integration test contains " + marker +
                                    ". It tests retrieval isolation and makes no claim about the actual game.\n"}
            key = str(uuid.uuid5(uuid.NAMESPACE_URL, "re-discipline/two-community-fixture/clock-v1"))
            submission = adapter.community("submission.create", community=community["id"],
                                           data={"idempotency_key": key, "document": document})
            replay = adapter.community("submission.create", community=community["id"],
                                       data={"idempotency_key": key, "document": document})
            assert replay["id"] == submission["id"], "Retry duplicated a submission"
            deadline = time.monotonic() + 30
            while submission["state"] == "queued" and time.monotonic() < deadline:
                time.sleep(.25)
                submission = adapter.community("submission.get", community=community["id"], data={"id": submission["id"]})
            assert submission["state"] == "accepted", submission
            report["communities"].append({"name": name, "alias": slug, "id": community["id"],
                                          "finding_id": submission["finding_id"], "marker": marker})
        assert report["communities"][0]["id"] != report["communities"][1]["id"]
        assert report["communities"][0]["finding_id"] != report["communities"][1]["finding_id"]
        # Exercise the skill's real local-file preparation/queue/flush route too.
        # Queue both independent projects, then verify each flush respects its alias.
        for root in roots:
            (root / ".re-discipline/docs/published.md").write_text(
                "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n"
                "# Synthetic publication fixture\n"
                "The publicationfixture marker tests plugin transfer only; it is not a game finding.\n")
        drafts = []
        for adapter, community in zip(adapters, report["communities"]):
            prepared = adapter.community("publish.prepare", alias=community["alias"],
                                         build=community["name"] + " synthetic fixture",
                                         data={"paths": ["docs/published.md"]})
            assert not prepared["uploaded"] and prepared["items"][0]["state"] == "new", prepared
            draft_id = prepared["items"][0]["draft_id"]
            preview = adapter.community("publish.preview", draft_id=draft_id)
            assert "publicationfixture" in preview["document"]["markdown"], preview
            adapter.community("publish.queue", data={"draft_ids": [draft_id]})
            drafts.append(draft_id)
        for i, (adapter, community) in enumerate(zip(adapters, report["communities"])):
            flushed = adapter.community("publish.flush", alias=community["alias"])
            assert flushed["submitted"] == 1 and not flushed["issues"], flushed
            if i == 0:
                assert adapters[1].community("publish.preview", draft_id=drafts[1])["state"] == "queued"
            receipt = adapter.community("publish.preview", draft_id=drafts[i])
            deadline = time.monotonic() + 30
            while True:
                sub = adapter.community("submission.get", alias=community["alias"], data={"id": receipt["submission_id"]})
                if sub["state"] != "queued" or time.monotonic() >= deadline:
                    break
                time.sleep(.25)
            if sub["state"] == "needs_review":
                # Similar fixture wording can require a deliberate identity
                # decision. Use the real revision-bound review gate.
                packet = adapter.community("submission.packet", alias=community["alias"], data={"id": sub["id"]})
                assert packet["submission"]["document"]["source_path"] == "docs/published.md", packet
                assert all(c["reference"]["id"] == community["finding_id"] for c in packet["comparisons"]), packet
                sub = adapter.community("submission.review", alias=community["alias"], data={
                    "id": sub["id"], "digest": sub["digest"], "policy_version": sub["policy_version"],
                    "resolution_version": sub["resolution_version"], "packet_token": packet["token"],
                    "decision": "accept", "plan": {"mode": "create"},
                    "rationale": "This synthetic publication-transfer marker is distinct from the synthetic clock marker; retain both test findings."})
            assert sub["state"] == "accepted", sub
            assert len(adapter.community("query", alias=community["alias"], data={"query": "publicationfixture"})["hits"]) == 1
            report["communities"][i]["publication_finding_id"] = sub["finding_id"]
        assert report["communities"][0]["publication_finding_id"] != report["communities"][1]["publication_finding_id"]
        for provider in ["claude", "codex"]:
            for i, community in enumerate(report["communities"]):
                adapter = Adapter(provider, roots[i], args.service)
                other = report["communities"][1-i]
                result = adapter.community("query", alias=community["alias"], data={"query": "fixtureclock"})
                assert len(result["hits"]) == 1 and result["hits"][0]["community_id"] == community["id"], result
                assert not adapter.community("query", alias=community["alias"], data={"query": other["marker"]})["hits"]
                assert not adapter.community("finding.get", alias=community["alias"], data={"id": other["finding_id"]})
                selected = adapter.tool("query", {"root": str(roots[i]), "sources": "external", "query": "fixtureclock"})
                assert len(selected["hits"]) == 1 and selected["hits"][0]["community_id"] == community["id"], selected
                adapter.tool("community", {"root": str(roots[i]), "service": args.service, "action": "connect",
                    "community": other["id"], "alias": other["alias"]}, error="already connects")
                assert not (roots[i] / ".re-discipline/community/cache").exists()
            report["providers"].append(provider)
            print(provider + ": separate projects, scoped key retrieval, publication, and second-link rejection passed")
        doom, godzilla = report["communities"]
        for operator in adapters:
            reader_root = base / (operator.provider + "-reader")
            (reader_root / ".re-discipline/docs").mkdir(parents=True)
            label = "adapter-smoke-" + str(uuid.uuid4())
            grant = operator.community("access.create", community=doom["id"], data={"label": label, "hours": 1})
            reader = Adapter(operator.provider, reader_root, args.service, reader=True)
            try:
                reader.community("connect", community=doom["id"], alias="doom", data={"invitation": grant["invitation"]})
                # Retry must reuse the project key, even though the invitation is consumed.
                reader.community("connect", community=doom["id"], alias="doom", data={"invitation": grant["invitation"]})
                own = reader.community("query", alias="doom", data={"query": "fixtureclock"})
                assert len(own["hits"]) == 1 and own["hits"][0]["community_id"] == doom["id"]
                reader.tool("community", {"root": str(reader_root), "service": args.service, "action": "query",
                    "community": godzilla["id"], "data": {"query": "fixtureclock"}}, error="conflicts")
                for action in ["submission.create", "community.create", "access.create"]:
                    reader.tool("community", {"root": str(reader_root), "action": action,
                        "alias": "doom", "data": {}}, error="read-only")
                grants = operator.community("access.list", community=doom["id"])
                for item in grants:
                    if item["label"] == label:
                        operator.community("access.revoke", community=doom["id"], data={"id": item["id"]})
                reader.tool("community", {"root": str(reader_root), "action": "query", "alias": "doom",
                    "data": {"query": "fixtureclock"}}, error="revoked")
            finally:
                if reader.community("connections")["connections"]:
                    reader.community("disconnect", alias="doom")
            print(operator.provider + ": OS key storage, invitation reuse, write denial, revocation and key deletion passed")
        for adapter, community in zip(adapters, report["communities"]):
            connections = adapter.community("connections")
            assert len(connections["connections"]) == 1 and connections["retrieval"] == "remote", connections
            disconnected = adapter.community("disconnect", alias=community["alias"])
            assert disconnected["server_removed"], disconnected
        report["reader_permissions_passed"] = True
        report["deletion"] = [check_deletion(provider, base, args.service, report["communities"])
                              for provider in ["claude", "codex"]]
    report["passed"] = True
    if args.report:
        args.report.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
