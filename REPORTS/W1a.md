# WORKER-1a — status: DONE

Branch `feat/mm-catalog` (from `origin/dev` @ `03001d4`), pushed to `origin`.
Commits: `ef1e416` (go.mod yaml.v3) · `39748b9` (backend) · `1cbbd8b` (tests) · (+ this report).

## Ringkasan (3 kalimat)

Fondasi backend Model Manager 0.9.7 selesai: katalog per-realm (`modelDef` + satu API
`catalogModels`/`catalogModelInfos`), `fetchUpstreamModels` per akun (Global `/v2` ∪ `/v3/config`,
CN console ∪ `/v3/config`, UA WorkBuddy, ctx = max `supportedLengths`), state file
`workbuddy-state.json` (atomic, struktur persis kontrak) dengan `effectiveSettings(realm, authID)`
layering default → realm → akun, dan endpoint `GET/POST /models/config` (+`?reset=1`) sesuai kontrak.
`go test -race` HIJAU lewat docker `golang:1.26.0-bookworm` (183 test PASS, 0 FAIL), `gofmt` dan
`go vet` bersih. `stream.go` / `panel.html` / `models.go` / `main.go` tidak disentuh.

## Deliverables

- `workbuddy/models_config.go` — **baru, 1401 baris**: katalog, side-table metadata, fetch upstream,
  cache, state file, layering, payload + handler endpoint.
- `workbuddy/models_config_test.go` — **baru, 895 baris**: 16 test baru (semua area yang diminta brief).
- `workbuddy/management.go` — **+7/−1 baris**: registrasi 2 route (`GET`/`POST` `/plugins/workbuddy/models/config`),
  2 case dispatch, guard `modelsConfigResetRequest` (GET `?reset=1` = mutasi → kena management-key + rate limit).
- `workbuddy/go.mod` — +`gopkg.in/yaml.v3 v3.0.1 // indirect` (uncommitted change dari WORKER-1 lama, di-commit di `ef1e416`).
- `REPORTS/W1a.md` — file ini.
- Branch pushed: `git fetch && git log --oneline origin/feat/mm-catalog -4` →
  `ef1e416`, `39748b9`, `1cbbd8b` di atas `03001d4`.

## Yang diimplementasikan (per item brief)

1. **Katalog 1 sumber** — `modelDef{ID,Name,Realm,Ctx,MaxTok,Hidden,Tier,Util}` persis brief.
   `catalogModels(realm)` = konversi `wbModels()`/`wbCNModels()` + tabel metadata `wbCatalogMeta`
   (tier alias `default-model/fast-model/balanced-model/primary-model/deep-model` Global; `fast-model/
   balanced-model/deep-model` CN) + hidden verified `grok-4.6`, `gemini-3.1-pro`. **Penyimpangan yang
   disengaja:** data mentah tidak saya salin manual ke sini karena (a) `models.go` dilarang disentuh, dan
   (b) menyalin daftar itu akan menghasilkan salinan kedua yang bisa drift. Jadi katalog *mengonversi*
   tabel legacy; `TestCatalogCoversLegacyEntries` mengunci nilai (nama/ctx/maxtok) supaya tidak bisa
   melenceng, dan W1d cukup meng-inline hasil konversinya saat menghapus fungsi legacy.
2. **`fetchUpstreamModels(authID)`** — resolve kredensial via `hostAuthList`/`hostAuthGetBundle`
   (AuthID **atau** AuthIndex), realm dari `accountRegion`, token + UID + Origin/Referer per realm.
   Global: `/v2/enterprises/personal/models` (fallback ke `/console/...` bila 500), CN: `/console/...`;
   keduanya ∪ `/v3/config`, dua request paralel, salah satu gagal → degrade, dua-duanya gagal → error.
   UA **`WorkBuddy/2.63.2 WorkBuddy AI/2.63.2 CLI/2.63.2`** (diuji assertion di test).
   Union dedup dengan field `/v3/config` menang; entri tanpa id & `disabled:true` dilewati.
   **ctx = max `contextWindow.supportedLengths`** (bukan `defaultLength`), fallback `maxInputTokens`
   → `maxAllowedSize`; `credits` dinormalkan (`"x0.79 credits"` → `x0.79`); flag `hidden` (Global: tidak
   ada di `/v2`, atau hidden di katalog), `util` (id `nes-/completion-/codewise-`, out ≤256, atau set util),
   `new`, `stale`, `verified`, `effort`, `tier`; urutan render (tier → upstream per vendor alfabetis →
   harga naik → verified → util → stale → custom) karena panel hanya menyisipkan sub-header vendor saat
   family berubah. Cache TTL 5 menit per akun + `invalidateUpstreamModels()` untuk `POST /models/refresh` (W1d).
3. **State file** `workbuddy-state.json` — struktur persis kontrak:
   `{realm:{global:{disabled[],custom[],prefix{on,val},fallback{on,model},retry_fallback{...}},cn:{...}},
   authOverride:{<authId>:{...}},update:{seenVersion}}`. Save **atomic** (temp file di dir yang sama →
   `fsync` → `rename` → `fsync` dir), `WORKBUDDY_DATA_DIR`/`.codebuddy_data` (konvensi sama dengan usage log),
   file korup dipindah ke `.corrupt` (tidak ditimpa), dan `mutateState` **re-read dari disk** sebelum tulis
   supaya key `update` milik W1c tidak tertimpa. `effectiveSettings(realm, authID)` = default → realm → override akun
   (override menimpa utuh, sesuai scope selector "this account uses its own settings"); `defaultSettings`:
   Global prefix OFF (`cb/` hanya placeholder), CN prefix ON `cn/`.
4. **Endpoint** — `GET /models/config?realm=&fetch_account=` (+`reset=1&realm=R`),
   `POST /models/config {realm, scope:"all"|"acct"|"auth", auth, state:{disabled,custom,prefix_enabled,
   prefix_value,fallback_enabled,fallback_model,retry_fallback_enabled,retry_fallback_model}, remove?}`.
   Payload `realms.global|cn` berisi `scope, override_auth, accounts, fetch_account, fetched_at, upstream,
   catalog, free, prefix{enabled,value,def}, disabled, custom, fallback{enabled,model},
   retry_fallback{enabled,model}, models[{id,name,ctx,mult,hidden,verified,stale,effort,tier,util,custom,new}]`
   + `version:{current}` — persis yang dibaca panel W2 (`/tmp/w2panel.html` baris 1766-1790).
   `POST` scope `acct`/`auth` + `remove:true` menghapus override; `reset=1` menghapus layer realm
   (override per-akun tetap, karena itu record terpisah).

## Verifikasi (bukti)

Perintah (docker `golang:1.26.0-bookworm`, **bukan alpine**):

```
docker run --rm -v <worktree>:/src -v wb-gomod:/go/pkg/mod -v wb-gocache:/root/.cache/go-build \
  -w /src/workbuddy golang:1.26.0-bookworm sh -c "gofmt -l . ; go vet ./... && go test -race -count=1 ./..."
```

Hasil:

```
go version go1.26.0 linux/amd64
--- gofmt -l .      → (kosong = bersih)
--- go vet ./...    → vet-ok
--- go test -race -count=1 -v ./...
ok  github.com/zidanefaqih/codebuddy-intl-cpa/workbuddy  1.466s     (183 PASS, 0 FAIL)
```

Test baru (semua HIJAU, `-race`):

```
--- PASS: TestCatalogCoversLegacyEntries              (legacy ⊆ katalog, nilai identik, hidden tetap)
--- PASS: TestEffectiveSettingsLayering               (default → realm → akun, remove override)
--- PASS: TestResetRealmSettingsRestoresDefaults      (reset realm, override akun selamat)
--- PASS: TestStateFileAtomicSave                     (JSON kontrak, tanpa file .tmp, reload bersih, update.seenVersion selamat)
--- PASS: TestStateFileCorruptRecovery                (.corrupt + fallback default)
--- PASS: TestFetchUpstreamModelsUnionDedup           (UA WorkBuddy, union+dedup, /v3 menang, ctx max, credits, hidden/util, skip disabled/empty)
--- PASS: TestFetchUpstreamModelsEnterpriseFallback   (/v2 500 → /console; dua-duanya gagal → error)
--- PASS: TestUpstreamCacheAndInvalidate              (TTL cache + invalidate untuk /models/refresh)
--- PASS: TestBuildModelRowsFlagsAndOrder             (tier/vendor/harga, stale+~mult, verified, new, custom)
--- PASS: TestModelsConfigEndpointGet                 (semua key kontrak, prefix default per realm, counter, fetch_error)
--- PASS: TestModelsConfigEndpointPost                (save realm, override akun, remove, realm invalid ditolak)
--- PASS: TestModelsConfigResetEndpoint               (reset via handler + guard mutasi)
--- PASS: TestFormatCtx / TestParseCreditsMultiplier / TestUpstreamMaxCtx / TestAtomicWriteFileKeepsOldContentOnFailure
```

Bukti manual (test sementara `TestZZEvidence`, sudah dihapus) — state file nyata:

```json
{ "realm": { "global": { "disabled": ["gpt-6-astra"],
    "custom": [{"id":"my-model","name":"My Model","ctx":"128k"}],
    "prefix": {"on": false, "val": ""},
    "fallback": {"on": true, "model": "glm-5.2"},
    "retry_fallback": {"on": true, "model": "glm-5.1"} } },
  "authOverride": { "Intl-zxdrwin0602": { "disabled": ["hy3"], "custom": [],
    "prefix": {"on": true, "val": "cb/"}, "fallback": {"on": false, "model": ""},
    "retry_fallback": {"on": false, "model": ""} } },
  "update": { "seenVersion": "0.9.6" } }
```

Bukti payload `GET /models/config` (fixture httptest, bukan data hardcode; diambil setelah
fix `formatCtx`, lihat `/tmp/evidence2.txt` di mesin ini):

```
GLOBAL scope = "auth"   override_auth = "Intl-zxdrwin0602"   fetch_account = "Intl-zxdrwin0602"
GLOBAL upstream = 5     catalog = 23     free = 1     fetched_at = "2026-10-12T22:23:15Z"
GLOBAL prefix = {"def":"cb/","enabled":true,"value":"cb/"}
CN     scope = "all"    fetch_account = "CN-64426370"   upstream = 4
CN     prefix = {"def":"cn/","enabled":true,"value":"cn/"}

ROW: {"id":"default-model","name":"Auto","ctx":"176k","mult":"x0.79","tier":true}
ROW: {"id":"deepseek-v4.1-flash","name":"Deepseek-V4.1-Flash","ctx":"1M","mult":"x0.00","hidden":true}
ROW: {"id":"kimi-k2.8-preview","name":"Kimi-K2.8-Preview","ctx":"1M","mult":"x0.77","hidden":true,"new":true}
ROW: {"id":"codewise-jump","name":"codewise-jump","hidden":true,"util":true,"new":true}
ROW: {"id":"glm-5.1","name":"GLM-5.1","ctx":"131k","mult":"x0.79","stale":true}
ROW: {"id":"minimax-m3","name":"MiniMax M3","ctx":"205k","mult":"x0.12","verified":true,"stale":true}
ROW: {"id":"grok-4.6","name":"Grok 4.6","ctx":"500k","mult":"x0.78","hidden":true,"verified":true,"stale":true}
ROW: {"id":"gemini-3.1-pro","name":"Gemini 3.1 Pro","ctx":"1M","mult":"x0.88","hidden":true,"verified":true,"stale":true}
ROW: {"id":"gpt-6-astra","name":"GPT-6 Astra","ctx":"200k","hidden":true,"stale":true}
```

Manual check lain — `git diff origin/dev --stat`:

```
 workbuddy/go.mod                |    1 +
 workbuddy/management.go         |    8 +-
 workbuddy/models_config.go      | 1401 +++++++++++++++++++++++++++++++++++++++
 workbuddy/models_config_test.go |  895 +++++++++++++++++++++++++
 4 files changed, 2304 insertions(+), 1 deletion(-)
```

`stream.go`, `panel.html`, `models.go`, dan `main.go` **tidak ada di diff**.

## Catatan untuk lead (merge)

- **Kontrak endpoint: persis.** Tidak ada deviasi pada path, method, query, body, atau bentuk payload.
  Dua hal yang saya pastikan cocok dengan panel W2: (1) panel mengirim `scope:"acct"` (bukan `"auth"`)
  → handler menerima `acct`/`auth`/`account`; (2) `GET ?reset=1` adalah mutasi → saya tambahkan guard
  management-key + rate limit (`modelsConfigResetRequest`) supaya reset tidak bisa dipanggil tanpa key.
- **Penyimpangan sengaja (1): `models.go` tidak disentuh** (dilarang di brief) sehingga tabel legacy
  masih ada di sana; `catalogModels()` mengonversinya + tabel metadata, jadi tidak ada data katalog yang
  terduplikasi. Konsekuensi: item "pindahkan wbModels/wbCNModels" tuntas secara *API* (satu sumber untuk
  semua konsumen baru), tapi penghapusan literal di `models.go` = tugas W1d (atau lead saat merge).
  `TestCatalogCoversLegacyEntries` adalah pengunci anti-drift sampai itu terjadi.
- **Penyimpangan sengaja (2): `management.go` +7/−1.** Brief W1a minta endpoint hidup, dan registrasi
  route/dispatch ada di `management.go`; file itu bukan daftar terlarang. W1d tinggal menambah
  `POST /models/refresh` dan `POST /models/test` di `managementRegistration()` dan switch yang sama
  (`management.go` sekitar baris 145-230) — **konflik merge yang paling mungkin** kalau W1d/lead menyentuh
  dua blok itu; area `models_config.go` sendiri tidak akan konflik (file baru).
- **Ditunda (sengaja) ke W1d:**
  (a) fan-out fetch **semua** akun per realm — sekarang fetch memakai akun terpilih (`fetch_account`,
  default akun pertama) dan `upstream`/`fetched_at` melaporkan akun itu; `invalidateUpstreamModels()` +
  `upstreamModelsCached()` sudah siap dipakai route refresh;
  (b) badge `partial`/`owned_by`/`all_exhausted` (butuh entitlement per akun) — field-nya sengaja tidak
  saya isi (omitempty, panel aman);
  (c) `version.registry` di payload = milik W1c (`GET /update/check`); saya hanya mengirim `version.current`
  dan sudah menyediakan `loadUpdateState()`/`saveUpdateSeenVersion()` untuk key `update.seenVersion`.
  (d) `new` saya definisikan sebagai "ada di upstream tapi tidak ada di katalog statis" — tanpa state
  tambahan di file (kontrak state beku), jadi tidak ada badge-storm saat restart.
- **Catatan data:** dua model hidden-verified di mockup masih ber-ID kosong/merah di dokumen
  (baris mockup `{id:"",nm:"the model 4.6"/"the model 4.7 1M"}`) sehingga tidak bisa saya masukkan ke
  tabel metadata; `grok-4.6` dan `gemini-3.1-pro` sudah masuk. Satu entri legacy di blok "hidden models"
  juga tidak tertulis namanya di dokumen — itu sebabnya `hiddenLegacySignature()` (200k ctx/16k out)
  dipakai sebagai jaring agar flag `hidden`-nya tetap benar; kalau lead mau, ID-nya bisa di-inline saat
  W1d menghapus tabel legacy.
- **Lingkungan (bukan blocker, tapi perlu lead tahu):** repo utama dipakai bersama worker paralel
  (branch checkout di `/home/noel/codebuddy-intl-cpa` berubah saat saya bekerja — W1b sempat menulis
  laporan tentang insiden branch-switch). Karena itu W1a dikerjakan di worktree terpisah
  `/tmp/codebuddy-intl-cpa-w1a` (branch tetap `feat/mm-catalog` dari `origin/dev`) dan saya hanya
  `git add` file milik saya. Saat saya tiba, repo utama sedang di branch `feat/model-manager` dengan
  `workbuddy/go.mod` uncommitted — saya kembalikan ke `feat/model-manager` setelah membuat worktree.

## Blockers

Tidak ada. Semua deliverable brief W1a selesai dan hijau. Yang tersisa adalah pekerjaan worker lain
(W1b/W1c/W1d) dan keputusan lead soal item "penyimpangan sengaja (1)" di atas.
