"""Update policy, rollback, transport boundaries, and concurrency tests."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch, Mock


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module


host = load('community-local')
update = load('community-update')


class Updates(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'bin').mkdir()
        self.old = dict(version='1.10.0', build='a'*64, schema='c'*64, protocol=1)
        self.new = dict(version='1.11.0', build='b'*64, schema='c'*64, protocol=1)
        (self.root / 'bin/build.json').write_text(json.dumps(self.new))
        self.state = self.root / 'state'; self.state.mkdir()
        self.old_compose = json.dumps({'services': {'app': {'image': 'old-image'}}})
        (self.state / 'compose.json').write_text(self.old_compose)
        self.existing = dict(context='test', name='test', port=7346, mode='ready', image='old-image', software=self.old)
        self.commands = []

    def run_command(self, command, **kwargs):
        self.commands.append(command)
        return subprocess.CompletedProcess(command, 0, stdout=json.dumps(self.new), stderr='')

    def deploy(self):
        with patch.object(host, 'REPO', self.root), patch.object(host, 'run', self.run_command), patch.object(host, 'health', return_value={'software':self.old}), patch.object(host.urllib.request, 'build_opener') as opener:
            opener.return_value.open.return_value.__enter__.return_value.status = 200
            return host.deploy(self.state, self.state/'installation.json', self.existing, ['docker','--context','test'], ['docker','compose','--file',str(self.state/'compose.json')], 'test',7346,'ready',{'owner_id':'owner','token':'x'*43},'owner-file',True,'http://127.0.0.1:7346')

    def test_policy(self):
        for old, expected in [
            (self.old,'upgrade'), (self.new,'current'), ({**self.old,'version':'1.12.0'},'newer_server'),
            ({**self.old,'version':'2.0.0'},'newer_server'), ({**self.old,'schema':'different'},'maintenance_required'),
            ({},'maintenance_required'), ({**self.old,'protocol':2},'maintenance_required'), ({**self.old,'version':self.new['version']},'same_version_different_build'),
        ]:
            self.assertEqual(host.update_policy(old,self.new),expected)
        self.assertEqual(host.update_policy(self.old,{**self.new,'version':'2.0.0'}),'maintenance_required')

    def test_automatic_update_never_migrates_or_recreates_database(self):
        with patch.object(host,'wait_health',return_value={'software':self.new,'ok':True}):
            self.assertEqual(self.deploy()['status'],'updated')
        self.assertFalse(any('migrate' in c or 'database' in c for c in self.commands))
        self.assertTrue(any('version' in c for c in self.commands))
        saved=json.loads((self.state/'installation.json').read_text())
        self.assertEqual(saved['software'],self.new)
        self.assertEqual(saved['previous_image'],'old-image')
        self.assertFalse((self.state/'update.json').exists())

    def test_failed_health_restores_old_image_without_database_rollback(self):
        with patch.object(host,'wait_health',side_effect=[RuntimeError('candidate unhealthy'),{'software':self.old}]):
            with self.assertRaises(RuntimeError):self.deploy()
        self.assertEqual((self.state/'compose.json').read_text(),self.old_compose)
        self.assertFalse((self.state/'installation.json').exists())
        self.assertFalse((self.state/'update.json').exists())
        self.assertFalse(any('migrate' in c for c in self.commands))

    def test_stage_failure_never_replaces_running_service(self):
        with patch.object(host,'wait_health') as ready:
            with patch.object(host,'REPO',self.root), patch.object(host,'run',side_effect=RuntimeError('out of disk')), patch.object(host,'health',return_value={'software':self.old}), patch.object(host.urllib.request,'build_opener') as opener:
                opener.return_value.open.return_value.__enter__.return_value.status=200
                with self.assertRaises(RuntimeError):
                    host.deploy(self.state,self.state/'installation.json',self.existing,['docker','--context','test'],['docker','compose','--file',str(self.state/'compose.json')],'test',7346,'ready',{'owner_id':'owner','token':'x'*43},'owner-file',True,'http://127.0.0.1:7346')
            ready.assert_not_called()
        self.assertEqual((self.state/'compose.json').read_text(),self.old_compose)

    def test_journal_recovers_interrupted_switch_before_retry(self):
        (self.state/'update.json').write_text(json.dumps({'compose':self.old_compose}))
        with patch.object(host,'wait_health',return_value={'software':self.new,'ok':True}) as health:
            self.deploy()
            self.assertEqual(health.call_count,2)
        self.assertEqual(self.commands[0][-4:],['up','--detach','--no-deps','app'])

    def test_installation_lock_excludes_concurrent_updater(self):
        with host.installation_lock(self.state):
            with self.assertRaises(host.DeploymentBusy):
                with host.installation_lock(self.state):pass
        with host.installation_lock(self.state):pass

    def test_ssh_disallows_options_and_does_not_disable_host_checks(self):
        for name in ['-oProxyCommand=anything','host; touch /tmp/no','host\nanything']:
            with self.assertRaises(ValueError): update.ssh_command(name,'print(values)',{})
        command=update.ssh_command('operator@my-server','print(values)',{'state':"/private/quote ' $ path"})
        self.assertIn('BatchMode=yes',command)
        self.assertIn('StrictHostKeyChecking=yes',command)
        self.assertEqual(command[-2],'operator@my-server')

    def test_ssh_extract_rejects_traversal_and_symlinks(self):
        import io, tarfile
        (self.state/'installation.json').write_text(json.dumps({'public_url':'https://kb.example'}))
        for name, kind in [('../escape',tarfile.REGTYPE),('bin/build.json',tarfile.SYMTYPE)]:
            data=io.BytesIO()
            with tarfile.open(fileobj=data,mode='w') as archive:
                entry=tarfile.TarInfo(name);entry.type=kind;entry.linkname='/tmp/no';archive.addfile(entry)
            values={'state':str(self.state),'service':'https://kb.example'}
            code='import json; values='+repr(values)+';'+update.REMOTE_UPDATE
            result=subprocess.run([sys.executable,'-c',code],input=data.getvalue(),capture_output=True)
            self.assertNotEqual(result.returncode,0)
        self.assertFalse((self.root/'escape').exists())


if __name__=='__main__': unittest.main()
