# Release Checklist

The proven order for shipping a version to `main`, the GitHub release, and the
official plugin store. Every step below was executed verbatim in the v0.9.7
cycle (2026-10-14). **Do not reorder step 2 and step 4** — the tag must exist
before `main` moves, and the store PR must be part of every release, not an
afterthought.

## 0. Pre-flight (on `dev`, nothing merged yet)

- [ ] `workbuddy/VERSION`, `registry.json` (`version` + `release_tag`), and
      `main.go`'s `version` var all carry the target version.
- [ ] `workbuddy/CHANGELOG.md` heading drops `(unreleased)`.
- [ ] README / README.zh-CN counts match the tree (Go files, test files, panel
      line count) — these drift every release.
- [ ] Full gate, all green:
      ```bash
      gofmt -l .                                   # empty
      go vet ./...                                 # exit 0
      staticcheck ./...                            # 0 findings
      go test -race -count=1 ./...
      node --test scripts/test-panel-*.cjs         # from the repo root
      python3 scripts/validate-registry.py registry.json
      make build
      ```
- [ ] Commit, push `dev`. Working tree clean, `dev == origin/dev`.

## 1. Tag from `dev` FIRST — never merge-then-tag

Order matters. The README points users at `raw/main/registry.json` as a plugin
source, and `main`'s copy is bumped ahead of the release. If you merge `dev →
main` before the tag exists, `main` announces a version whose release 404s and
store installs break — not "old version", download failure.

```bash
make tag VERSION=x.y.z     # tags the dev HEAD and pushes the tag
```

Then **watch CI** until the release job finishes (six platform archives + the
release-attached registry). If the darwin dylib check fails, fix the
`MACOSX_DEPLOYMENT_TARGET` pin, delete and re-push the tag. `main` has not
moved, so users are unaffected while you fix forward.

## 2. Verify the release like a user would

- [ ] `gh release view vX.Y.Z`: all six zips + `checksums.txt` + stamped
      `registry.json` present.
- [ ] Download the darwin dylib from the release and run
      `scripts/check-darwin-dylib.py` against it — minos must be 11.0.0
      (0.9.6 shipped dylibs declaring minos 15.0, which crashed on macOS 11–14).
- [ ] Release-attached `registry.json` says `version`/`release_tag` matching the tag.

## 3. Merge to `main` (fast-forward only)

```bash
git checkout main && git merge --ff-only dev && git push origin main
git checkout dev
```

`main` must always be an ancestor of `dev`; if `--ff-only` fails, stop and
rebase instead of creating a merge commit. Verify `raw/main/registry.json` now
serves the new version **while the release exists** — that combination is the
whole point of the tag-first order.

## 4. Official plugin store PR (REQUIRED every release)

The default plugin source inside CPA is
`router-for-me/CLIProxyAPI-Plugins-Store`. CPA resolves the displayed version
and install target **dynamically** from the plugin repo's `releases/latest`
(install type `github-release` is the default), so a fresh release is already
installable the moment it publishes. **But** the registry's `version` field is
the display fallback used by older CPA builds and whenever the GitHub API query
fails or is rate-limited — forget this step and those users keep seeing the old
version (0.9.2 kept showing for four releases because of this).

```bash
gh repo sync zidanefaqih/CLIProxyAPI-Plugins-Store \
  --source router-for-me/CLIProxyAPI-Plugins-Store   # forks go stale; sync first
# branch, bump ONLY the workbuddy "version" line in registry.json, push, open PR
```

PR requirements (from the store's README, enforced by review):

- The plugin repository URL and the release tag in `v<version>` form.
- Evidence that all `<id>_<version>_<goos>_<goarch>.zip` assets and
  `checksums.txt` exist on the release.
- Diff limited to the one `version` line; `schema_version` stays `1`; no `v`
  prefix on the version; ids stay unique.

Reference: v0.9.7 bump was
[CLIProxyAPI-Plugins-Store#254](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/pull/254).

## 5. Post-release

- [ ] **Keep the GitHub release body short**: summary bullets (max ~8) + a link to
      the full CHANGELOG section
      (`…/blob/main/workbuddy/CHANGELOG.md#<version-without-dots>`) + the asset
      table. Do NOT paste the whole CHANGELOG section — the first v0.9.7 body was
      23KB and buried the asset list below it (direct feedback from the operator).
      Long-form release detail lives in `CHANGELOG.md`.
- [ ] Reply to issues fixed by the release **only after** the release exists
      (include the version and the upgrade path); leave still-broken issues
      open with a status comment instead.
- [ ] Mirror the store entry into `.store-submission/registry-entry-v1.json`
      so the local snapshot matches what was submitted.
- [ ] Keep `dev` and `main` identical; next cycle starts from `dev`.
- [ ] Feature-level evidence that CI cannot prove (e.g. scheduled CN check-in
      hitting its 09:00/21:00 window in real logs) is soak-followed on prod —
      noted in the CHANGELOG or an issue, not silently assumed.
