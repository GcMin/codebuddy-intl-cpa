# W1c (update checker) — status: DONE

## Ringkasan (3 kalimat)
`GET /update/check` selesai di `workbuddy/update_check.go` + `update_check_test.go` (619 baris total) di branch `feat/mm-update` (dari `origin/dev` @ `03001d4`), worktree terpisah `/tmp/w1c-update` — nol file lain disentuh. Semua kontrak PLAN-0907 diikuti: fetch `releases/latest/download/registry.json` (BUKAN raw/main), banding `plugins[0].version` vs `var version` (`0.9.6` di main.go), cache 6 jam + on-demand `refresh=1`, silent fail, `seenVersion` untuk Skip. `go test -race` HIJAU penuh via docker `golang:1.26.0-bookworm`, gofmt + go vet bersih.

## Deliverables
- `workbuddy/update_check.go` (+377) — checker, handler, persistence, version compare
- `workbuddy/update_check_test.go` (+242) — 8 test
- `REPORTS/W1c.md` — file ini
- Commits (pushed ke `origin/feat/mm-update`):
  - `fe6ad4e` w1c: report stub
  - `90112a4` w1c: update checker — GET /update/check

## Yang diimplementasikan (sesuai brief W1c)
- **Endpoint kontrak**: `handleUpdateCheck(req pluginapi.ManagementRequest) updateCheckResponse` → `{current, latest, available, checkedAt, changelog?, seen, failed?}`. Route registration (`case ... base+"/update/check"`) ditinggal untuk W1d sesuai pembagian PLAN (W1d: "Wire GET /update/check dari W1c"); handler siap dipanggil 1 baris.
- **Fetch registry**: `https://github.com/zidanefaqih/codebuddy-intl-cpa/releases/latest/download/registry.json` — release-attached, bukan raw/main (komentar di kode menjelaskan alasannya: main di-bump sebelum tag rilis ada). UA biasa browser-ish, timeout 10s, body dibatasi 1 MB, pakai `sharedHTTPClient()` yang sudah ada di `host_bridge.go`.
- **Perbandingan**: `isNewerVersion(latest, version)` — numerik per-segmen, toleransi prefix `v`, segmen kosong = 0 (`0.9` < `0.9.1`), `0.10.0 > 0.9.9` (numerik bukan leksikografis), equal/older → false.
- **Cache 6 jam + on-demand**: cache in-process `defaultUpdateChecker`; `?refresh=1` bypass cache (panel-open / manual check).
- **SILENT FAIL**: offline/GitHub down/JSON rusak/HTTP ≠ 200 → HTTP 200 dengan `available:false`, `current` tetap terisi, `failed:true` untuk debug panel; kalau ada cache lama yang masih valid, dipakai sebagai fallback. Tidak pernah error-out ke panel.
- **seenVersion ("Skip this version")**: `?seen=<ver>` mencatat + persist. Penulisan: kalau `workbuddy-state.json` (kontrak W1a) sudah ada → tulis key `update:{seenVersion}` di dalamnya mempertahankan key lain (atomic tmp+rename); kalau belum ada → fallback file kecil `workbuddy-update.json` (sesuai instruksi PLAN: lead konsolidasi saat merge). Pembacaan prefer key di state file, fallback ke file kecil. Dir via `WORKBUDDY_DATA_DIR` → auth dir → `$HOME` → tmp (pola sama dengan `usageLogDir()`).

## Verifikasi
- `docker run golang:1.26.0-bookworm`: `gofmt -l .` → kosong; `go vet ./...` → bersih.
- **`go test -race ./...` → `ok github.com/zidanefaqih/codebuddy-intl-cpa/workbuddy 1.165s`** (full package, bukan subset).
- Test yang dijalankan (semua PASS): `TestIsNewerVersion` (11 kasus), `TestFetchUpdateRegistryParsesPlugins0`, `TestFetchUpdateRegistrySilentFail` (500 + bad JSON), `TestUpdateCheckAvailable`, `TestUpdateCheckCacheTTL` (cache menang dalam TTL, kedaluwarsa → fetch baru, refresh=1 bypass), `TestUpdateCheckConcurrent` (16 goroutine, race detector bersih), `TestSeenVersionPersistence` (fallback file, state file W1a dengan sibling key utuh, skip flow), `TestHandleUpdateCheckQueryContract` (`refresh=1`, `seen=`).
- Network test tidak keluar: `updateRegistryURL` di-override ke httptest server.
- Manual: `git status` bersih — hanya 2 file baru + REPORTS.

## Catatan untuk lead (merge)
- **Kontrak endpoint**: persis `{current, latest, available, checkedAt, changelog?}` + 2 field tambahan: `seen` (W2 butuh untuk skip badge) dan `failed` (diagnostik, omitempty). Route wiring ditunggu W1d: `case req.Method == http.MethodGet && path == base+"/update/check": return okEnvelope(mgmtJSONResponse(http.StatusOK, handleUpdateCheck(req)))` — handler sudah pluginapi-shape (bukan http.HandlerFunc) mengikuti pola handler lain.
- **Koordinasi W1a**: hanya menyentuh key `update` di `workbuddy-state.json`; fallback `workbuddy-update.json` otomatis tidak dipakai lagi begitu state file ada (baca prefer state file). Lead bisa hapus fallback path saat merge kalau W1a pasti duluan.
- **URL registry pakai `var`** (bukan const) supaya bisa di-inject httptest — tidak berpengaruh runtime.
- TODO ditunda: field `release_tag` sudah di-parse tapi belum di-expose (tidak ada di kontrak; simpan untuk deep-link download nanti).
- Konflik merge diperkirakan: nol — 2 file baru, tidak menyentuh file existing. `go.mod` tidak diubah (tidak ada dep baru).

## Blockers
- (tidak ada)
