# CPAMC WorkBuddy account names

`cpamc-v1.24.2-workbuddy-labels.patch` fixes the host management panel's account
identity renderer. Apply it to upstream CPAMC **v1.24.2** (commit
`4530da271ba2e89810d4dccebc57f3091afa590a`), then rebuild its single-file artifact.
Do not rename auth files or maintain a nickname mapping.

The WorkBuddy plugin provides `label` from the stored nickname during auth
load/import. CPAMC previously ignored that field and displayed the UUID filename.
This patch displays the WorkBuddy label, removes the trailing realm suffix
(`[Global]` or `[CN]`), and keeps the physical filename as a secondary line.
Search and alphabetical sorting use the same display name. Other providers keep
their email/project identity rules. The raw `account` field remains excluded from
display and search because it can contain an API key.

The plugin's Model Manager has a separate fix: `/models/config` now provides
`account_labels` keyed by stable account IDs. Dropdown text uses the nickname;
option values and fetch/test requests continue to use account IDs. Duplicate
nicknames cannot change routing identity. Newly imported accounts work without
manual edits.

## Rebuild

From the plugin repo:

```sh
scripts/build-cpamc-labels.sh /tmp/management-workbuddy-labels.html
```

The script clones the pinned upstream tag, applies the patch, installs its locked
Bun dependencies, runs tests/lint/build, and copies `dist/index.html` to the output.
It uses Docker `oven/bun:1.3.14`; the build needs network access for the initial
clone/image/dependency downloads. The generated HTML contains no credentials.

Deploy the resulting file to `/CLIProxyAPI/static/management.html` in CPA, with a
backup first. Keep panel auto-update OFF for the v7 host (CB-027). Bind-mount the
static directory in the runtime's container recipe if it must survive recreation;
`docker cp` alone survives restart but not recreation. Do not change auth UUIDs,
tokens, labels, or the host routing configuration to fix display names.

This patch belongs to **CPAMC**, not the plugin `.so`; users need the patched host
panel for Auth Files cards. The Model Manager dropdown fix ships in the `.so`.
