#!/usr/bin/env python3
"""Compare accounting/fingerprints only; never emit arbitrary fields or prompts."""
import argparse
import json
from pathlib import Path
from cache_trace_analysis import read_rows, cid, summary, compare, classify, number

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('usage_log', type=Path)
parser.add_argument('--request-id')
parser.add_argument('--timestamp', help='UTC completion timestamp')
args = parser.parse_args()
rows, malformed = read_rows([args.usage_log])
if not rows:
    parser.error('No cache_trace records yet.')
if args.request_id or args.timestamp:
    matches = [i for i, r in enumerate(rows) if
               (args.request_id and r['cache_trace'].get('request_id') == args.request_id)
               or (args.timestamp and r.get('timestamp') == args.timestamp)]
    if not matches:
        parser.error('Target not found in traced records.')
else:
    # Detect prefix loss relative to the prior session, including partial hits.
    previous, matches = {}, []
    for i, r in enumerate(rows):
        key = (r.get('model'), cid(r))
        p = previous.get(key)
        if number(r, 'input_tokens') > 1000 and classify(p, r) in (
                'same_account_stable_prefix_loss', 'cross_account_prefix_loss',
                'stable_messages_changed_settings', 'prefix_changed'):
            matches.append(i)
        if cid(r) and not r.get('failed'):
            previous[key] = r
index = matches[-1] if matches else len(rows) - 1
miss = rows[index]
print('TARGET', json.dumps(summary(miss)))
for scope, condition in (
    ('same CID + model', lambda r: cid(miss) is not None and cid(r) == cid(miss)
        and r.get('model') == miss.get('model')),
    ('same CID + model + account', lambda r: cid(miss) is not None and cid(r) == cid(miss)
        and r.get('model') == miss.get('model') and r.get('auth_id') == miss.get('auth_id')),
):
    prev = next((r for r in reversed(rows[:index]) if condition(r) and not r.get('failed')), None)
    after = next((r for r in rows[index+1:] if condition(r) and not r.get('failed')), None)
    if prev:
        print(scope, 'CLASSIFICATION', classify(prev, miss))
    for name, row in (('PREVIOUS', prev), ('FOLLOWING', after)):
        if row:
            print(scope, name, json.dumps(summary(row)))
            print('COMPARISON', json.dumps(compare(row, miss) if name == 'PREVIOUS' else compare(miss, row)))
        else:
            print(scope, name, 'not available yet')
if malformed:
    print('Skipped malformed records:', malformed)
