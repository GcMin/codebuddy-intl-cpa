#!/usr/bin/env python3
"""Read accounting/fingerprints only; never emit arbitrary JSON fields or prompts."""
import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('usage_log', type=Path)
parser.add_argument('--request-id', help='Plugin cache_trace.request_id of the miss')
parser.add_argument('--timestamp', help='UTC completion timestamp of the miss')
args = parser.parse_args()
rows = []
with args.usage_log.open() as file:
    for line in file:
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if row.get('cache_trace'):
            rows.append(row)
if not rows:
    parser.error('No cache_trace records yet; traces exist only after logging deployment.')
if args.request_id or args.timestamp:
    matches = [i for i, row in enumerate(rows)
               if (args.request_id and row['cache_trace']['request_id'] == args.request_id)
               or (args.timestamp and row.get('timestamp') == args.timestamp)]
    if not matches:
        parser.error('Target not found in traced records.')
    index = matches[-1]
else:
    # Latest low-hit, nontrivial prompt; else show the latest record.
    matches = [i for i, row in enumerate(rows)
               if row.get('input_tokens', 0) > 1000
               and row.get('cache_read_tokens', 0) < row['input_tokens'] * 0.1]
    index = matches[-1] if matches else len(rows) - 1
miss = rows[index]

def cid(row):
    attempts = row['cache_trace'].get('attempts', [])
    return attempts[-1].get('cid') if attempts else None

def summary(row):
    trace = row['cache_trace']
    return {**{key: row.get(key) for key in ('timestamp', 'auth_id', 'model', 'input_tokens',
                'cache_read_tokens', 'cache_write_tokens', 'credit', 'failed')},
            'request_id': trace['request_id'], 'cid': cid(row),
            'bytes': trace['upstream']['body_bytes'],
            'messages': trace['upstream']['message_count']}

def compare(a, b):
    out = {}
    for stage in ('inbound', 'upstream'):
        x, y = a['cache_trace'][stage], b['cache_trace'][stage]
        fields = ('body_hash', 'system_hash', 'first_user_hash', 'tools_hash',
                  'options_hash', 'messages_hash')
        ah, bh = x.get('message_hashes', []), y.get('message_hashes', [])
        differences = [i for i, (left, right) in enumerate(zip(ah, bh)) if left != right]
        out[stage] = {'changed_hashes': [field for field in fields if x.get(field) != y.get(field)],
                      'first_changed_message': differences[0] if differences else None,
                      'shared_message_hashes': len(ah) if not differences and len(ah) <= len(bh)
                          else (differences[0] if differences else len(bh)),
                      'message_count': [x['message_count'], y['message_count']],
                      'hashes_truncated': x.get('hashes_truncated', False) or y.get('hashes_truncated', False)}
    return out

print('TARGET', json.dumps(summary(miss)))
# Same CID across accounts and same account separately; do not mix concurrent sessions.
for scope, condition in (
    ('same CID', lambda row: cid(row) == cid(miss) and cid(miss) is not None),
    ('same CID + account', lambda row: cid(row) == cid(miss) and cid(miss) is not None
         and row.get('auth_id') == miss.get('auth_id')),
):
    previous = next((row for row in reversed(rows[:index]) if condition(row)), None)
    following = next((row for row in rows[index+1:] if condition(row)), None)
    for name, row in (('PREVIOUS', previous), ('FOLLOWING', following)):
        if row:
            print(scope, name, json.dumps(summary(row)))
            a, b = (row, miss) if name == 'PREVIOUS' else (miss, row)
            print('COMPARISON', json.dumps(compare(a, b)))
        else:
            print(scope, name, 'not available yet')
