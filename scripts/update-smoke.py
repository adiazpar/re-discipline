"""Disposable real old-to-new deployment, with concurrent scoped query/read checks.

Requires Docker and Go, and never updates the user's existing installation.
The old fixture uses the current service source stamped with an older version.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

REPO=Path(__file__).resolve().parents[1]


def run(command, **kw):
    return subprocess.run(list(map(str,command)),check=True,capture_output=True,timeout=240,**kw)


def health(origin):
    with urllib.request.urlopen(origin+'/healthz',timeout=5) as response:return json.load(response)


def snapshot(origin, token):
    def operation(action, community='', data=None):
        request=urllib.request.Request(origin+'/v1/operations',data=json.dumps({'action':action,'community':community,'data':data}).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
        with urllib.request.urlopen(request,timeout=10) as response:return json.load(response)
    result={}
    for community in operation('community.list'):
        cid=community['id']; inventory=operation('knowledge.inventory',cid,{})
        documents=[operation('knowledge.get',cid,{'id':item['id']}) for item in inventory['items']]
        connections=operation('access.list',cid,{})
        result[cid]={'documents':documents,'connections':sorted(connections,key=lambda x:x['id'])}
    return result


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context')
    parser.add_argument('--ssh',action='store_true',help='Exercise real SSH registration and remote software transfer')
    parser.add_argument('--report',type=Path)
    args=parser.parse_args()
    docker=['docker']+(['--context',args.context] if args.context else [])
    name='update-test-'+uuid.uuid4().hex[:8]
    with socket.socket() as listener:
        listener.bind(('127.0.0.1',0));port=listener.getsockname()[1]
    origin=f'http://127.0.0.1:{port}'
    service='https://'+name+'.invalid' if args.ssh else origin
    credential=Path.home()/'.re-discipline/servers/credentials'/(hashlib.sha256(service.encode()).hexdigest()+'.json')
    assert not credential.exists(),'test origin already registered'
    probe=None;compose=None;ssh_name=None;ssh_image=None
    with tempfile.TemporaryDirectory(prefix='re-discipline-update-test-') as temp:
        base=Path(temp).resolve();old=base/'old';state=base/'installation';signals=base/'signals';signals.mkdir()
        report={}
        try:
            for relative in ['scripts/community-local.py','server/Dockerfile']:
                path=old/relative;path.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(REPO/relative,path)
            target=json.loads((REPO/'bin/build.json').read_text())
            previous={**target,'version':'1.10.0','build':'0'*64}
            arch={'aarch64':'arm64','arm64':'arm64','x86_64':'amd64','amd64':'amd64'}[platform.machine().lower()]
            artifact=old/'bin/server'/('linux-'+arch)/'community';artifact.parent.mkdir(parents=True)
            flags=' '.join('-X github.com/adiazpar/re-discipline/retrieval/buildinfo.'+k+'='+v for k,v in [('Version',previous['version']),('ID',previous['build']),('Schema',previous['schema'])])
            print('Building the older-version fixture executable...',flush=True)
            run(['go','build','-trimpath','-buildvcs=false','-ldflags',flags,'-o',artifact,'./cmd/community'],cwd=REPO/'server',env=dict(os.environ,GOOS='linux',GOARCH=arch,CGO_ENABLED='0'))
            (old/'bin/build.json').write_text(json.dumps(previous))
            command=[sys.executable,old/'scripts/community-local.py','up','--name',name,'--port',str(port),'--state-dir',state]
            if args.context:command+=['--context',args.context]
            if args.ssh:command+=['--public-url',service]
            print('Starting isolated old-version server...',flush=True)
            run(command)
            compose=docker+['compose','--project-name','re-discipline-'+name,'--file',str(state/'compose.json')]
            assert health(origin)['software']==previous
            transport_env=dict(os.environ)
            if args.ssh:
                print('Preparing isolated SSH deployment controller...',flush=True)
                ssh_name=name+'-ssh';ssh_image='re-discipline-ssh-test:'+name
                ssh_build=base/'ssh';ssh_build.mkdir()
                (ssh_build/'Dockerfile').write_text('FROM docker:27-cli AS cli\nFROM alpine:3.21\nCOPY --from=cli /usr/local/bin/docker /usr/local/bin/docker\nCOPY --from=cli /usr/local/libexec/docker/cli-plugins /usr/local/libexec/docker/cli-plugins\nRUN apk add --no-cache python3 openssh-server socat && ssh-keygen -A && passwd -d root\nCMD ["sh","-c","mkdir -p /root/.ssh; cp /fixture-key.pub /root/.ssh/authorized_keys; chmod 700 /root/.ssh; chmod 600 /root/.ssh/authorized_keys; exec /usr/sbin/sshd -D -e"]\n')
                run(docker+['build','-t',ssh_image,ssh_build])
                key=base/'id_ed25519';run(['ssh-keygen','-t','ed25519','-N','','-f',key])
                remote_home=base/'remote-home';remote_creds=remote_home/'.re-discipline/servers/credentials';remote_creds.mkdir(parents=True)
                shutil.copy2(credential,remote_creds/credential.name);credential.unlink()
                config=json.loads((state/'installation.json').read_text());config['context']='default';config['owner_file']='/root/.re-discipline/servers/credentials/'+credential.name
                (state/'installation.json').write_text(json.dumps(config))
                run(docker+['run','-d','--name',ssh_name,'--network','re-discipline-'+name+'_frontend','-p','127.0.0.1::22','-v',str(base)+':'+str(base),'-v',str(remote_home/'.re-discipline')+':/root/.re-discipline','-v',str(key)+'.pub:/fixture-key.pub:ro','-v','/var/run/docker.sock:/var/run/docker.sock',ssh_image])
                run(docker+['exec','-d',ssh_name,'socat',f'TCP-LISTEN:{port},bind=127.0.0.1,reuseaddr,fork','TCP:app:8080'])
                ssh_port=run(docker+['port',ssh_name,'22/tcp']).stdout.decode().strip().split(':')[-1]
                host_key=run(docker+['exec',ssh_name,'cat','/etc/ssh/ssh_host_ed25519_key.pub']).stdout.decode().strip()
                known=base/'known_hosts';known.write_text('[127.0.0.1]:'+ssh_port+' '+host_key+'\n')
                config_path=base/'ssh_config';config_path.write_text(f'Host re-update-test\n HostName 127.0.0.1\n Port {ssh_port}\n User root\n IdentityFile "{key}"\n IdentitiesOnly yes\n UserKnownHostsFile "{known}"\n')
                wrapper=base/'tools';wrapper.mkdir();real_ssh=shutil.which('ssh')
                launcher=wrapper/'ssh';launcher.write_text('#!'+sys.executable+'\nimport os,sys\nos.execv('+repr(real_ssh)+', ['+repr(real_ssh)+',"-F",'+repr(str(config_path))+']+sys.argv[1:])\n');launcher.chmod(0o755)
                transport_env['PATH']=str(wrapper)+os.pathsep+transport_env['PATH']
                run([sys.executable,REPO/'scripts/community-update.py','register','--service',service,'--ssh','re-update-test','--remote-state',state],env=transport_env)
            owner_before=credential.read_bytes()
            with (base/'probe.log').open('wb') as log:
                env=dict(os.environ,TEST_UPDATE_ORIGIN=origin,TEST_UPDATE_STATE=str(signals),TEST_UPDATE_OWNER_FILE=str(credential),TEST_UPDATE_PUBLIC=service if args.ssh else origin)
                probe=subprocess.Popen(['go','test','-count=1','-timeout=150s','-run','^TestManagedUpdateLiveRetrieval$','-v','.'],cwd=REPO/'server',env=env,stdout=log,stderr=subprocess.STDOUT)
                deadline=time.monotonic()+100
                while not (signals/'ready').exists():
                    assert probe.poll() is None,(base/'probe.log').read_text()
                    assert time.monotonic()<deadline,'reader fixture setup timed out'
                    time.sleep(.2)
                print('32 reader keys across two communities are searching; triggering owner MCP management...',flush=True)
                binary=REPO/'bin'/('re-search.exe' if os.name=='nt' else 're-search')
                requests=[{'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2024-11-05'}},{'jsonrpc':'2.0','id':2,'method':'tools/call','params':{'name':'community','arguments':{'action':'server.update' if args.ssh else 'server.stats','service':service}}}]
                started=time.monotonic()
                output=run([binary,'serve','--mcp'],cwd=base,env=transport_env,input=''.join(json.dumps(r)+'\n' for r in requests).encode())
                response=json.loads(output.stdout.splitlines()[-1])['result']
                assert not response.get('isError'),response
                report['update_seconds']=round(time.monotonic()-started,3)
                assert health(origin)['software']==target,health(origin)
                assert credential.read_bytes()==owner_before,'operator credential changed'
                time.sleep(2)
                (signals/'stop').touch()
                assert probe.wait(timeout=40)==0,(base/'probe.log').read_text()
            report.update(json.loads((signals/'report.json').read_text()))
            report['owner_identity_preserved']=True
            report['transport']='ssh' if args.ssh else 'local'
            report['target']=target
            # A second management request must leave the current container intact.
            first_id=run(compose+['ps','-q','app']).stdout
            again=run([sys.executable,REPO/'scripts/community-update.py','ensure','--service',service,'--retry'],env=transport_env)
            assert json.loads(again.stdout)['status']=='current'
            assert run(compose+['ps','-q','app']).stdout==first_id
            report['unchanged_update_did_not_restart']=True
            if not args.ssh:
                print('Checking real rollback after an unhealthy replacement...',flush=True)
                bad=base/'bad';bad_binary=bad/'bin/server'/('linux-'+arch)/'community';bad_binary.parent.mkdir(parents=True)
                (bad/'server').mkdir();shutil.copy2(REPO/'server/Dockerfile',bad/'server/Dockerfile')
                bad_target={**target,'version':'1.12.0','build':'f'*64}
                (bad/'bin/build.json').write_text(json.dumps(bad_target))
                program=base/'bad.go';program.write_text('package main; import("os";"fmt");func main(){if len(os.Args)>1 && os.Args[1]=="version"{fmt.Print('+json.dumps(json.dumps(bad_target))+');return};os.Exit(42)}')
                run(['go','build','-o',bad_binary,program],env=dict(os.environ,GOOS='linux',GOARCH=arch,CGO_ENABLED='0'))
                spec=importlib.util.spec_from_file_location('deployment_fixture',REPO/'scripts/community-local.py');controller=importlib.util.module_from_spec(spec);spec.loader.exec_module(controller)
                controller.REPO=bad;controller.run=lambda command,capture=False,timeout=900:run(command)
                def short_health(url,mode,software=None,timeout=60):
                    deadline=time.monotonic()+3
                    while time.monotonic()<deadline:
                        try:
                            result=controller.health(url,mode)
                            if software is None or result.get('software')==software:return result
                        except (OSError,ValueError):pass
                        time.sleep(.1)
                    raise RuntimeError('fixture image did not become healthy')
                controller.wait_health=short_health
                existing=json.loads((state/'installation.json').read_text());owner=json.loads(credential.read_text())
                before_config=(state/'installation.json').read_bytes()
                before_knowledge=snapshot(origin,owner['token'])
                try:
                    with controller.installation_lock(state):
                        controller.deploy(state,state/'installation.json',existing,['docker','--context',existing['context']],compose,name,port,'ready',owner,str(credential),True,origin)
                    raise AssertionError('unhealthy replacement was accepted')
                except RuntimeError:pass
                assert health(origin)['software']==target
                assert (state/'installation.json').read_bytes()==before_config
                assert credential.read_bytes()==owner_before
                assert not (state/'update.json').exists()
                assert snapshot(origin,owner['token'])==before_knowledge
                report['unhealthy_image_rollback']=True
                report['rollback_preserved_knowledge_and_reader_grants']=True
            print(json.dumps(report,indent=2))
            if args.report:args.report.parent.mkdir(parents=True,exist_ok=True);args.report.write_text(json.dumps(report,indent=2)+'\n')
        except subprocess.CalledProcessError as error:
            if args.report:
                args.report.parent.mkdir(parents=True,exist_ok=True)
                details=error.stderr or b''
                if isinstance(details,bytes):details=details.decode('utf-8','replace')
                (args.report.parent/'failed-command.log').write_text(details[-20000:])
            raise
        finally:
            if args.report and not report.get('owner_identity_preserved'):
                args.report.parent.mkdir(parents=True,exist_ok=True)
                for suffix in ['.update.json','.update-log.json']:
                    log_path=credential.with_suffix(suffix)
                    if log_path.exists(): shutil.copy2(log_path,args.report.parent/('failed'+suffix))
                if (base/'probe.log').exists():shutil.copy2(base/'probe.log',args.report.parent/'failed-probe.log')
            (signals/'stop').touch()
            if probe and probe.poll() is None:
                probe.terminate()
                try:probe.wait(timeout=10)
                except subprocess.TimeoutExpired:probe.kill();probe.wait()
            if ssh_name:
                subprocess.run(docker+['rm','-f',ssh_name],capture_output=True)
            if ssh_image:
                subprocess.run(docker+['image','rm',ssh_image],capture_output=True)
            if (state/'compose.json').exists():
                cleanup=docker+['compose','--project-name','re-discipline-'+name,'--file',str(state/'compose.json'),'down','--volumes','--remove-orphans']
                run(cleanup)
            for path in credential.parent.glob(credential.stem+'*'):path.unlink()
            images=run(docker+['image','ls','--format','{{.Repository}}:{{.Tag}}','re-discipline-server-local']).stdout.decode().splitlines()
            for image in images:
                if image.startswith('re-discipline-server-local:'+name+'-'):run(docker+['image','rm',image])


if __name__=='__main__':main()
