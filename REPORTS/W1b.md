# W1b (loop guard) — status: DONE

## Ringkasan (3 kalimat)
Loop guard 0.9.7 sudah terpasang di satu-satunya funnel non-stream (`aggregateCompletionCounted`) sebagai `guardToolCalls()`: cap 32 tool call per completion + dedup exact-duplicate (id+name+arguments), kelebihan → error bersih (payload tidak dikembalikan, jadi host membuang hasilnya — bukan truncate diam-diam), plus log `toolcall guard tripped: N folded to M` ke stderr. Test replika kasus produksi 1.014 `echo skip` sekarang error, sedangkan 1–6 call normal lolos utuh dan fragment merge multi-index tidak salah ke-dedup. `go test -race` + `go vet` + `gofmt` hijau via docker `golang:1.26.0-bookworm`; hanya `workbuddy/stream.go` + `workbuddy/toolcall_guard_test.go` yang disentuh.

## Deliverables
- `workbuddy/stream.go` (+88/-1) — guard, dedup, log helper, satu hook di fold tool_calls.
- `workbuddy/toolcall_guard_test.go` (baru, 326 baris) — 9 test guard + helper SSE/delta.
- Branch `feat/mm-guard` dari `origin/dev` @ `03001d4`. Commit: `933bdfc` (kode+test) + commit report ini (pushed ke `origin/feat/mm-guard`).
- `REPORTS/W1b.md` (file ini).

## Yang diimplementasikan (bullet per fitur, sesuai brief item)
- **Satu funnel, sesuai brief**: `guardToolCalls()` dipanggil di `aggregateCompletionCounted` (`stream.go:424`) tepat sebelum `message["tool_calls"] = …`. Tidak ada jalur lain yang merakit `tool_calls` (streaming `pumpAttempt` mengalirkan chunk mentah tanpa fold, jadi memang bukan tempat guard).
- **Cap 32**: `const maxToolCallsPerCompletion = 32`.
- **Dedup exact-duplicate**: `foldDuplicateToolCalls()` + `toolCallIdentity()`; key = `id \x00 function.name \x00 function.arguments`, urutan asli dipertahankan (first occurrence menang).
- **Kelebihan → error bersih**: `return nil, sawData, err` → `upstreamAttempt` → `classifyNonStream` melihat status 0 → `outcomeStop` (tidak di-retry ke akun lain, jadi tidak double-bill) → `return nil, fin.Err` ke host. Host membuang payload saat plugin error, jadi klien dapat kegagalan bersih, bukan completion terpotong.
- **Log saat trip**: `guardLogf("toolcall guard tripped: %d folded to %d", total, len(folded))` lewat `guardLogWriter` (default `os.Stderr`, di-swap buffer di test). Plugin tidak punya RPC log ke host (tidak ada `MethodHostLog` di `sdk/pluginabi`), jadi stderr = satu-satunya kanal; host menangkapnya di log plugin.
- **File lain tidak disentuh**: `models.go`/`management.go`/`main.go`/`go.mod` tidak diubah. `go.mod` di working tree masih ada baris `yaml.v3` dari W1 lama (uncommitted) — **sengaja tidak saya commit**; biarkan W1a yang membawanya.

### Keputusan semantik yang perlu di-review lead (penting)
1. **Cap dihitung dari jumlah delta mentah, bukan hasil fold.** `if total > 32 || len(folded) > 32` → error. Alasannya: badai duplikat (mis. 1.014 call dengan id yang sama) tidak boleh menyamarkan diri dengan cara "fold ke 1 call lalu lolos" — guard harus tetap trip, dan requirement test "1.014 → error" jadi benar tanpa bergantung pada apakah upstream memberi id unik. Efek samping: completion dengan >32 call mentah selalu error walau setelah fold ≤32 (dianggap memang signature loop, bukan turn multi-tool yang wajar). Kalau lead lebih suka cap dihitung **setelah** fold (1014 exact-dup → fold ke 1 → lolos), cukup ubah kondisi di `stream.go:486` jadi `len(folded) > maxToolCallsPerCompletion` saja — test 1.014 saya pakai id unik sehingga tetap hijau di dua interpretasi.
2. **Call tanpa `id` tidak pernah di-dedup** (`toolCallIdentity` return ok=false). Tanpa id tidak ada bukti dua call itu sama, jadi keduanya dipertahankan (masih dibatasi cap). Ini yang melindungi anti-regresi "fragment merge multi-index tidak boleh salah ke-dedup" untuk upstream yang tidak mengirim id.
3. **Dedup juga berlaku saat di bawah cap** (mis. 5 call → 3 call) dan tetap menulis log fold. Log fold di bawah cap bukan error; kalau lead mau log hanya saat error, hapus blok `if len(folded) != total` di `stream.go:490`.

## Verifikasi (WAJIB punya bukti)
Dijalankan di docker `golang:1.26.0-bookworm` (bukan alpine), module cache host di-mount:

```
$ docker run --rm -v "$PWD":/src -w /src/workbuddy -v /home/noel/go/pkg/mod:/go/pkg/mod \
    golang:1.26.0-bookworm bash -c 'gofmt -l . && echo "gofmt clean" && go vet ./... && echo "vet clean"'
gofmt clean
vet clean

$ docker run --rm -v "$PWD":/src -w /src/workbuddy -v /home/noel/go/pkg/mod:/go/pkg/mod \
    golang:1.26.0-bookworm bash -c 'go test -race -count=1 ./...'
ok  	github.com/zidanefaqih/codebuddy-intl-cpa/workbuddy	1.263s
```

Test guard saja (`-v`):

```
$ ... go test -race -count=1 -run "ToolcallGuard|GuardToolCalls|GuardLogWriter" -v ./...
--- PASS: TestToolcallGuard_RealWorld1014EchoSkip (0.11s)
--- PASS: TestToolcallGuard_NormalCallsPassThrough (0.01s)
    --- PASS: .../1_calls ... /2_calls ... /3_calls ... /4_calls ... /5_calls ... /6_calls
--- PASS: TestToolcallGuard_FragmentMergeMultiIndexNotDeduped (0.00s)
--- PASS: TestGuardToolCalls_FoldsExactDuplicates (0.00s)
--- PASS: TestGuardToolCalls_CapTripsOnDistinctCalls (0.00s)
--- PASS: TestGuardToolCalls_CapBoundaryKeeps32 (0.00s)
--- PASS: TestGuardToolCalls_IdlessCallsNeverDeduped (0.00s)
--- PASS: TestGuardToolCalls_EmptyAndNil (0.00s)
--- PASS: TestGuardLogWriterDefaultsToStderr (0.00s)
PASS
ok  	github.com/zidanefaqih/codebuddy-intl-cpa/workbuddy	1.155s
```

Isi test (semua lewat `aggregateCompletionCounted` untuk 3 test pertama, jadi menguji jalur produksi, bukan cuma fungsi guard):
- `TestToolcallGuard_RealWorld1014EchoSkip` — SSE 1.014 delta `echo` `{"text":"skip"}` (id unik per call, persis pola produksi) → assert: `err != nil`, `out == nil` (tidak ada payload), `sawData == true`, pesan error menyebut `toolcall guard` + angka `1014`, log memuat `toolcall guard tripped: 1014 folded to 1014`.
- `TestToolcallGuard_NormalCallsPassThrough` — n=1..6, id/name/arguments unik → jumlah call utuh, urutan/index utuh, argumen utuh, log **kosong**.
- `TestToolcallGuard_FragmentMergeMultiIndexNotDeduped` — index 0 dipecah 4 frame (`ec`+`ho`, arguments 2 fragmen), index 1 call dengan name+arguments **identik** tapi id beda → hasil 2 call, name `echo`, arguments `{"text":"skip"}`, log kosong.
- `TestGuardToolCalls_FoldsExactDuplicates` — 5 call / 3 exact-dup → 3 call sisa (first occurrence, urutan benar) + log `5 folded to 3`, tanpa error.
- `TestGuardToolCalls_CapTripsOnDistinctCalls` — 33 call distinct → error, `got == nil`, log `33 folded to 33`.
- `TestGuardToolCalls_CapBoundaryKeeps32` — tepat 32 → lolos.
- `TestGuardToolCalls_IdlessCallsNeverDeduped` — 2 call identik tanpa id → tetap 2.
- `TestGuardToolCalls_EmptyAndNil` — nil/empty → no-op tanpa log.
- `TestGuardLogWriterDefaultsToStderr` — sink default `os.Stderr`.

Manual check: `git diff --stat` mengonfirmasi hanya `workbuddy/stream.go` (+88/-1) yang berubah; `gofmt -l .` kosong (tidak ada file yang perlu diformat ulang). Tidak ada build/tag/release yang dijalankan.

## Catatan untuk lead (merge)
- **Kontrak endpoint**: tidak relevan (guard internal, tidak menambah/mengubah route). Tidak ada perubahan pada `models_config` sama sekali → aman paralel dengan W1a/W1c/W1d.
- **Titik konflik merge yang diperkirakan**:
  - `workbuddy/stream.go` — saya menambah 88 baris (import `os`, hook di blok `if len(toolOrder) > 0`, helper baru sebelum `mergeToolCallDelta`). Konflik hanya mungkin kalau worker lain menyentuh `stream.go`; menurut pembagian brief, tidak ada. Blok `if len(toolOrder) > 0` adalah satu-satunya baris yang saya ubah di badan fungsi, jadi rebase ke `feat/model-manager` seharusnya trivial.
  - `workbuddy/go.mod` — **tidak** saya sentuh; baris `gopkg.in/yaml.v3` milik W1a tetap uncommitted di working tree saya dan tidak ikut ter-commit.
  - `REPORTS/W1b.md` baru, tidak konflik.
- **TODO yang sengaja ditunda + alasan**:
  - Guard **tidak** dipasang di jalur streaming (`pumpAttempt`). Sesuai brief ("`aggregateCompletionCounted` (satu funnel)") dan karena jalur stream tidak mem-fold `tool_calls`; kalau nanti mau, guard-nya harus beda bentuk (per-chunk counter, bukan per-completion).
  - Tidak ada test build `c-shared` / verifikasi runtime 8318 — itu checklist lead (butuh `make build-dev` + DEV_LDFLAGS).
  - `guardLogf` hanya stderr. Kalau host CPA tidak menangkap stderr plugin, log trip tidak akan terlihat; alternatif (kalau lead mau) adalah menulis event ke state file atau mengembalikan error yang sudah memuat angka `N`/`M` — saat ini angka tersebut **sudah** ada di pesan error klien, jadi trip tetap terlihat walau log hilang.
- **Risiko/verifikasi yang disarankan saat merge**: jalankan `go test -race ./...` di `feat/model-manager` setelah W1b masuk, lalu verifikasi manual 8318 dengan 1.014 call dan cek pesan error yang sampai ke klien (harus memuat `toolcall guard`), serta pastikan chat normal multi-tool (≤32, mis. 2–3 tool call) tetap jalan.

## Blockers
Tidak ada. Tidak ada keputusan yang menghambat; item "Keputusan semantik" di atas adalah preferensi lead, bukan blocker.
