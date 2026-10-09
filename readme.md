# Dokumentasi Telegram Wrapper

Program ini adalah server HTTP berbasis Go untuk mengelola alur login QR Telegram, menyimpan sesi, dan menyediakan endpoint REST. Program menggunakan `gotd/td` untuk koneksi MTProto. Beberapa endpoint pengelolaan chat saat ini masih berupa kerangka/simulasi; batasan tersebut dijelaskan pada bagian API.

## Alur Kerja Program

1. `main.go` memanggil konfigurasi CLI. Program berhenti jika `--app-id` atau `--app-hash` tidak ada atau tidak valid, kemudian memeriksa bahwa `--api-key` sudah diisi.
2. Program membuka logger dan membuat direktori `database` di dalam `--run-dir`.
3. `account.Manager` membaca berkas sesi yang tersimpan dan mencoba menjalankan setiap akun kembali.
4. `auth.Manager` dan handler API dibuat, lalu HTTP server mendengarkan pada port yang dipilih.
5. Saat `/api/auth` dipanggil, program membuat `session_id` acak dan menjalankan proses login QR di goroutine terpisah. Masa berlaku awal sesi adalah dua menit. QR yang diterima dari library disimpan pada sesi aktif.
6. Jika login berhasil, data sesi disimpan ke berkas JSON dan `account.Manager` diminta memulai goroutine akun. Saat proses autentikasi selesai, sesi auth dibersihkan dari daftar sesi aktif.
7. Setiap akun aktif dijalankan melalui `telegram.Client.Run`. Permintaan yang membutuhkan `session_id` akan memeriksa akun yang sedang aktif di memori.
8. Saat proses menerima SIGINT atau SIGTERM, server berhenti dengan graceful shutdown dan akun aktif dibatalkan.

## Konfigurasi

| Flag | Wajib | Nilai bawaan | Keterangan |
| --- | --- | --- | --- |
| `--port` | Tidak | `3500` | Port HTTP server. |
| `--run-dir` | Tidak | `./` | Direktori kerja untuk log dan folder `database`. Nilainya dinormalisasi menjadi path absolut. |
| `--api-key` | Ya | kosong | Kunci yang harus disertakan pada setiap endpoint API. |
| `--app-id` | Ya | kosong | Telegram API ID numerik dari `my.telegram.org`. |
| `--app-hash` | Ya | kosong | Telegram API hash dari `my.telegram.org`. |

API key dapat diberikan sebagai parameter query `api_key` atau sebagai field form. Request JSON tidak digunakan untuk membaca API key. Gunakan HTTPS dan hindari membagikan URL yang memuat API key karena query dapat tercatat di log perantara.

## Penyimpanan dan Log

Di dalam `--run-dir`, program membuat struktur berikut:

```text
run-dir/
  database/
    <session_id>.json
  go-running.log
  go-running.log.old
  go-error.log
  go-error.log.old
```

File sesi berisi `session_id`, string sesi MTProto yang dikodekan sebagai hex, `last_login`, dan `user_id`. Saat startup, file JSON yang valid dimuat kembali sebagai akun. File yang tidak dapat dibaca atau JSON-nya rusak dilewati.

Logger menulis aktivitas ke `go-running.log` dan error ke `go-error.log` (error juga dicatat di running log). Rotasi dilakukan ketika ukuran file mencapai sekitar 1 MiB; satu berkas sebelumnya disimpan dengan akhiran `.old`. Penulisan log melakukan sync ke disk.

## API

Semua route memeriksa `api_key`. Handler saat ini tidak membatasi HTTP method secara konsisten. Parameter di bawah dikirim sebagai query string; beberapa handler juga menerima field form seperti yang disebutkan. Response JSON memakai `Content-Type: application/json`, kecuali unduhan attachment.

### Autentikasi

#### `GET` atau `POST /api/auth`

Tanpa `session_id`, memulai sesi login QR baru.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Tidak | Jika diberikan, mengambil data sesi auth yang masih aktif, bukan membuat sesi baru. |
| `pass` | Tidak | Password verifikasi dua langkah Telegram. Dipakai hanya jika Telegram memintanya setelah QR dipindai. Sertakan saat memulai sesi baru. |

Contoh memulai login:

```sh
curl 'http://localhost:3500/api/auth?api_key=YOUR_API_KEY&pass=YOUR_TELEGRAM_2FA_PASSWORD'
```

Response berisi `ok`, `session_id`, `expired` dalam format `YYYY/MM/DD HH:MM:SS`, dan `qrcode` berupa URL login Telegram. Server menunggu hingga 5 detik agar QR tersedia sebelum merespons; jika Telegram belum mengirimkannya dalam batas waktu itu, `qrcode` masih dapat kosong. Untuk meminta sesi aktif yang sama lagi, panggil `/api/auth` dengan `api_key` dan `session_id` tersebut. Sesi yang sudah tidak aktif menghasilkan HTTP 404.

```sh
curl 'http://localhost:3500/api/auth?api_key=YOUR_API_KEY&session_id=SESSION_ID'
```

Jika password tidak disertakan padahal akun memerlukan verifikasi dua langkah, login gagal dan sesi harus dimulai ulang dengan parameter `pass`. Password disimpan sementara di memori sesi dan hanya diberikan ke client Telegram jika Telegram mengembalikan `SESSION_PASSWORD_NEEDED`; setelah digunakan, nilai tersebut langsung dihapus dari sesi. Karena `pass` berada di query string, jangan memakai URL ini melalui layanan/proxy yang menyimpan access log.

#### `GET` atau `POST /api/auth/logout`

Membatalkan proses autentikasi yang masih berjalan. Ini bukan endpoint logout untuk akun Telegram yang sudah login.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | ID sesi auth yang akan dibatalkan; diterima dari query atau form. |

### Pesan dan chat

Endpoint di bawah memvalidasi bahwa `session_id` mengacu pada akun yang sedang aktif. Untuk request tersebut, implementasi sekarang belum menjalankan operasi MTProto untuk mengirim pesan, mengambil chat/pesan, bergabung, keluar, atau menghapus chat. Response selain attachment adalah data contoh/simulasi dan tidak boleh dianggap sebagai hasil operasi Telegram yang sebenarnya.

#### `GET` atau `POST /api/sendMessage`

Parameter `session_id` dapat berasal dari query atau form; parameter lain dibaca dari query.

| Parameter | Keterangan |
| --- | --- |
| `api_key` | Wajib. |
| `session_id` | Wajib, akun aktif yang akan digunakan. |
| `chat_id` | ID chat tujuan. |
| `chat_type` | Jenis chat; saat ini hanya dicatat di log. |
| `is_media` | Penanda media; saat ini hanya mempengaruhi response simulasi jika parameter media lain juga terisi. |
| `media_source` | Sumber media; belum diunduh atau dikirim. |
| `media_info` | Informasi jenis sumber; belum diproses. |
| `reply_to_message_id` | ID pesan balasan; saat ini hanya dicatat di log. |
| `callback` | URL callback; implementasi hanya mencatat rencana pemanggilan, belum mengirim HTTP request. |
| `topic_id` | ID topik; saat ini hanya dicatat di log. |

Handler mengembalikan `message_id` simulasi. Nilai `PENDING` digunakan jika `is_media`, `media_source`, dan `media_info` semuanya terisi.

#### `GET` atau `POST /api/MyChat`

Query: `api_key`, `session_id`, `page`, `max_result`, dan `type`. `page` yang tidak valid atau kurang dari 1 menjadi `1`; `max_result` yang tidak valid atau kurang dari 1 menjadi `10`. Response saat ini adalah satu entri chat contoh, bukan daftar chat Telegram.

#### `GET` atau `POST /api/joinChat`

Query: `api_key`, `session_id`, dan opsi `chat_id`, `username`, atau `join_url`. Belum menjalankan join ke Telegram; handler mengembalikan `{"ok":true}` setelah validasi akun.

#### `GET` atau `POST /api/readChat`

Query: `api_key`, `session_id`, `chat_id`, `message_id`, `max_result`, dan `page`. Default `page` dan `max_result` masing-masing `1` dan `10`. Response berisi satu pesan contoh; pesan Telegram tidak dibaca oleh implementasi sekarang.

#### `GET` atau `POST /api/getAttachment`

Query: `api_key`, `session_id`, dan `file_id`. Response saat ini `application/octet-stream` dengan isi placeholder, bukan berkas dari Telegram.

#### `GET` atau `POST /api/leaveChat` dan `/api/deleteChat`

Query: `api_key`, `session_id`, dan `chat_id`. Kedua handler hanya memvalidasi akun dan mengembalikan `{"ok":true}`; operasi leave/delete belum dilakukan.

### Administrasi akun

#### `GET` atau `POST /api/status`

Hanya memerlukan `api_key`. Mengembalikan `total_accounts`, `active_auth`, dan `active_account`. Pada implementasi sekarang, `total_accounts` dan `active_account` sama-sama menghitung akun yang sedang tercatat di memori, bukan jumlah semua file sesi di disk.

#### `GET` atau `POST /api/AccountList`

Hanya memerlukan `api_key`. Mengembalikan daftar akun yang aktif di memori dengan `session_id`, `name`, dan `id` (`user_id`). Nilai nama dapat kosong karena informasi nama belum dipulihkan dari penyimpanan.

#### `GET` atau `POST /api/RemoveAccount`

Memerlukan `api_key` dan `session_id` (query atau form). Membatalkan goroutine akun dan menghapus berkas sesi dari database. Kode saat ini belum memanggil operasi logout MTProto meskipun ada komentar yang menyebut logout.

## Menjalankan Program

Pastikan Go tersedia dan dependensi modul telah diunduh. `--app-id` dan `--app-hash` bisa didapatkan dari [my.telegram.org](https://my.telegram.org). Ganti nilai contoh dengan kredensial dan API key milik sendiri.

Jalankan langsung dari source:

```sh
go run main.go --port="3500" --run-dir="./" --api-key="YOUR_API_KEY" --app-id="123456" --app-hash="YOUR_APP_HASH"
```

Atau build binary:

```sh
go build -o belajar-go main.go
./belajar-go --port="3500" --run-dir="./" --api-key="YOUR_API_KEY" --app-id="123456" --app-hash="YOUR_APP_HASH"
```




















