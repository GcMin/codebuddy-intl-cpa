# LEAD W1d — status: DONE (rollback produksi, dev-only sekarang)

## Ringkasan
W1d + merge semua worker selesai dan sempat ke-release ke produksi — **keputusan Noel: rollback** (proses salah, rilis harus nginep di `dev` dulu). Produksi kembali 0.9.6, `main` di-reset ke `03001d4`, tag & release v0.9.7 dihapus. Semua kerja 0.9.7 aman di `dev` (`e87fb5b`).

## Deliverables
- `models_integrate.go` — POST /models/refresh (fan-out), POST /models/test (SSE fold, upstream_code), routes + wiring update/check.
- `main.go` — stub `models` dihapus.
- `panel.html` (fix atas temuan Noel): sticky fetch-account picker (pilih akun balik ke awal), /models/test menerima `account` alias.
- REPORTS ini.

## Verifikasi
- go test -race hijau (dev @ e87fb5b), panel script 3/3 syntax OK.
- Dev 8318 live: GET /models/config honor `fetch_account_global` (terbukti dengan 2 akun global di dev auth dir — `fetch_account` = akun yang diminta).
- Produksi kembali 0.9.6 (registered di log).

## Catatan untuk lead (selanjutnya: lead = lead)
- PROSES: rilis berikutnya — verify dev 8318 LAMA ( hari, bukan menit), baru ke main. Jangan pernah langsung main.
- Rollback path terbukti jalan: backup .so + reset main + hapus tag/release.
