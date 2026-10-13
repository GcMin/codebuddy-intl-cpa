import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('compare-cache-traces.py')
from cache_trace_analysis import classify, compare, read_rows, safe_id, studies

def fixture(rid='abcd', account='aa', model='gpt', inp=10000, read=9997, count=2, options='opts'):
    stage = dict(body_bytes=100, valid_json=True, body_hash=rid, message_count=count,
                 message_hashes=[str(i) for i in range(count)], prefix_hashes=[str(i) for i in range(count)],
                 system_hash='sys', first_user_hash='user', tools_hash='tools', options_hash=options,
                 option_hashes={'temperature': options})
    return dict(timestamp='2026-10-13T06:00:00Z', auth_id=account, model=model,
                input_tokens=inp, cache_read_tokens=read, credit=1, failed=False,
                cache_trace=dict(request_id=rid, started_at='2026-10-13T05:59:00Z',
                                 inbound=stage, upstream=stage, attempts=[dict(cid='aabb')]))

class CompareTraceTests(unittest.TestCase):
    def test_scope_filters_concurrent_sessions_and_never_prints_payload(self):
        rows = []
        for request_id, cid, hashes, credit in (
            ('one', 'cid-a', ['a', 'b'], 1),
            ('other', 'cid-b', ['z'], 2),
            ('miss', 'cid-a', ['a', 'x', 'y'], 50),
            ('after', 'cid-a', ['a', 'b', 'c'], 1),
        ):
            stage = dict(body_bytes=100, body_hash=request_id, message_count=len(hashes),
                         message_hashes=hashes, system_hash='system', tools_hash='tools')
            rows.append(dict(timestamp=request_id, auth_id='account', model='model',
                             input_tokens=2000, cache_read_tokens=0 if request_id=='miss' else 1900,
                             credit=credit, payload='DO-NOT-PRINT-SECRET',
                             cache_trace=dict(request_id=request_id, started_at=f'2026-10-13T06:00:0{len(rows)}Z', inbound=stage, upstream=stage,
                                              attempts=[dict(cid=cid)])))
        with tempfile.TemporaryDirectory() as d:
            p = Path(d)/'usage.jsonl'
            p.write_text('\n'.join(json.dumps(row) for row in rows))
            result = subprocess.run([sys.executable, str(SCRIPT), str(p), '--request-id', 'miss'],
                                    capture_output=True, text=True, check=True)
        self.assertIn('"first_changed_message": 1', result.stdout)
        self.assertIn('"request_id": "' + safe_id('one') + '"', result.stdout)
        self.assertIn('"request_id": "' + safe_id('after') + '"', result.stdout)
        self.assertNotIn('"request_id": "' + safe_id('other') + '"', result.stdout)
        self.assertNotIn('DO-NOT-PRINT-SECRET', result.stdout)

    def test_partial_prefix_loss_and_large_new_tail(self):
        prev = fixture()
        tail = fixture('eeee', inp=100000, read=9997, count=4)
        miss = fixture('ffff', inp=20000, read=2000, count=4)
        self.assertEqual(classify(prev, tail), 'retained_previous_prefix')
        self.assertEqual(classify(prev, miss), 'same_account_stable_prefix_loss')
        self.assertEqual(classify(None, miss), 'first_observed_session')
        miss['auth_id'] = 'bb'
        self.assertEqual(classify(prev, miss), 'cross_account_prefix_loss')
        miss['cache_trace']['upstream']['options_hash'] = 'changed'
        miss['cache_trace']['upstream']['option_hashes']['temperature'] = 'changed'
        self.assertEqual(classify(prev, miss), 'stable_messages_changed_settings')
        self.assertEqual(compare(prev, miss)['upstream']['changed_options'], ['temperature'])

    def test_recovery_lag_is_not_counted_as_independent_miss(self):
        prev = fixture('aa', inp=10000, read=9000)
        miss = fixture('bb', inp=12000, read=2000, count=3)
        recovery = fixture('cc', inp=13000, read=9997, count=4)
        normal = fixture('dd', inp=14000, read=12997, count=5)
        self.assertEqual([r['category'] for r in studies([prev, miss, recovery, normal])],
                         ['first_observed_session', 'same_account_stable_prefix_loss',
                          'prefix_lag_followup', 'retained_previous_prefix'])

    def test_capped_hashes_are_not_proof(self):
        prev, miss = fixture(), fixture('ffff', read=0, count=4)
        prev['cache_trace']['upstream']['hashes_truncated'] = True
        self.assertEqual(classify(prev, miss), 'incomplete_fingerprint_evidence')

    def test_multi_log_dedup_and_request_start_order(self):
        first, second = fixture('aa'), fixture('bb')
        first['cache_trace']['started_at'] = '2026-10-13T05:58:00Z'
        first['timestamp'] = '2026-10-13T06:01:00Z'
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'a.jsonl'
            q = Path(d) / 'b.jsonl'
            p.write_text(json.dumps(second) + '\n' + json.dumps(first) + '\n{broken\n')
            q.write_text(json.dumps(first))
            rows, malformed = read_rows([p, q])
        self.assertEqual([r['cache_trace']['request_id'] for r in rows], ['aa', 'bb'])
        self.assertEqual(malformed, 1)

    def test_model_scope_and_failed_neighbor(self):
        before, other, failure, miss = fixture('aa'), fixture('bb', model='other'), fixture('cc'), fixture('dd', read=0, count=4)
        failure['failed'] = True
        rows = [before, other, failure, miss]
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'usage.jsonl'
            p.write_text('\n'.join(json.dumps(r) for r in rows))
            result = subprocess.run([sys.executable, str(SCRIPT), str(p), '--request-id', 'dd'], capture_output=True, text=True, check=True)
        self.assertIn('same_account_stable_prefix_loss', result.stdout)
        self.assertNotIn('"request_id": "bb"', result.stdout)
        self.assertNotIn('"request_id": "cc"', result.stdout)

    def test_private_study_does_not_copy_arbitrary_fields(self):
        row = fixture('aa', account='Bearer SECRET-ACCOUNT')
        row['payload'] = 'SECRET-PROMPT'
        row['cache_trace']['inbound']['option_hashes']['SECRET-NAME'] = 'SECRET-VALUE'
        with tempfile.TemporaryDirectory() as d:
            path, output = Path(d)/'usage.jsonl', Path(d)/'report.json'
            path.write_text(json.dumps(row))
            subprocess.run([sys.executable, str(SCRIPT.with_name('study-cache-misses.py')), str(path), '--model', 'gpt', '--output', str(output)], capture_output=True, check=True)
            text = output.read_text()
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            for secret in ('SECRET-ACCOUNT', 'SECRET-PROMPT', 'SECRET-NAME', 'SECRET-VALUE'):
                self.assertNotIn(secret, text)

if __name__ == '__main__':
    unittest.main()
