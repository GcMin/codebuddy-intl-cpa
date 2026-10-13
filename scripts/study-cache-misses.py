#!/usr/bin/env python3
"""Build a private metadata-only cache study across active/rotated usage logs."""
import argparse
import collections
import json
from pathlib import Path
from cache_trace_analysis import read_rows, studies, number

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('logs', type=Path, nargs='+')
p.add_argument('--model', default='gpt-6.1-sol')
p.add_argument('--since', help='UTC completion timestamp, inclusive')
p.add_argument('--min-input', type=int, default=10000)
p.add_argument('--output', type=Path, help='Private report; raw usage rows are NEVER copied')
a = p.parse_args()
rows, malformed = read_rows(a.logs)
records = studies(rows)  # retain older neighbors even with --since filter
selected = [r for r in records if r['request']['model'] == a.model
            and (not a.since or (r['request']['timestamp'] or '') >= a.since)
            and number(r['request'], 'input_tokens') >= a.min_input]
counts = collections.Counter(r['category'] for r in selected)
interesting = [r for r in selected if r['category'] not in (
    'retained_previous_prefix', 'small_prefix', 'failed_request')]
report = {'model': a.model, 'traced_records': len(rows), 'selected_records': len(selected),
          'malformed_records': malformed, 'categories': dict(counts),
          'caveats': ['First observed CID is not proof of a cache anomaly.',
                      'Changed options and cross-account transitions are confounders.',
                      'Stable wire prefixes support an upstream cause, not a specific eviction/shard root cause.',
                      'No upstream replay; comparison coverage stops at 512 messages.'],
          'observations': interesting}
text = json.dumps(report, indent=2)
if a.output:
    # Keep digests/identities private; no permissive default umask or partial file.
    import os
    import tempfile
    a.output.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=a.output.parent, prefix='.cache-study-')
    try:
        with os.fdopen(fd, 'w') as f:
            f.write(text + '\n')
        os.replace(tmp, a.output)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    print(json.dumps({'output': str(a.output), 'selected': len(selected), 'categories': dict(counts)}))
else:
    print(text)
