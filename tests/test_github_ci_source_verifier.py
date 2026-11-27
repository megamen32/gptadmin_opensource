"""Local recovery contract: real NoticePlace lifecycle/SDK, isolated SQLite + fake GitHub I/O."""
import io
import json
from pathlib import Path
import sys
import unittest
import urllib.error
from unittest.mock import patch
import test_github_ci_autofix as fixtures
ci = fixtures.ci

sys.path.insert(0, '/opt/noticeplace')
try:
    from notification_center.core import NotificationCenter, ValidationError
    from notification_center.consumer_lifecycle import SourceVerifier
except ImportError as error:
    raise unittest.SkipTest("installed NoticePlace SDK/lifecycle required for this integration test") from error


class SourceRecoveryTests(unittest.TestCase):
    state = fixtures.WatcherTests.state
    rest_run = fixtures.WatcherTests.rest_run
    api = fixtures.WatcherTests.api

    def setUp(self):
        fixtures.WatcherTests.setUp(self)
        self.center = NotificationCenter(Path(self.temp.name) / 'center.db', {
            'source': {'project': 'gptadmin-ci-autofix', 'max_severity': 'emergency',
                       'principal_id': 'probe', 'incident_role': 'verifier',
                       'verification_sources': ['gptadmin-ci-autofix']},
            'executor': {'project': 'gptadmin-ci-autofix', 'max_severity': 'emergency',
                         'principal_id': 'worker', 'incident_role': 'executor'}})
        self.event = dict(schema='notify.event.v1', kind='incident', severity='notice',
            project='gptadmin-ci-autofix', recipient='fixture', title='CI', dedup_key=self.item['key'],
            event_type='health.degraded', source_id='gptadmin-ci-autofix', host_id='admin-node-100',
            signal_type='github_ci', correlation_id=self.item['key'])
        self.incident = self.center.create_event('source', 'open', self.event)['incident_id']
        self.center.record_work_finished('executor', self.incident, 'finish',
            dict(attempt_id='one', session_id='preserved-session', harness='codex'))
        self.db.execute('UPDATE events SET incident_id=?,session_id=?', (self.incident, 'preserved-session'))
        self.db.commit()
        ci.change(self.db, self.state(), 'verified_green', 'cached previous green')
        token=Path(self.temp.name)/'token';token.write_text('source')
        self.cfg={'notice_token_file':str(token)}
        self.posts=[];self.legacy=[];self.github=[];self.mode='success'
        self.stack = __import__('contextlib').ExitStack()
        self.stack.enter_context(patch.object(ci,'get_json',lambda *a: self.center.get_incident(self.incident),create=True))
        self.stack.enter_context(patch.object(ci,'post_json',self.legacy_post))
        self.stack.enter_context(patch.object(ci,'gh',self.green))
        self.stack.enter_context(patch.object(SourceVerifier,'_http',lambda _,*a: self.transport(*a)))

    def tearDown(self):
        self.stack.close();self.center._connection.close();fixtures.WatcherTests.tearDown(self)

    def legacy_post(self,url,body,headers):
        self.legacy.append(url)
        raise ValueError('legacy closure cannot resolve work_finished')

    def green(self,path):
        self.github.append(path)
        if path.endswith('actions/workflows?per_page=100'):
            return {'total_count':1,'workflows':[{'id':3,'state':'active'}]}
        if '/actions/workflows/3/runs?' in path:
            return {'total_count':1,'workflow_runs':[self.rest_run(11,'failure' if self.mode=='red' else 'success')]}
        return self.api(path)

    def transport(self,method,path,body=None,key=None):
        if method=='GET':
            if path.startswith('/v1/incidents/'):
                return self.center.get_incident(self.incident)
            requests=self.center.due_source_checks('source','gptadmin-ci-autofix','gptadmin-ci-autofix')
            return {'requests':requests}
        self.posts.append((key,dict(body)))
        if self.mode=='unknown':raise TimeoutError('lost response')
        if self.mode=='stale' and len(self.posts)==1:
            # Real expired-request rejection rotates current request/generation.
            self.center._connection.execute('UPDATE incident_lifecycle SET verification_deadline=1 WHERE incident_id=?',(self.incident,))
            self.center._connection.commit()
        try:
            result=self.center.record_source_check('source',self.incident,key,body)
        except ValidationError as e:
            raise urllib.error.HTTPError('http://fixture',400,'Bad Request',{},io.BytesIO(json.dumps({'error':str(e)}).encode())) from e
        if self.mode=='lost_after_accept':raise TimeoutError('accepted but response lost')
        return result

    def test_work_finished_fresh_predicate_current_request_resolves(self):
        before=self.center.get_incident(self.incident)['lifecycle']
        ci.notices(self.db,self.cfg)
        self.assertEqual('resolved',self.center.get_incident(self.incident)['state'])
        self.assertEqual(1,self.db.execute('SELECT sent FROM notices').fetchone()[0])
        self.assertEqual([],self.legacy)
        self.assertTrue(self.github)
        self.assertEqual(before['request_id'],self.posts[0][1]['request_id'])
        self.assertEqual(before['generation'],self.posts[0][1]['generation'])
        self.assertEqual('preserved-session',self.state()['session_id'])
        ci.notices(self.db,self.cfg)
        self.assertEqual(1,len(self.posts))

    def test_expired_rejection_readback_new_request_without_old_replay(self):
        self.mode='stale';ci.notices(self.db,self.cfg)
        self.assertNotEqual('resolved',self.center.get_incident(self.incident)['state'])
        ci.notices(self.db,self.cfg)
        self.assertEqual('resolved',self.center.get_incident(self.incident)['state'])
        self.assertEqual(2,len(self.posts))
        self.assertNotEqual(self.posts[0][0],self.posts[1][0])
        self.assertNotEqual(self.posts[0][1]['request_id'],self.posts[1][1]['request_id'])

    def test_unknown_never_reposts_or_reuses_cached_green(self):
        self.mode='unknown';ci.notices(self.db,self.cfg);count=len(self.github)
        ci.notices(self.db,self.cfg)
        self.assertEqual(1,len(self.posts));self.assertEqual(count,len(self.github))
        self.assertEqual(0,self.db.execute('SELECT sent FROM notices').fetchone()[0])
        self.assertEqual([],self.legacy)

    def test_lost_accepted_result_reconciles_without_replay(self):
        self.mode='lost_after_accept';ci.notices(self.db,self.cfg)
        ci.notices(self.db,self.cfg)
        self.assertEqual(1,len(self.posts))
        self.assertEqual(1,self.db.execute('SELECT sent FROM notices').fetchone()[0])

    def test_new_red_ci_does_not_close_from_cached_outbox(self):
        self.mode='red';ci.notices(self.db,self.cfg)
        self.assertNotEqual('resolved',self.center.get_incident(self.incident)['state'])
        self.assertEqual('unknown',self.posts[0][1]['observed_state'])
        self.assertEqual(0,self.db.execute('SELECT sent FROM notices').fetchone()[0])

    def test_operator_stop_preserves_outbox_and_session(self):
        self.db.execute("UPDATE events SET state='operator_stopped'");self.db.commit()
        ci.notices(self.db,self.cfg)
        self.assertEqual([],self.posts);self.assertEqual([],self.github);self.assertEqual([],self.legacy)
        self.assertEqual('preserved-session',self.state()['session_id'])
        self.assertEqual(0,self.db.execute('SELECT sent FROM notices').fetchone()[0])

    def test_unknown_with_rotated_request_stays_fenced(self):
        self.mode='unknown';ci.notices(self.db,self.cfg)
        self.center.create_event('source','new-generation',self.event)
        ci.notices(self.db,self.cfg)
        self.assertEqual(1,len(self.posts));self.assertEqual([],self.legacy)
        self.assertEqual(0,self.db.execute('SELECT sent FROM notices').fetchone()[0])

    def test_unknown_missing_lifecycle_never_uses_legacy(self):
        self.mode='unknown';ci.notices(self.db,self.cfg)
        with patch.object(ci,'get_json',return_value={'lifecycle':{}}):
            ci.notices(self.db,self.cfg)
        self.assertEqual(1,len(self.posts));self.assertEqual([],self.legacy)

    def test_foreign_correlation_never_probed(self):
        snapshot=self.center.get_incident(self.incident)
        snapshot['lifecycle']['group']['correlation_id']='other-episode'
        with patch.object(ci,'get_json',return_value=snapshot):
            ci.notices(self.db,self.cfg)
        self.assertEqual([],self.posts);self.assertEqual([],self.github);self.assertEqual([],self.legacy)
