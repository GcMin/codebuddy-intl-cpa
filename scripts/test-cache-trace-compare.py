import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('compare-cache-traces.py')

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
                             cache_trace=dict(request_id=request_id, inbound=stage, upstream=stage,
                                              attempts=[dict(cid=cid)])))
        with tempfile.TemporaryDirectory() as d:
            p = Path(d)/'usage.jsonl'
            p.write_text('\n'.join(json.dumps(row) for row in rows))
            result = subprocess.run([sys.executable, str(SCRIPT), str(p), '--request-id', 'miss'],
                                    capture_output=True, text=True, check=True)
        self.assertIn('"first_changed_message": 1', result.stdout)
        self.assertIn('"request_id": "one"', result.stdout)
        self.assertIn('"request_id": "after"', result.stdout)
        self.assertNotIn('"request_id": "other"', result.stdout)
        self.assertNotIn('DO-NOT-PRINT-SECRET', result.stdout)

if __name__ == '__main__':
    unittest.main()
