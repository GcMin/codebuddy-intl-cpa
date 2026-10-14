# Content-free cache tracing

Cache tracing is **off by default**. Turn it on either way:

- the Usage card's **Hash tracing** toggle (`GET`/`POST /cachetrace/config`),
  which applies immediately to new requests and is persisted as `cache_trace`
  in `workbuddy-state.json`; an explicit choice wins over the environment
  default and survives plugin restarts; or
- `WB_CACHE_TRACE=1` in the plugin's environment, the deployment-level default
  used when no panel choice was made yet.

When enabled, the plugin records `cache_trace` in each new
executor accounting line in `.codebuddy_data/request-usage.jsonl`. Existing rows
remain readable and have no trace. The panel flip applies to new requests only;
rows already written keep whatever they had. This is observability only: the plugin does not
change CID, model routing, request content, or billing. It does not retry a cache
miss.

Why it is opt-in: the fingerprints dominate the accounting log — measured at
99.4% of the JSONL line, roughly 976 message/prefix hashes per request — and cost
a SHA-256 pass over the whole prompt plus two log lines per request. A usage query
has to parse all of it to display five fields, which took 330ms versus 90ms with
the log at 52MB. Enable it while investigating a specific miss, then turn it off.

## Recorded fields

- A generated plugin `request_id`, start time, and a host request ID when the host
  supplies one in executor metadata. Arbitrary IDs are hashed before recording.
- `inbound` and `upstream` body fingerprints, before and after plugin rewriting:
  body byte count/hash, system content hash, first-user content hash, tool/function
  schema hash, non-message option hash, message count, and ordered message hashes.
- Cumulative `prefix_hashes[i]` covers messages 0 through i with length framing.
  Compare these and `message_hashes` to locate the first changed message. The
  arrays stop at 512 messages; the complete `messages_hash` still covers the tail.
- Per-option digests for a fixed list of known settings (model, temperature,
  reasoning, token limits, tool choice, response format, etc.). Values and arbitrary
  option names are not logged. Old records have only the combined options hash.
- Per-attempt runtime auth ID, actual outgoing `X-Conversation-ID` and
  `X-Request-ID`, start time and HTTP status. Failover attempts share the inbound
  trace but retain their own outgoing account/request identity.
- The existing accounting row supplies the final account, model, cache read/write,
  token counts, credit and failure status. Log metadata prints the trace ID at
  attempt start and result, joining the outgoing request to its accounting row.

Hashes use SHA-256 truncated to 128 bits and remain deterministic across restart.
No prompt text, tool names/arguments, header values other than safe IDs, credential
values, or raw upstream errors enter the trace. Hashes are still sensitive
metadata: someone who knows a candidate prompt can compare its digest. Keep the
local accounting file and management API private; do not publish trace logs.

## Panel view

In **Usage CodeBuddy**, expand **Cache hash** below the cache read/write counts.
It shows the hit ratio, outgoing CID, content-free hashes and failover attempt
accounts/status. The previous loaded request with the same CID/model is compared
for account changes, changed system/tools/options and leading message equality.
Clear account filters to see cross-account neighbors. Records without a trace
(the switch was off when they were written) show a muted `hash tracing off`
label after the read/write counts — flip **Hash tracing** in the same card to
collect fingerprints for new requests; comparisons are limited to the loaded
records and 512-message hash coverage. A low-hit badge is an observation, not a
root-cause verdict.

## Compare a miss

Run the following where you can read the usage log:

```sh
python3 scripts/compare-cache-traces.py /path/to/request-usage.jsonl \
  --request-id '<plugin trace ID>'
# Or select by the UTC completion timestamp:
python3 scripts/compare-cache-traces.py /path/to/request-usage.jsonl \
  --timestamp '2026-10-13T06:00:00Z'
```

Without a selector, the script picks the latest prefix-loss candidate over 1K
input tokens (or the latest trace). It compares successful neighbors with the same
CID **and model**, then the same account, ordered by request start. It skips failed
neighbors and unrelated sessions. Partial hits count as possible loss if read
falls below the previously cached input; a large new tail alone does not.

To collect a private study across active and rotated logs:

```sh
python3 scripts/study-cache-misses.py /path/to/request-usage*.jsonl \
  --model gpt-6.1-sol --since '2026-10-13T05:54:00Z' \
  --output /private/path/cache-study.json
```

The output uses mode 0600 and copies accounting/fingerprint comparisons only. It
separates first-observed sessions, retained previous prefix, same-account stable
prefix loss, cross-account changes, changed settings, capped evidence and lag
follow-ups after a miss. Classification describes evidence, not the upstream root
cause. The heuristic uses a 2% tolerance against the previous input minus three
protocol tokens; do not infer prices or cache TTL from a category count.

Read changes as evidence, not a verdict:

- Changed `system_hash`/`first_user_hash` and CID indicate a session prefix change.
- Stable CID but changed tool/schema or early message hashes indicates a request
  prefix change not covered by the CID algorithm.
- Stable early message hashes with an appended tail support append-only history.
- Identical body/CID/model/account followed by low cache read supports an upstream
  eviction/shard explanation, but does not identify its cause.
- A replacement account can miss cache despite an identical CID. Compare account
  transitions separately from transient misses on the same account.

Tracing cannot reconstruct a miss that happened before deployment. It adds bounded
hash metadata to accounting, so log growth increases. JSONL rotation stays at
50MB and no longer overwrites a previous rotation in the same month.
