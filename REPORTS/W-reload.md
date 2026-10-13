# W-reload — status: DONE

## Ringkasan (3 kalimat)

PLAN-0907b T1–T4 selesai di branch `fix/mm-live-reload` (worktree `/tmp/cbi-w-reload`,
basis `a462bac` = `origin/dev` saat mulai): Save Model Manager sekarang memicu reload
model-registry host lewat `PATCH /v0/management/plugins/<id>/config {"models_rev":...}`
(sinkron, ~0,4–0,5 dtk, tanpa restart), routing prefix custom per-realm sudah benar
(CB-038), dan override per-akun benar-benar dipakai untuk emission/strip/disabled.
Semua diuji live di dev 8318 (langkah 1–7 hijau), `gofmt`/`go vet`/`go test -race`
hijau via docker `golang:1.26.0-bookworm`, dan dev 8318 sudah dikembalikan ke baseline.
Tidak ada keputusan di luar brief yang saya ambil sendiri — lihat § Catatan untuk lead
(2 hal butuh keputusan lead).

## Deliverables

Branch `fix/mm-live-reload`, worktree `/tmp/cbi-w-reload`, basis `a462bac`.
Commit: `e86e617` (`feat(model-manager): live model-registry reload + per-realm/per-account prefix routing`), di-push ke `origin/fix/mm-live-reload`.

File diubah (semua di `workbuddy/`):

- `models_integrate.go` — `mmTriggerHostReload` (+ `mmReloadResult`, client khusus,
  `mmNextModelsRev`), `mmPrefixFromSettings`/`mmEffectivePrefix(realm,authID)`,
  `mmPrefixRealm`, `validatePrefixSettings`, `applyDynamicPrefix(realm,authID)`,
  `mmFilterDisabled(realm,authID)`, `mmKnownPrefixes`, `stripDynamicPrefix(realm,authID)`;
  `mmNudgeModelRegistry` dihapus.
- `models_config.go` — Save/GET-reset memanggil reload + menaruh `"reload"` di response,
  validasi prefix sebelum mutasi, warning log (tanpa header/key).
- `usage_config.go` — parse `self_url` + `mm_live_reload` (config_yaml → env → default),
  accessor `selfURLValue()`/`liveReloadEnabled()`.
- `main.go` — ConfigFields `self_url` + `mm_live_reload`; call site executor pakai
  `accountRegion(sa)` + `req.AuthID`; pemilihan pool pakai `mmPrefixRealm`.
- `models.go` — `isCNModelID` = prefix efektif CN; `resolveUpstreamModel(model, realm,
  authID, attributes)`; `handleModelStatic`/`handleModelForAuth` teruskan `authID`.
- `retry.go` — `buildRequestFailoverPool` realm-general (`buildRealmFailoverPool`,
  `authInRealm`, `realmLabel`): prefix Global → pool Global-only, prefix CN → CN-only.
- `management.go` — POST `/models/config` yang error → HTTP 400 (body `{"error":...}` sama).
- `panel.html` — `mmReloadToast`; `mmSave`/`mmDeleteOverride`/`mmReset` baca `d.reload`;
  i18n EN 2 string baru.
- `models_reload_test.go` — baru, 9 test T4.

## Yang diimplementasikan (bullet per fitur, sesuai brief item)

### T1 — Live reload setelah Save (CB-037)
- `mmTriggerHostReload(hdr http.Header) mmReloadResult` menggantikan
  `mmNudgeModelRegistry` (dihapus). Dipanggil setelah mutasi state berhasil di
  `POST /models/config` (scope all/acct/auth+remove) **dan** `GET /models/config?reset=1`.
- Request: `PATCH <self_url>/v0/management/plugins/<pluginRouteID>/config`, body
  `{"models_rev":"<UnixNano unik>"}`. Hanya `Authorization` + `X-Management-Key`
  request asli yang di-forward. Pakai `pluginRouteID` (dev `workbuddy-dev`, prod `workbuddy`).
- Client HTTP khusus `mmReloadClient`: `Transport.Proxy = nil`, timeout 3 dtk, **tidak**
  lewat `hostHTTPDo`. Panggilan sinkron; terukur Save 0,41–0,48 dtk → tidak deadlock.
- Config: `self_url` (default `http://127.0.0.1:8317`, env `WB_SELF_URL`) + `mm_live_reload`
  (default `true`), keduanya terdaftar di ConfigFields `main.go`.
- Response Save/Reset + `"reload": {"status":"ok"|"failed"|"disabled"|"no_credentials",
  "http":<code>,"error":"..."}`. Gagal → warning `guardLogf` ke stderr tanpa header/key.
- Panel: `reload.status=="ok"` → toast "已保存 · 已实时生效"; selain itu warning
  "已保存，但需重启 CPA 才会更新 /v1/models". Dua string baru masuk kamus i18n EN.
- `models_rev` yang di-PATCH selalu naik (`mmNextModelsRev` menjamin beda walau dua Save
  dalam nanosecond yang sama) supaya host tidak `hash match, skipping reload`.

### T2 — Routing prefix custom (CB-038)
- `resolveUpstreamModel` sekarang terima `realm = accountRegion(sa)` (sa yang sudah
  di-parse di `main.go`) + `authID = req.AuthID`; `mmRealmHintFromAttributes` hanya
  fallback kalau `realm == ""`.
- `isCNModelID` = `mmPrefixRealm(model) == realmCN`; `mmPrefixRealm` mengenali `cn/`
  (legacy), `cb/` (legacy) dan prefix efektif realm (realm layer) CN/Global, ambil
  prefix terpanjang supaya tidak saling shadow.
- `buildRequestFailoverPool` realm-general: prefix Global (custom atau `cb/`) → pool
  **Global-only**, prefix CN → pool CN-only, bare → jalur legacy realm-aware. Strip tetap
  sekali di executor memakai realm+authID akun yang mengeksekusi (tidak mengulang pola
  CB-035 UID awal vs akhir).
- Validasi Save → HTTP 400 + pesan: prefix Global ≠ prefix CN efektif, Global ≠ `cn/`,
  CN ≠ `cb/`, karakter `[A-Za-z0-9._-]+/` saja (kosong = default realm).

### T3 — Override per-akun benar-benar dipakai
- `mmEffectivePrefix`, `mmFilterDisabled`, `applyDynamicPrefix`, `stripDynamicPrefix`
  menerima `authID` → `effectiveSettings(realm, authID)`. `handleModelForAuth` pakai
  `req.AuthID`; executor pakai `req.AuthID`; `handleModelStatic` tetap realm-only.
- `applyDynamicPrefix` mode bare juga strip `cb/` (sebelumnya hanya `cn/`).
- **Isi `authOverride` state produksi** (read-only, tanpa token) — lihat § Catatan untuk lead.
- Cache `loadState()` by mtime: **tidak dikerjakan** — `loadStateLocked` sudah meng-cache
  per path (bukan baca disk per request chat), jadi tidak ada yang perlu diperbaiki. Lihat
  § Catatan untuk lead.

### T4 — Test + verifikasi
Lihat § Verifikasi.

## Verifikasi (WAJIB punya bukti)

### Unit/lint (docker `golang:1.26.0-bookworm`, volume `cpa-gomod`)

```
$ docker run --rm -v "$PWD":/src -v cpa-gomod:/go/pkg/mod -v wb-gocache:/root/.cache/go-build \
    -w /src/workbuddy golang:1.26.0-bookworm bash -c 'gofmt -l . && go vet ./... && go test -race -count=1 ./...'
(no output dari gofmt)
ok  github.com/zidanefaqih/codebuddy-intl-cpa/workbuddy  1.896s
```

Test baru (`go test -race -count=1 -run "Reload|Prefix|Override" -v`):

```
--- PASS: TestMMTriggerHostReloadOK                 (PATCH, path, header forwarded, models_rev beda tiap call)
--- PASS: TestMMTriggerHostReloadNoCredentials       (tanpa header → no_credentials, host tak dipanggil)
--- PASS: TestMMTriggerHostReloadUnauthorized        (401 → failed/401, error tidak memuat credential)
--- PASS: TestMMTriggerHostReloadDisabled            (mm_live_reload=false → disabled, host tak dipanggil)
--- PASS: TestModelsConfigSaveTriggersReload         (response Save punya reload.status=ok, host dipanggil 1×)
--- PASS: TestPrefixRoutingCustomRealm               (prefix Global custom strip; prefix CN custom → isCNModelID true)
--- PASS: TestPerAccountOverrideEmissionAndStrip     (emission+strip per auth; disabled override per auth)
--- PASS: TestValidatePrefixSettings                 (tabrakan prefix + karakter → error)
--- PASS: TestBuildRequestFailoverPoolRealmPrefix    (prefix Global → pool Global-only; CN → CN-only)
```

JS panel: 3 blok `<script>` diekstrak, `node --check` → OK semua.

### Build dev

```
$ docker run ... golang:1.26.0-bookworm make build-dev
CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "-X main.version=0.9.7-dev \
  -X main.pluginRouteID=workbuddy-dev -X 'main.pluginDisplayName=CodeBuddy Dev' ..." -o workbuddy-dev.so .
```

Artefak keluar repo (`/tmp/w-reload-out/workbuddy-dev.so`). Backup dev lama:
`/home/noel/cliproxyapi-dev/plugins/workbuddy-dev.so.bak-0.9.7-dev-wreload-pre`.
Config dev: backup `config.yaml.bak-wreload`, ditambah `self_url: "http://127.0.0.1:8318"`
dan `mm_live_reload: true` (dev-only; tanpa ini default 8317 salah di container dev 8318).
`docker restart cli-proxy-api-dev` → log: `plugin registered ... version=0.9.7-dev`.

### Live 8318 (key dari `/home/noel/cliproxyapi-dev/management.env`, tidak ditulis ke report)

**Baseline awal:** `/v1/models` total 46, `cn/` 20, `cb/` 0, prefix global off, override kosong.

1. **Save Global prefix `yes/`** → `reload={"status":"ok","http":200}`, Save `real 0m0.483s`;
   poll pertama `/v1/models` → `yes_count=26` (tanpa restart). ✅
2. **Chat `yes/deepseek-v4.1-flash`** → `http=200`, `model=deepseek-v4.1-flash`,
   content `OK`, time 0,98 dtk. ✅ (CB-038)
3. **Disable/enable `glm-5.3` 2 putaran** (reload ok tiap kali):
   disable → `yes/glm-5.3 count = 0`; enable → `1`; disable → `0`; enable → `1`. ✅
   (bukti tidak kena `hash match, skipping reload`)
4. **Override per-akun** (`workbuddy-1efdaccc-…json`, prefix `ovr/`):
   sebelum `total 46 yes/ 26 ovr/ 0` → sesudah `total 72 yes/ 26 ovr/ 26` →
   Delete override → `total 46 yes/ 26 ovr/ 0`. Semua live tanpa restart. ✅
5. **`self_url` port mati** (`http://127.0.0.1:9`) + restart dev: Save `http=200`,
   `reload={"status":"failed","error":"Patch \"http://127.0.0.1:9/...\": connect: connection refused"}`,
   state tetap tersimpan (`global prefix {enabled:true,value:"yes"}`). Panel menampilkan
   warning restart via `mmReloadToast` (kode + string i18n diverifikasi; JS valid). ✅
   `self_url` dikembalikan ke 8318 + restart.
6. **Realm CN prefix custom `zcn/`**: Save → reload ok → `/v1/models` `zcn/ 20`, `cn/ 0`;
   chat `zcn/hy3` (gratis) → `http=200`, `model=hy3`; disable/enable `hy3` 2 putaran →
   `0/1/0/1`. ✅
   Validasi Save (spasi 7 dtk karena rate-limit plugin 5 burst / 1 token per 6 dtk):
   - global prefix `cn` → `400 {"error":"prefix \"cn/\" is reserved for CN models"}`
   - cn prefix `cb` → `400 {"error":"prefix \"cb/\" is reserved for Global models"}`
   - global prefix `zcn` (bentrok CN efektif) → `400 {"error":"prefix \"zcn/\" collides with the CN realm prefix \"zcn/\""}`
   - `bad prefix!` → `400 {"error":"invalid prefix \"bad prefix!\": use only A-Za-z0-9._- followed by an optional /"}`
7. **Cek log container** (`docker logs --since 20m | grep -iE "plugin|reconfig|checkin|keepalive|lifecycle|bonus|error|panic"`):
   hanya 3 baris load/register (dari 3 restart) + 1 baris warning reload gagal yang
   disengaja. **Tidak ada** aktivitas checkin/keepalive/lifecycle/daily-bonus berulang,
   tidak ada panic/error lain. Reconfigure tiap Save tidak memicu efek samping. ✅

### Baseline akhir dev 8318

`workbuddy-state.json` → `{"realm":{},"authOverride":{},"update":{"seenVersion":""}}`;
`/v1/models` total 46, `cn/` 20, `yes/` 0, `zcn/` 0. `models_rev` tertinggal di
`config.yaml` (boleh, sesuai brief). `self_url`/`mm_live_reload` tetap di dev config
(dibutuhkan dev; dev-only). Backup `.so` + `config.yaml` tersedia.

## Catatan untuk lead (merge)

### Kontrak endpoint — penyimpangan
1. **Response Save + field `reload`** (sesuai brief). **Tambahan**: POST `/models/config`
   yang error sekarang balas **HTTP 400** (sebelumnya 200 + body `{"error":...}`) untuk
   memenuhi "Validasi saat Save (400 + pesan jelas)". Body tetap `{"error":"..."}` dan
   `api()` panel tetap membacanya. GET/reset tidak diubah. **Kalau lead ingin 200 tetap
   dipertahankan, ini 1 baris di `management.go` yang bisa dibalik.**
2. Reload PATCH menulis `models_rev` ke `plugins.configs.<id>` di `config.yaml` host
   (konsekuensi mekanisme beku #1). Nilainya tertinggal/naik terus; tidak dibaca plugin.
   `SaveConfigPreserveComments` mempertahankan `self_url`/`mm_live_reload` (terverifikasi).

### T3 — override produksi yang bakal mendadak aktif (brief minta ditulis)
State produksi `/home/noel/cliproxyapi/auths/.codebuddy_data/workbuddy-state.json`
(dibaca read-only via `docker exec cli-proxy-api`, tanpa token):
- `realm.global`: prefix `{on:true,val:""}` → efektif `cb/`; `disabled` 16 entri
  (hy3, step-5-preview, default-model, fast-model, balanced-model, primary-model,
  deep-model, deepseek-v4.1-flash-sg, gpt-5.6-luna, gpt-5.3-codex, gpt-6-sol,
  gpt-5.6-terra, gpt-5.4, gpt-5.5, gpt-5.6-sol, gpt-image-2.5-sunburst);
  `custom` 0; fallback/retry off.
- `realm.cn`: **tidak ada** (default: prefix on `cn/`, disabled kosong).
- `authOverride` — 1 entri, key `workbuddy-20bba045-9178-4267-b7f1-571736fc8a0e.json`:
  prefix `{on:true,val:""}` → efektif `cb/`; `disabled` 32 entri (16 realm + glm-5.2,
  gpt-6.1-sol, gemini-3.8-flash, gemini-3.5-flash, grok-4.7, hy4-preview, kimi-k2.6,
  kimi-k2.8-preview, glm-5.1, glm-5v-turbo, kimi-k2.7, minimax-m3, hy3-preview,
  hy3-preview-agent, deepseek-v4-pro, deepseek-v4-flash, `""` (string kosong),
  grok-4.6, gemini-3.1-pro); `custom` 0; fallback/retry off.
- **Dampak setelah fix T3**: emission prefix akun itu tidak berubah (override `cb/` ==
  realm `cb/`), tetapi **filter disabled akun itu berubah** dari 16 (realm) → 32 (override):
  16 model tambahan hilang dari `/v1/models` untuk akun tersebut. Ini perilaku yang
  diminta brief, tapi berubah mendadak di produksi saat merge — tolong konfirmasi
  apakah override lama ini memang disengaja (dibuat saat tes "Save — this account only")
  atau mau dihapus lebih dulu. **Saya tidak menyentuh state produksi.**

### Keputusan yang butuh lead (di luar brief, tidak saya putuskan sendiri)
1. **HTTP 400 vs 200 untuk error Save** (lihat § Kontrak di atas).
2. **Nasib override produksi `workbuddy-20bba045-…json`** (lihat § T3 di atas): hapus
   sebelum merge, atau biarkan aktif?

### TODO yang sengaja ditunda
- Cache `loadState()` by mtime: tidak perlu — sudah di-cache per path.
- `mmRealmHintFromAttributes` dipertahankan sebagai fallback (bukan dihapus) karena
  signature `resolveUpstreamModel` masih defensif kalau realm kosong; masih dipakai.
- Prefix override per-akun **tidak** ikut dikenali `mmPrefixRealm` (deteksi prefix di
  level realm saja, karena pool dibentuk sebelum akun diketahui). Jalur override tetap
  benar untuk strip (pakai `authID`) dan untuk pool (routed auth menentukan realm legacy),
  tapi kalau klien mengirim prefix override ke akun yang di-route ke peer lain, id bisa
  lolos tanpa strip. Kasus pinggir, tidak diminta brief; dicatat di sini.

### Konflik merge yang diperkirakan
- `models_integrate.go` dan `retry.go` berubah banyak; kalau ada worker lain yang
  menyentuh `buildRequestFailoverPool`/prefix, konflik hampir pasti. Basis `a462bac`
  belum ada perubahan lain di `dev` saat saya mulai.

## Blockers

Tidak ada.
