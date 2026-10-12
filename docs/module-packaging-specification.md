# Spesifikasi Standar Paket Modul WebAssembly Subzero

Dokumen ini merupakan panduan spesifikasi teknis resmi (*Official Technical Specification*) untuk pembuatan, pengemasan, validasi, dan publikasi modul transformasi data WebAssembly (Wasm) pada ekosistem **Subzero**.

---

## 1. Filosofi & Tujuan Desain

Platform SaaS B2B Subzero memproses berkas dokumen (spreadsheet, CSV, JSON, atau aliran byte data mentah) dari puluhan hingga ratusan klien (*tenants*) yang masing-masing memiliki format kolom, validasi, dan aturan bisnis berbeda.

Untuk menjaga integritas server utama dan mencegah kegagalan (*crash*) lintas klien, seluruh logika pemrosesan tenant diisolasi ke dalam **Self-Contained Micro-Package** yang dikompilasi menjadi bytecode WebAssembly sandboxed (`GOOS=wasip1 GOARCH=wasm`).

Setiap modul yang diunggah ke Subzero wajib dibungkus sebagai arsip `.tar.gz` yang mematuhi standar struktur direktori dan tata kelola keamanan. **Setiap pelanggaran terhadap spesifikasi ini akan langsung ditolak sistem (*Strict Rejection Policy*).**

---

## 2. Struktur Direktori Standar

Arsip `.tar.gz` modul wajib memiliki hierarki direktori sebagai berikut:

```text
<nama-modul>/
├── config.zero          # [WAJIB] Manifest spesifikasi runtime, metadata & batas kuota
├── readme.md            # [WAJIB] Dokumentasi modul, peruntukan tenant, & skema data
├── main.go              # [WAJIB] Entrypoint Go utama (package main, func main)
├── go.mod               # [WAJIB jika ada import] Deklarasi modul Go
├── go.sum               # [OPSIONAL] Checksum verifikasi dependensi pihak ketiga
├── parsers/             # [OPSIONAL] Sub-package internal pendukung logika
│   └── normalizer.go
└── schema/              # [OPSIONAL] Contoh data & skema validasi
    ├── sample.input.csv
    └── expected.output.json
```

---

## 3. Spesifikasi Berkas `config.zero`

Berkas `config.zero` adalah **Manifest Kontrak Modul** di Subzero. Berkas ini diletakkan di root arsip dan dapat ditulis dalam format **YAML / Key-Value** atau **JSON**.

### Schema Reference

| Field | Tipe Data | Status | Default | Deskripsi |
| :--- | :--- | :--- | :--- | :--- |
| `name` | String | **Wajib** | - | Identifier unik modul (hanya huruf, angka, `-`, `_`). |
| `version` | String | Opsional | `1.0.0` | Versi semantik modul (*Semantic Versioning*). |
| `tenant` | String | Opsional | `default` | Identitas klien / tenant pemilik modul. |
| `target` | String | Opsional | `wasip1` | Target arsitektur runtime Wasm (standar: `wasip1`). |
| `entrypoint`| String | Opsional | `main.go` | Nama berkas Go yang memuat `package main` & `func main()`. |
| `description`| String | Opsional | - | Ringkasan fungsionalitas modul. |
| `memory_limit_pages` | Integer | Opsional | `512` | Batas maksimum RAM dalam ukuran halaman Wasm (1 halaman = 64 KB. `512` = 32 MB). |
| `timeout` | Duration | Opsional | `10s` | Batas waktu maksimal eksekusi CPU sebelum dimatikan paksa. |

### Contoh Format `config.zero`

#### Opsi A: YAML / Key-Value (Dianjurkan)
```yaml
# config.zero
name: bca-statement-normalizer
version: 1.0.0
tenant: client-bank-bca
target: wasip1
entrypoint: main.go
description: Normalisasi mutasi rekening Bank BCA ke format kanonikal Subzero
memory_limit_pages: 512
timeout: 15s
```

#### Opsi B: Format JSON
```json
{
  "name": "bca-statement-normalizer",
  "version": "1.0.0",
  "tenant": "client-bank-bca",
  "target": "wasip1",
  "entrypoint": "main.go",
  "description": "Normalisasi mutasi rekening Bank BCA ke format kanonikal Subzero",
  "memory_limit_pages": 512,
  "timeout": "15s"
}
```

---

## 4. Spesifikasi Berkas `readme.md`

Sebagai platform B2B multi-tenant, setiap modul **wajib menyertakan dokumentasi yang jelas** dalam berkas `readme.md` (atau `README.md`) non-kosong.

Dokumentasi ini harus memuat:
1. **Tujuan Bisnis**: Penjelasan singkat modul dan nama tenant pengguna.
2. **Karakteristik Format Asal (Input)**:
   * Urutan nama kolom mentah (misal: `NoRek, MataUang, SaldoKotor, CatatanInternal, NamaLengkap`).
   * Format penulisan angka atau tanggal (misal: pemisah desimal koma vs titik, nominal dalam satuan sen).
3. **Format Kanonikal Tujuan (Output)**:
   * Skema output standar yang dikeluarkan modul (misal: JSON/CSV dengan kolom standar `account_id, currency, balance, holder_name`).
4. **Aturan Penanganan Anomali**: Penjelasan jika modul mengabaikan baris kosong, baris subtotal, atau header ganda.

---

## 5. Entrypoint `main.go` & Modularitas Kode

### A. Persyaratan Entrypoint
* Berkas yang ditunjuk oleh `entrypoint` (default: `main.go`) wajib dideklarasikan dengan `package main` dan memiliki fungsi `func main()`.
* **Protokol I/O Aliran Data**:
  * **Input**: Membaca aliran data mentah langsung dari Standard Input (`os.Stdin`).
  * **Output**: Menuliskan data hasil normalisasi yang sudah bersih ke Standard Output (`os.Stdout`).
  * **Logging & Error**: Pesan debug atau peringatan dikirim ke Standard Error (`os.Stderr`).

### B. Impor Kode Internal
Kode tidak perlu ditumpuk dalam satu file. Anda dapat memecahnya ke dalam beberapa file `.go` atau sub-folder internal:
```go
package main

import (
    "fmt"
    "os"
    "my-module/parsers" // Mengimpor sub-package internal di arsip yang sama
)

func main() {
    parsers.Process(os.Stdin, os.Stdout)
}
```
Kompiler Subzero mengompilasi **seluruh direktori package**, sehingga semua berkas internal otomatis terhubung (*linked*).

### C. Impor Library Eksternal via `go.mod`
Modul dapat mengimpor library pihak ketiga dengan syarat:
1. Menyertakan berkas `go.mod` (dan `go.sum`).
2. Library harus merupakan **Pure Go** yang kompatibel dengan target `wasip1` (tidak menggunakan CGo native).

---

## 6. Kontrak Antarmuka Pemrosesan (Subzero Go SDK)

Untuk mempermudah pembuatan modul dan memastikan pemrosesan data terstandarisasi, Subzero menyediakan pustaka resmi **`pkg/sdk`** (`github.com/rachmanzz/subzero/pkg/sdk`).

### A. Pola Dua Tahap: `Receive` dan `Return`

Setiap modul transformasi mengimplementasikan interface `sdk.Transformer`:

```go
type Transformer interface {
    // Receive menerima dan memvalidasi request metadata & payload mentah dari host Subzero.
    Receive(ctx context.Context, req *sdk.Request) error

    // Return memproses data dan mengembalikan hasil normalisasi serta metrik jumlah baris ke Subzero.
    Return(ctx context.Context) (*sdk.Response, error)
}
```

#### Struktur Data `sdk.Request` (Input)
```go
type Request struct {
    TaskID       string            // ID unik penugasan
    TenantID     string            // Identitas tenant
    ActionTarget string            // Target aksi (misal: "normalize", "parse")
    SourceURI    string            // URI berkas sumber (S3 / lokal) jika ada
    CustomConfig map[string]string // Konfigurasi dinamis dari SUBZERO_CFG_*
    Payload      []byte            // Byte data mentah (CSV, JSON, teks)
}
```

#### Struktur Data `sdk.Response` (Output)
```go
type Response struct {
    IsSuccess     bool              // Status keberhasilan eksekusi
    ProcessedRows int               // Metrik jumlah baris valid yang berhasil diproses
    Data          []byte            // Byte data hasil normalisasi (CSV/JSON kanonikal)
    OutputURI     string            // URI berkas tujuan jika ditulis langsung
    Metadata      map[string]string // Metadata tambahan (misal: delimiter, encoding)
    ErrorMessage  string            // Pesan error jika transformasi gagal
}
```

### B. Contoh Implementasi Lengkap `main.go`

```go
package main

import (
    "bytes"
    "context"
    "encoding/csv"
    "fmt"
    "io"

    "github.com/rachmanzz/subzero/pkg/sdk"
)

type StatementNormalizer struct {
    req  *sdk.Request
    rows [][]string
}

// 1. Receive: Menerima data dan memvalidasi payload
func (n *StatementNormalizer) Receive(ctx context.Context, req *sdk.Request) error {
    if len(req.Payload) == 0 {
        return fmt.Errorf("payload kosong")
    }
    n.req = req

    r := csv.NewReader(bytes.NewReader(req.Payload))
    for {
        record, err := r.Read()
        if err == io.EOF {
            break
        }
        if err != nil {
            return fmt.Errorf("gagal parsing baris CSV: %w", err)
        }
        n.rows = append(n.rows, record)
    }
    return nil
}

// 2. Return: Mengembalikan data kanonikal dan metrik baris
func (n *StatementNormalizer) Return(ctx context.Context) (*sdk.Response, error) {
    var outBuf bytes.Buffer
    w := csv.NewWriter(&outBuf)

    // Tulis header standar kanonikal Subzero
    _ = w.Write([]string{"tenant_id", "row_index", "raw_content"})

    for i, row := range n.rows {
        _ = w.Write([]string{n.req.TenantID, fmt.Sprintf("%d", i+1), fmt.Sprintf("%v", row)})
    }
    w.Flush()

    return &sdk.Response{
        IsSuccess:     true,
        ProcessedRows: len(n.rows),
        Data:          outBuf.Bytes(),
        Metadata: map[string]string{
            "schema_version": "v1.0",
        },
    }, nil
}

func main() {
    // Jalankan transformer via Subzero SDK runtime
    sdk.Run(&StatementNormalizer{})
}
```

> **Alternatif Fungsional:** Anda juga dapat menggunakan handler fungsional satu baris:
> ```go
> sdk.Handle(func(ctx context.Context, req *sdk.Request) (*sdk.Response, error) {
>     // Logika transformasi singkat
>     return &sdk.Response{IsSuccess: true, ProcessedRows: 1, Data: req.Payload}, nil
> })
> ```

---

## 7. Model Keamanan & Audit Statis AST

Sebelum masuk ke tahap kompilasi, Subzero menjalankan pemindaian AST (*Abstract Syntax Tree Static Analysis*) untuk mendeteksi vektor serangan dan celah keamanan:

### Aturan Impor Terlarang (Forbidden Imports)
| Paket Terlarang | Alasan Penolakan |
| :--- | :--- |
| `os/exec` | Mencegah eksekusi perintah shell OS atau remote command execution. |
| `syscall` | Mencegah panggilan kernel traps dan manipulasi proses tingkat rendah. |
| `unsafe` | Mencegah manipulasi pointer memori bebas di luar batasan sandbox. |
| `plugin` | Mencegah pemuatan pustaka dinamis (.so) dari luar. |
| `runtime/cgo` | Mencegah pemanggilan kode biner C asli tanpa perlindungan Wasm. |

### Aturan Vektor Jaringan & Shell
* **Network Server Listener**: Pemanggilan `net.Listen` atau `net.ListenPacket` otomatis ditolak untuk mencegah worker dijadikan backdoor listener.
* **Suspicious Shell Literals**: String mencurigakan seperti `/bin/sh`, `/bin/bash`, `cmd.exe`, atau `powershell` yang digabungkan dengan panggilan sistem akan memicu penolakan.

### Aturan Integritas Arsip
* **Zip-Slip Protection**: Jalur berkas di dalam tar tidak boleh mengandung manipulasi path traversal (misal: `../../etc/passwd`).
* **Decompression Bomb Protection**: Ukuran tidak terkompresi dibatasi maksimal **25 MB** dan jumlah berkas maksimal **50 berkas**.

---

## 8. Matriks Penolakan Sistem (*Rejection Policy*)

| Pelanggaran | Status HTTP | Response Payload |
| :--- | :--- | :--- |
| Format bukan `.tar.gz` / `.tgz` | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "uploaded file must be a .tar.gz bundle..."}` |
| Berkas `config.zero` hilang / kosong | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: package must contain config.zero..."}` |
| Berkas `readme.md` hilang / kosong | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: package must contain readme.md..."}` |
| Berkas `main.go` tidak memiliki `func main()` | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: entrypoint does not contain package main..."}` |
| Ditemukan impor terlarang / backdoor | `422 Unprocessable Entity` | `{"stage": "security_audit", "error": "Security validation failed...", "audit": {...}}` |

---

## 9. Alur Kerja Pengembang via `subzero-cli`

Subzero menyediakan perangkat CLI terpadu untuk mempermudah siklus pengembangan:

### 1. Inisialisasi Scaffold Modul Baru
```bash
subzero-cli module init --name currency-normalizer --tenant acme-bank ./currency-normalizer
```
Perintah ini otomatis membuat struktur lengkap:
* `config.zero` terisi template konfigurasi.
* `readme.md` terisi kerangka dokumentasi.
* `main.go` terisi kode awal pembacaan environment.
* `go.mod` terinisialisasi.

### 2. Audit Struktur & Keamanan Lokal
```bash
subzero-cli module audit ./currency-normalizer
```
Memverifikasi kelengkapan struktur dan memindai AST Go tanpa perlu mengunggah ke server.

### 3. Kemas ke Arsip Produksi
```bash
subzero-cli module pack -o currency-normalizer.tar.gz ./currency-normalizer
```
Mengemas direktori menjadi arsip aman yang siap diunggah.

### 4. Publikasikan ke Server Subzero
```bash
subzero-cli module publish --server http://localhost:8080 --tenant acme-bank --name currency-normalizer ./currency-normalizer
```
Server akan mengaudit, mengompilasi ke format WebAssembly, dan menyimpannya ke repository modul aktif.
