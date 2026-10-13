"""Accounting/fingerprint analysis only. Never export arbitrary JSON fields."""
import hashlib
import json
from datetime import datetime
from pathlib import Path

STABLE_FIELDS = ('system_hash', 'first_user_hash', 'tools_hash', 'options_hash')
OPTIONS = ('model', 'temperature', 'top_p', 'max_tokens', 'max_completion_tokens',
           'reasoning', 'reasoning_effort', 'tool_choice', 'parallel_tool_calls',
           'stream', 'stream_options', 'seed', 'response_format', 'stop', 'logprobs',
           'top_logprobs', 'presence_penalty', 'frequency_penalty', 'n', 'service_tier')


def read_rows(paths):
    rows, seen, malformed = [], set(), 0
    for path in paths:
        with Path(path).open() as file:
            for line in file:
                try:
                    row = json.loads(line)
                except (ValueError, TypeError):
                    malformed += 1
                    continue
                if not isinstance(row, dict) or not isinstance(row.get('cache_trace'), dict):
                    continue
                tr = row['cache_trace']
                if not all(isinstance(tr.get(k), dict) for k in ('inbound', 'upstream')):
                    malformed += 1
                    continue
                identity = tr.get('request_id')
                if identity and identity in seen:
                    continue
                if identity:
                    seen.add(identity)
                rows.append(row)
    # Request start, not completion order: slow simultaneous calls can finish reversed.
    rows.sort(key=lambda r: (r['cache_trace'].get('started_at') or r.get('timestamp', ''),
                             r.get('timestamp', '')))
    return rows, malformed


def cid(row):
    attempts = row['cache_trace'].get('attempts') or []
    return attempts[-1].get('cid') if attempts else None


def safe_id(value):
    if value is None:
        return None
    text = str(value)
    if len(text) <= 64 and text and all(c in '0123456789abcdefABCDEF-' for c in text):
        return text
    return 'hash:' + hashlib.sha256(text.encode()).hexdigest()[:32]


def number(row, key):
    value = row.get(key, 0)
    return value if isinstance(value, (float, int)) and not isinstance(value, bool) else 0


def summary(row):
    tr = row['cache_trace']
    inp, read = number(row, 'input_tokens'), number(row, 'cache_read_tokens')
    return {**{key: row.get(key) for key in ('timestamp', 'model', 'failed')},
            **{key: number(row, key) for key in ('input_tokens', 'cache_read_tokens',
                'cache_write_tokens', 'output_tokens', 'reasoning_tokens', 'credit', 'latency_ms')},
            'auth_id': safe_id(row.get('auth_id')), 'request_id': safe_id(tr.get('request_id')),
            'account_changed': None,
            'started_at': tr.get('started_at'), 'cid': safe_id(cid(row)),
            'hit_ratio': read / inp if inp else None,
            'bytes': tr['upstream'].get('body_bytes'),
            'messages': tr['upstream'].get('message_count'),
            'attempts': [{'auth_id': safe_id(a.get('auth_id')), 'status': a.get('status'),
                          'cid': safe_id(a.get('cid'))} for a in tr.get('attempts', [])]}


def compare(a, b):
    out = {}
    for stage in ('inbound', 'upstream'):
        x, y = a['cache_trace'][stage], b['cache_trace'][stage]
        ah, bh = x.get('message_hashes', []), y.get('message_hashes', [])
        ap, bp = x.get('prefix_hashes', []), y.get('prefix_hashes', [])
        first = next((i for i, (u, v) in enumerate(zip(ah, bh)) if u != v), None)
        prefix_first = next((i for i, (u, v) in enumerate(zip(ap, bp)) if u != v), None)
        complete = (x.get('valid_json', False) and y.get('valid_json', False)
                    and not x.get('hashes_truncated', False) and not y.get('hashes_truncated', False)
                    and len(ap) == x.get('message_count') and len(bp) == y.get('message_count')
                    and len(ap) > 0)
        options_a, options_b = x.get('option_hashes'), y.get('option_hashes')
        out[stage] = {
            'changed_hashes': [k for k in ('body_hash', *STABLE_FIELDS, 'messages_hash') if x.get(k) != y.get(k)],
            'changed_options': [k for k in OPTIONS if options_a.get(k) != options_b.get(k)]
                if isinstance(options_a, dict) and isinstance(options_b, dict) else None,
            'first_changed_message': first, 'first_changed_prefix': prefix_first,
            'shared_message_hashes': first if first is not None else min(len(ah), len(bh)),
            'shared_prefix_hashes': prefix_first if prefix_first is not None else min(len(ap), len(bp)),
            'message_count': [x.get('message_count'), y.get('message_count')],
            'hashes_truncated': x.get('hashes_truncated', False) or y.get('hashes_truncated', False),
            'complete_prefix_evidence': bool(complete),
            'append_only': bool(complete and prefix_first is None and len(ap) <= len(bp)),
        }
    return out


def classify(previous, row):
    if row.get('failed'):
        return 'failed_request'
    if previous is None:
        return 'first_observed_session'  # NOT proof of a cache anomaly.
    read, prev_input = number(row, 'cache_read_tokens'), number(previous, 'input_tokens')
    if prev_input < 1000:
        return 'small_prefix'
    if read >= (prev_input - 3) * .98:
        return 'retained_previous_prefix'  # low overall ratio can just be a large new tail.
    comparison = compare(previous, row)
    if not all(c['complete_prefix_evidence'] for c in comparison.values()):
        return 'incomplete_fingerprint_evidence'
    if not all(c['append_only'] for c in comparison.values()):
        return 'prefix_changed'
    if any(any(k in c['changed_hashes'] for k in STABLE_FIELDS) for c in comparison.values()):
        return 'stable_messages_changed_settings'
    if previous.get('auth_id') != row.get('auth_id'):
        return 'cross_account_prefix_loss'
    return 'same_account_stable_prefix_loss'  # supports upstream hypothesis, not root cause.


def studies(rows):
    previous, prior_category, history = {}, {}, {}
    result = []
    for row in rows:
        key = (row.get('model'), cid(row))
        prev = previous.get(key) if cid(row) else None
        category = classify(prev, row)
        # A request after a miss may still read the older prefix. Keep that
        # measured lag visible, but do not count it as another independent miss.
        if (category == 'same_account_stable_prefix_loss'
                and prior_category.get(key) in ('same_account_stable_prefix_loss', 'prefix_lag_followup')
                and number(row, 'cache_read_tokens') >= number(prev, 'cache_read_tokens')):
            category = 'prefix_lag_followup'
        item = {'category': category, 'request': summary(row),
                'previous': summary(prev) if prev else None,
                'comparison': compare(prev, row) if prev else None}
        item['request']['account_changed'] = prev.get('auth_id') != row.get('auth_id') if prev else None
        past = history.get(key, [])
        # Stronger settings attribution is available only on newly instrumented
        # records. Old combined hashes remain useful but cannot name a setting.
        item['changed_settings'] = item['comparison']['upstream']['changed_options'] if prev else None
        # A fallback cache read can identify an older accounted prefix. This is
        # descriptive evidence; matching token totals does not prove cache IDs.
        older = next((p for p in reversed(past) if p.get('auth_id') == row.get('auth_id')
                      and number(p, 'input_tokens') - 3 == number(row, 'cache_read_tokens')), None)
        item['read_matches_prior_input'] = summary(older) if older else None
        try:
            start = datetime.fromisoformat(row['cache_trace']['started_at'].replace('Z', '+00:00'))
            previous_end = datetime.fromisoformat(prev['timestamp'].replace('Z', '+00:00')) if prev else None
            item['previous_finished_before_start'] = previous_end <= start if previous_end else None
            item['previous_end_to_start_seconds'] = (start - previous_end).total_seconds() if previous_end else None
        except (KeyError, TypeError, ValueError):
            item['previous_finished_before_start'] = None
            item['previous_end_to_start_seconds'] = None
        result.append(item)
        if cid(row) and not row.get('failed'):
            previous[key] = row
            prior_category[key] = category
            history.setdefault(key, []).append(row)
    return result
