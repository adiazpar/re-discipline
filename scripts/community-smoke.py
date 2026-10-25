"""Test owner uploads and scoped readers through both shipped MCP adapters.

Requires an explicitly selected local-owner server. Creates and deletes disposable
Doom and Godzilla fixture communities and temporary local projects.
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


def finding(fid, marker, qualifier="initial"):
    return f"---\nid: {fid}\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [fixtureclock]\n---\n# Synthetic clock\n\n## Knowledge\n{marker} {qualifier} fixtureclock. This is synthetic test knowledge.\n\n## Evidence\nPRIVATE_SMOKE_RESEARCH\n"


def delete_fixture(owner, cid, archive_path=None):
    preview = owner.community("community.delete", community=cid)
    ticket = preview["ticket"]
    owner.community("community.delete", community=cid, data={"step":"lock", "ticket":ticket, "archive": archive_path is not None})
    if archive_path:
        result = owner.community("community.archive", community=cid, path=str(archive_path), data={"ticket":ticket})
        while result.get("resume_required"):
            result = owner.community("community.archive", community=cid, path=str(archive_path), data={"ticket":ticket})
        assert result["verified"] and result["summary"]["counts"]["document"] == 1, result
    result = owner.community("community.delete", community=cid, data={"step":"confirm", "ticket":ticket, "confirm":True})
    assert result["deleted"], result


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service",required=True)
    parser.add_argument("--report",type=Path)
    args=parser.parse_args()
    assert args.service.startswith("http://127.0.0.1:"), "Requires a local test server"
    report={"synthetic":True,"providers":[],"passed":False}
    with tempfile.TemporaryDirectory(prefix="re-discipline-upload-smoke-") as temp:
        base=Path(temp);fixtures=[]
        try:
            for provider,name in [("claude","Doom 2016"),("codex","Godzilla PS4")]:
                root=base/provider;(root/".re-discipline/docs").mkdir(parents=True)
                owner=Adapter(provider,root,args.service)
                assert owner.community("server.ping")["mode"]=="ready"
                l=owner.community("community.create",data={"name":name+" upload test","slug":"upload-test-"+str(uuid.uuid4()),"policy":{"scope":"Disposable synthetic integration corpus only."}})
                fixtures.append((owner,l,root));assert l["visibility"]=="private" and "mode" not in l["policy"]
                owner.community("connect",community=l["id"],alias="fixture")
                fid=str(uuid.uuid4());marker=provider+"uniquemarker"
                source=root/".re-discipline/docs/clock.md";source.write_text(finding(fid,marker))
                uploaded=owner.community("knowledge.upload",alias="fixture");assert uploaded["complete"] and uploaded["uploaded"]==1,uploaded
                unchanged=owner.community("knowledge.upload",alias="fixture");assert unchanged["uploaded"]==0 and unchanged["unchanged"]==1,unchanged
                source.write_text(finding(fid,marker,"corrected"))
                updated=owner.community("knowledge.upload",alias="fixture");assert updated["complete"] and updated["uploaded"]==1,updated
                doc=owner.community("knowledge.get",alias="fixture",data={"id":fid})
                assert "corrected" in doc["markdown"] and "PRIVATE_SMOKE_RESEARCH" not in doc["markdown"]
                assert "PRIVATE_SMOKE_RESEARCH" in source.read_text()
                deadline=time.monotonic()+30
                while True:
                    result=owner.community("query",alias="fixture",data={"query":marker})
                    if result["hits"] and result["hits"][0]["fingerprint"]==doc["fingerprint"]:break
                    assert time.monotonic()<deadline,result
                    time.sleep(.2)
                assert not result.get("warnings"),result
                combined=owner.tool("query",{"root":str(root),"query":marker,"sources":"both","assistance":"off"})
                assert len(combined["hits"])==1,combined
                merged=combined["hits"][0]
                assert merged["source"]=="local" and merged["finding_id"]==fid and merged["fingerprint"]==doc["fingerprint"],merged
                assert len(merged["locations"])==2 and not merged.get("content_differs"),merged
                assert {v["source"] for v in merged["locations"]}=={"local","fixture"},merged
                # A local correction replaces the stale remote candidate before ranking.
                source.write_text(finding(fid,marker,"local edit"))
                divergent=owner.tool("query",{"root":str(root),"query":marker,"sources":"both","assistance":"off"})
                assert len(divergent["hits"])==1,divergent
                assert divergent["hits"][0]["source"]=="local" and divergent["hits"][0]["resolution"]=="local_changed",divergent
                assert divergent["hits"][0]["content_differs"] and len(divergent["hits"][0]["other_copies"])==1,divergent
                source.write_text(finding(fid,marker,"corrected"))
                # A second operator workspace changes only the community copy.
                writer_root=base/(provider+"-writer");(writer_root/".re-discipline/docs").mkdir(parents=True)
                writer=Adapter(provider,writer_root,args.service)
                writer.community("connect",community=l["id"],alias="fixture")
                (writer_root/".re-discipline/docs/clock.md").write_text(finding(fid,marker,"remote edit"))
                remote_upload=writer.community("knowledge.upload",alias="fixture")
                assert remote_upload["complete"] and remote_upload["uploaded"]==1,remote_upload
                deadline=time.monotonic()+30
                while True:
                    result=owner.tool("query",{"root":str(root),"query":marker,"sources":"both","assistance":"off"})
                    if result["hits"] and result["hits"][0].get("resolution")=="remote_changed":break
                    assert time.monotonic()<deadline,result
                    time.sleep(.2)
                assert len(result["hits"])==1 and result["hits"][0]["source"]=="fixture",result
                assert result["hits"][0].get("created_at") and result["hits"][0].get("knowledge_updated_at"),result
                source.write_text(finding(fid,marker,"local divergence"))
                result=owner.tool("query",{"root":str(root),"query":marker,"sources":"both","assistance":"off"})
                assert len(result["hits"])==1 and result["hits"][0]["source"]=="local" and result["hits"][0]["resolution"]=="local_preferred_diverged",result
                writer.community("disconnect",alias="fixture")
                reader_root=base/(provider+"-reader");(reader_root/".re-discipline/docs").mkdir(parents=True)
                reader=Adapter(provider,reader_root,args.service,reader=True)
                invitation=owner.community("access.create",alias="fixture")
                try:
                    reader.community("connect",community=l["id"],alias="fixture",data={"invitation":invitation["invitation"]})
                    hits=reader.tool("query",{"root":str(reader_root),"query":marker,"sources":"external"})["hits"]
                    assert len(hits)==1 and hits[0]["community_id"]==l["id"]
                    denied=reader.community("knowledge.upload",alias="fixture");assert not denied["complete"] and "operator" in denied["issues"][0]
                    for action in ["knowledge.delete","community.create","access.create"]:
                        reader.tool("community",{"root":str(reader_root),"action":action,"alias":"fixture","data":{}},error="read-only")
                    reader.tool("community",{"root":str(reader_root),"action":"publish.prepare","alias":"fixture"},error="retired")
                finally:
                    if reader.community("connections")["connections"]:reader.community("disconnect",alias="fixture")
                report["providers"].append(provider)
                print(provider+": upload, skip, replacement, evidence stripping, both-mode reconciliation, timestamps, scoped read and reader-write denial passed")
            for owner,l,root in fixtures:
                other=next(v for v in fixtures if v[1]["id"]!=l["id"])
                assert not owner.community("query",alias="fixture",data={"query":other[0].provider+"uniquemarker"})["hits"]
                owner.tool("community",{"root":str(root),"action":"query","alias":"fixture","community":other[1]["id"],"data":{"query":"fixtureclock"}},error="conflicts")
            for owner,l,root in fixtures:
                delete_fixture(owner,l["id"],base/(owner.provider+".zst"))
                assert (root/".re-discipline/docs/clock.md").exists()
            fixtures.clear()
            report["passed"]=True
        finally:
            for owner,l,root in fixtures:
                owner.community("community.delete",community=l["id"],data={"step":"cancel"})
                delete_fixture(owner,l["id"])
    if args.report:args.report.write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps(report,indent=2))

if __name__=="__main__":main()
