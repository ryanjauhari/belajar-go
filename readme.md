# Dokumentasi Telegram Wrapper

Program ini adalah server HTTP berbasis Go untuk mengelola alur login QR Telegram, menyimpan sesi, dan menyediakan endpoint REST. Koneksi ke Telegram memakai MTProto melalui pustaka `gotd/td`.

Beberapa endpoint pengelolaan chat (mis. `/api/deleteChat`) masih berupa kerangka/simulasi; batasan tersebut ditulis jujur pada bagian API.

## Alur Kerja Program

1. `main.go` memanggil konfigurasi CLI. Program berhenti jika `--app-id` atau `--app-hash` tidak ada atau tidak valid, kemudian memeriksa bahwa `--api-key` sudah diisi.
2. Program membuka logger dan membuat direktori `database` di dalam `--run-dir`.
3. `account.Manager` membaca berkas sesi yang tersimpan dan mencoba menjalankan setiap akun kembali.
4. `auth.Manager` dan handler API dibuat, lalu HTTP server mendengarkan pada port yang dipilih.
5. Saat `/api/auth` dipanggil, program membuat `session_id` acak dan menjalankan proses login QR di goroutine terpisah. Masa berlaku awal sesi adalah dua menit. QR yang diterima dari library disimpan pada sesi aktif.
6. Jika login berhasil, data sesi disimpan ke berkas JSON dan `account.Manager` diminta memulai goroutine akun. Saat proses autentikasi selesai, sesi auth dibersihkan dari daftar sesi aktif.
7. Setiap akun aktif dijalankan melalui `telegram.Client.Run`. Permintaan yang membutuhkan `session_id` akan memeriksa akun yang sedang aktif di memori.
8. Saat proses menerima SIGINT atau SIGTERM, server berhenti dengan graceful shutdown dan koneksi akun aktif dibatalkan. File session di `database/` tetap disimpan agar login tidak perlu diulang.

## Konfigurasi

| Flag | Wajib | Nilai bawaan | Keterangan |
| --- | --- | --- | --- |
| `--port` | Tidak | `3500` | Port HTTP server. |
| `--run-dir` | Tidak | `./` | Direktori kerja untuk log dan folder `database`. Nilainya dinormalisasi menjadi path absolut. |
| `--api-key` | Ya | kosong | Kunci yang harus disertakan pada setiap endpoint API. |
| `--app-id` | Ya | kosong | Telegram API ID numerik dari `my.telegram.org`. |
| `--app-hash` | Ya | kosong | Telegram API hash dari `my.telegram.org`. |

API key dapat diberikan sebagai parameter query `api_key` atau sebagai field form. Body JSON tidak dibaca untuk API key. Gunakan HTTPS dan hindari membagikan URL yang memuat API key karena query dapat tercatat di log perantara.

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

File sesi berisi `session_id`, string sesi MTProto yang dikodekan sebagai hex, `last_login`, `user_id`, dan `name`. Saat startup, file JSON yang valid dimuat kembali sebagai akun. File yang tidak dapat dibaca atau JSON-nya rusak dilewati. Contoh isi `database/<session_id>.json`:

```json
{
  "session_id": "e37d573cb0f0a9e27862b1446ec7910a",
  "session": "7b2256657273696f6e2...",
  "last_login": "2026/10/09 20:31:04",
  "user_id": "2064545198",
  "name": "Reza"
}
```

Logger menulis aktivitas ke `go-running.log` dan error ke `go-error.log` (error juga dicatat di running log). Rotasi dilakukan ketika ukuran file mencapai sekitar 1 MiB; satu berkas sebelumnya disimpan dengan akhiran `.old`. Penulisan log melakukan sync ke disk.

## Cara Memakai API

Semua route memeriksa `api_key`. Handler saat ini tidak membatasi HTTP method secara konsisten, jadi `GET` maupun `POST` sama-sama diterima.

Aturan umum pengiriman parameter:

- Semua parameter di bawah adalah **query string**. Contoh di dokumen ini memakai `curl --get ... --data-urlencode 'nama=nilai'`, yang otomatis meng-encode spasi, tanda `+`, dan karakter khusus lain dengan benar.
- Beberapa handler (mis. `session_id` pada `/api/sendMessage`) juga menerima **form field** biasa (`application/x-www-form-urlencoded`).
- Response JSON selalu memakai `Content-Type: application/json`, kecuali unduhan attachment pada `/api/getAttachment`.

### Ringkasan Endpoint

| Method | Endpoint | Fungsi | Butuh `session_id` akun |
| --- | --- | --- | --- |
| GET/POST | `/api/auth` | Mulai atau lanjutkan login QR. | Tidak |
| GET/POST | `/api/auth/logout` | Batalkan proses login QR yang sedang berjalan. | Tidak (pakai `session_id` auth) |
| GET/POST | `/api/sendMessage` | Kirim teks atau file ke chat. | Ya |
| GET/POST | `/api/MyChat` | Daftar dialog (chat) akun. | Ya |
| GET/POST | `/api/joinChat` | Bergabung ke grup/channel. | Ya |
| GET/POST | `/api/readChat` | Baca riwayat pesan sebuah chat. | Ya |
| GET/POST | `/api/getAttachment` | Unduh media pesan sebagai file. | Ya |
| GET/POST | `/api/leaveChat` | Keluar dari grup/channel. | Ya |
| GET/POST | `/api/deleteChat` | (Kerangka) hapus chat. | Ya |
| GET/POST | `/api/status` | Statistik server. | Tidak |
| GET/POST | `/api/AccountList` | Daftar akun aktif. | Tidak |
| GET/POST | `/api/RemoveAccount` | Hapus akun dan berkas sesinya. | Tidak |

Contoh nilai yang dipakai di seluruh dokumen:

```text
BASE_URL   = http://localhost:3500
API_KEY    = YOUR_API_KEY
SESSION_ID = e37d573cb0f0a9e27862b1446ec7910a
```

### Format ID (gaya Bot API)

Seluruh endpoint memakai satu skema ID yang sama, baik saat menerima `chat_id`/`target_chat` maupun saat mengembalikan `id` di `/api/MyChat` dan `from.id` di `/api/readChat`:

| Tipe | Format | Contoh |
| --- | --- | --- |
| user (chat pribadi) | `<id>` (positif) | `1761729952` |
| grup biasa (chat) | `-<chat_id>` | `-123456` |
| channel / supergroup | `-100<channel_id>` | `-1001234567890` |

ID internal Telegram (`user.id`, `chat.id`, `channel.id`) selalu positif dan merupakan detail implementasi: wrapper mengonversi `-<id>` dan `-100<id>` ke nilai positif di dalam Go sebelum memanggil pustaka MTProto, dan sebaliknya saat menulis response. Jangan mengirim ID internal positif untuk grup/channel; pakai bentuk gaya Bot API di atas.

Karena skema Bot API membedakan channel lewat prefix `-100`, grup biasa yang `chat_id`-nya kebetulan diawali `100` (mis. `-1009999`) secara default terbaca sebagai channel. Kirim parameter `chat_type` (`group`/`chat`) untuk memaksanya. `chat_type` juga mempersempit resolusi ke satu tipe sehingga ID yang sama pada tipe berbeda tidak tertukar.

> Catatan resolusi: untuk ID numerik, target harus ada pada 100 dialog terbaru akun agar access hash Telegram dapat ditemukan. Username/tautan publik (`@nama`, `https://t.me/nama`) dapat di-resolve langsung tanpa batasan ini.

### Callback (Webhook)

Beberapa endpoint dapat mengirim hasil akhir ke **URL callback** yang Anda tentukan (parameter `callback`). Cara kerjanya:

- Server mengirim **HTTP `POST`** ke URL tersebut.
- Header `Content-Type: application/json`. **Body** request berisi payload JSON (bukan query string).
- Server menunggu paling lama **10 detik**; kegagalan dicatat di log dan **tidak dicoba ulang** (no retry).
- Endpoint callback **wajib membalas** dengan status **2xx** (mis. `204 No Content`). Status non-2xx dianggap gagal.
- URL callback hanya boleh menunjuk ke server yang Anda percaya.

Contoh endpoint yang memakai callback:

| Endpoint | Kapan callback dikirim | Bentuk payload |
| --- | --- | --- |
| `/api/auth` | Setelah login QR berhasil. | `{"ok", "session_id", "user_id"}` |
| `/api/sendMessage` | Setelah **setiap** pengiriman file selesai bila `callback` diisi (sukses/gagal), baik file kecil maupun besar. | `{"ok","chat_id","message_id"}` atau `{"ok","chat_id","error"}` |

Body callback sudah otomatis tersedia pada request yang masuk: di PHP pada `php://input`, di Node.js/Express pada `req.body` (setelah `express.json()`). Inti penanganannya cukup membaca body itu lalu membalas 2xx:

```php
<?php
// Contoh minimal endpoint callback.
$payload = json_decode(file_get_contents('php://input'), true);
// $payload = ["ok" => true, "chat_id" => "-100...", "message_id" => "74931"]
http_response_code(204);
```

```js
// Contoh minimal endpoint callback (Express).
app.post("/telegram/upload", (req, res) => {
  console.log(req.body); // { ok: true, chat_id: "-100...", message_id: "74931" }
  res.sendStatus(204);
});
```

URL callback hanya boleh menunjuk ke server yang Anda percaya.

## API

### Autentikasi

#### `GET` atau `POST /api/auth`

Tanpa `session_id`, memulai sesi login QR baru. Dengan `session_id`, mengambil data sesi auth yang masih aktif (untuk dipanggil ulang / polling QR), bukan membuat sesi baru.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Tidak | Jika diberikan, mengambil data sesi auth yang masih aktif, bukan membuat sesi baru. |
| `pass` | Tidak | Password verifikasi dua langkah Telegram. Dipakai hanya jika Telegram memintanya setelah QR dipindai. Sertakan saat memulai sesi baru. |
| `callback` | Tidak | URL webhook yang menerima POST JSON setelah login berhasil. Hanya berlaku saat memulai sesi baru. |

**Contoh memulai login baru:**

```sh
curl --get 'http://localhost:3500/api/auth' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'pass=YOUR_TELEGRAM_2FA_PASSWORD' \
  --data-urlencode 'callback=https://example.com/telegram/auth'
```

**Contoh response (HTTP 200):**

```json
{
  "ok": true,
  "session_id": "e37d573cb0f0a9e27862b1446ec7910a",
  "expired": "2026/10/09 21:33:04",
  "qrcode": "tg://login?token=AQAAAA..."
}
```

- `expired` berformat `YYYY/MM/DD HH:MM:SS`.
- `qrcode` adalah URL login Telegram (awalan `tg://login?token=`). Render nilai ini menjadi gambar QR di aplikasi klien Anda, lalu pindai dengan aplikasi Telegram. Server menunggu hingga 5 detik agar QR tersedia sebelum merespons; jika Telegram belum mengirimkannya dalam batas waktu itu, `qrcode` masih dapat kosong — panggil ulang endpoint ini dengan `session_id` yang sama untuk mendapatkannya.
- Login berlangsung di background karena menunggu pemindaian QR, sehingga response di atas keluar **sebelum** QR benar-benar dipindai.

**Contoh mengambil sesi yang sama lagi (polling QR):**

```sh
curl --get 'http://localhost:3500/api/auth' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=e37d573cb0f0a9e27862b1446ec7910a'
```

Response-nya berbentuk sama seperti di atas. Jika sesi sudah tidak aktif/kedaluwarsa, server membalas **HTTP 404**:

```json
{ "ok": false, "error": "session not found or expired" }
```

**Contoh response saat `api_key` salah (HTTP 401):**

```json
{ "ok": false, "error": "invalid api_key" }
```

**Callback setelah login berhasil.** Jika `callback` diberikan saat memulai sesi, setelah login berhasil dan sesi tersimpan, server mengirim POST JSON berikut (tanpa string sesi):

```json
{ "ok": true, "session_id": "e37d573cb0f0a9e27862b1446ec7910a", "user_id": "2061595198" }
```

Callback tidak menahan response awal yang berisi QR.

**Catatan 2FA.** Jika password tidak disertakan padahal akun memerlukan verifikasi dua langkah, login gagal dan sesi harus dimulai ulang dengan parameter `pass`. Password disimpan sementara di memori sesi dan hanya diberikan ke client Telegram jika Telegram mengembalikan `SESSION_PASSWORD_NEEDED`; setelah digunakan, nilai tersebut langsung dihapus. Karena `pass` berada di query string, jangan memakai URL ini melalui layanan/proxy yang menyimpan access log.

#### `GET` atau `POST /api/auth/logout`

Membatalkan proses autentikasi yang masih berjalan. Ini bukan endpoint logout untuk akun Telegram yang sudah login (untuk itu, gunakan `/api/RemoveAccount`).

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | ID sesi auth yang akan dibatalkan; diterima dari query atau form. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/auth/logout' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=e37d573cb0f0a9e27862b1446ec7910a'
```

**Contoh response (HTTP 200):**

```json
{ "ok": true }
```

Jika `session_id` tidak diberikan (HTTP 400):

```json
{ "ok": false, "error": "session_id required" }
```

### Pesan dan chat

Endpoint di bawah memvalidasi bahwa `session_id` mengacu pada akun yang sedang aktif. `/api/sendMessage` dapat mengirim file lokal ke Telegram; endpoint pengelolaan chat lainnya masih berupa kerangka/simulasi bila disebutkan.

#### `GET` atau `POST /api/sendMessage`

Mengirim teks atau file ke sebuah chat.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif yang akan digunakan. Dapat berasal dari query atau form. |
| `chat_id` | Ya | Username/tautan Telegram (mis. `@nama` atau `https://t.me/nama`) atau ID gaya Bot API yang ada di 100 dialog terbaru akun (lihat [Format ID](#format-id-gaya-bot-api)). |
| `chat_type` | Tidak | Opsional untuk ID numerik: `user`/`private`, `group`/`chat`, atau `channel`/`supergroup`. Memaksa tipe peer dan memecah ambiguitas prefix `-100`. |
| `message` | Bersyarat | Wajib bila `is_media` **tidak** diisi (mengirim teks). Bila `is_media` diisi, teks ini menjadi caption file. |
| `is_media` | Tidak | Setel `true` untuk mengaktifkan pengiriman file. Bila aktif, `media_source` menjadi wajib. Nilai `false`/`0`/kosong berarti mengirim teks biasa. |
| `media_source` | Bersyarat | Wajib bila `is_media=true`. Path file lokal yang dapat dibaca proses wrapper (URL belum didukung). Cara pengirimannya ditentukan `media_info`. |
| `media_info` | Tidak | Menentukan **cara file dikirim**. Nilai sederhana: `document` (bawaan), `photo`, `video`, `voice`, `audio`, `animation`/`gif`. Boleh juga diisi MIME type (mis. `video/mp4`) dan server menyimpulkan jenisnya. Bila kosong, dikirim sebagai dokumen dengan MIME dari ekstensi file. |
| `reply_to_message_id` | Tidak | ID positif pesan di chat yang sama; pesan baru dikirim sebagai balasan ke pesan tersebut, termasuk untuk caption media. |
| `callback` | Tidak | URL webhook penerima hasil pengiriman via POST JSON. Dikirim untuk **semua** pengiriman file bila diisi (file kecil maupun besar). |
| `topic_id` | Tidak | ID topik forum (integer positif). Dipakai bila mengirim ke grup yang memakai topik, agar pesan masuk ke topik yang dituju dan bukan "General". |

**Contoh mengirim teks biasa:**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=1761729952' \
  --data-urlencode 'message=Halo, apa kabar?'
```

**Contoh response (HTTP 200) — teks:**

```json
{ "ok": true, "chat_id": "1761729952", "message_id": 74928 }
```

`message_id` di sini adalah ID pesan aktual dari Telegram (bilangan bulat).

**Contoh mengirim sebagai balasan (reply):**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=1761729952' \
  --data-urlencode 'message=Setuju!' \
  --data-urlencode 'reply_to_message_id=74928'
```

**Contoh mengirim ke topik forum tertentu:**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=-1001234567890' \
  --data-urlencode 'topic_id=15' \
  --data-urlencode 'message=Halo dari topik 15'
```

Bila chat bukan grup ber-topik, jangan kirim `topic_id`. Bila `topic_id` diisi tetapi bukan angka positif, server membalas `topic_id must be a positive integer`.

**Contoh mengirim file kecil (≤ 1 MiB):**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=-1001234567890' \
  --data-urlencode 'is_media=true' \
  --data-urlencode 'media_source=/path/lokal/laporan.pdf' \
  --data-urlencode 'media_info=application/pdf' \
  --data-urlencode 'message=Berikut laporannya'
```

File berukuran sampai 1 MiB dikirim **sebelum** response HTTP diberikan. Progress unggah dicatat di `go-running.log` setiap kelipatan 10% (mis. `Upload files.mp4: 40% (33554432/86101289 bytes)`).

**Contoh mengirim video (dikirim sebagai video, bukan dokumen):**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=-1001234567890' \
  --data-urlencode 'is_media=true' \
  --data-urlencode 'media_source=/path/lokal/files.mp4' \
  --data-urlencode 'media_info=video' \
  --data-urlencode 'message=Video percobaan'
```

**Contoh response (HTTP 200) — file kecil:**

```json
{ "ok": true, "chat_id": "-1001234567890", "message_id": "74930" }
```

> Perhatikan: untuk pengiriman media, `message_id` bertipe **string** (`"74930"`), bukan angka. Ini berbeda dari pengiriman teks biasa.

**Contoh mengirim file besar (> 1 MiB) — diproses di background:**

```sh
curl --get 'http://localhost:3500/api/sendMessage' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=-1001234567890' \
  --data-urlencode 'is_media=true' \
  --data-urlencode 'media_source=/path/lokal/arsip.zip' \
  --data-urlencode 'message=Terlampir' \
  --data-urlencode 'callback=https://example.com/telegram/upload'
```

Untuk file > 1 MiB, handler **segera** membalas dengan **HTTP 202** dan mengunggah/mengirim file di background:

```json
{ "ok": true, "chat_id": "-1001234567890", "message_id": "PENDING" }
```

Setelah Telegram memberi hasil, server mengirim callback (bila `callback` diisi). Keberhasilan/kegagalan pengiriman callback juga dicatat di `go-running.log` (`Callback: ...`). **Sukses:**

```json
{ "ok": true, "chat_id": "-1001234567890", "message_id": "74931" }
```

**Gagal:**

```json
{ "ok": false, "chat_id": "-1001234567890", "error": "telegram upload/send failed: ..." }
```

Upload diberi batas waktu 30 menit. Kegagalan pengiriman callback dicatat di log dan tidak dicoba ulang.

**Response error:**

| HTTP | Body | Sebab |
| --- | --- | --- |
| 400 | `{ "ok": false, "error": "message is required when is_media is not set" }` | `message` kosong dan `is_media` tidak diisi. |
| 400 | `{ "ok": false, "error": "media_source is required" }` | `is_media` diisi tetapi `media_source` kosong. |
| 400 | `{ "ok": false, "error": "media_source must point to a readable local file" }` | File `media_source` tidak ada/tidak terbaca. |
| 400 | `{ "ok": false, "error": "media_info \"...\" tidak dikenal; isi document, photo, video, voice, audio, animation, atau sebuah MIME type" }` | `media_info` bukan jenis/MIME yang dikenal. |
| 400 | `{ "ok": false, "error": "reply_to_message_id must be a positive integer" }` | `reply_to_message_id` bukan angka positif. |
| 400 | `{ "ok": false, "error": "topic_id must be a positive integer" }` | `topic_id` bukan angka positif. |
| 404 | `{ "ok": false, "error": "session not found" }` | `session_id` tidak dikenal. |
| 502 | `{ "ok": false, "chat_id": "...", "error": "..." }` | Resolusi `chat_id` atau pengiriman ke Telegram gagal. |

#### `GET` atau `POST /api/MyChat`

Mengambil daftar dialog Telegram aktual akun.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif. |
| `page` | Tidak | Nomor halaman. Nilai tidak valid atau < 1 dianggap `1`. |
| `max_result` | Tidak | Jumlah per halaman. Bawaan `10`, maksimum `100`. |
| `type` | Tidak | Filter: `user`/`private`, `group`/`chat`, `channel`, `supergroup`, atau `all`. |

Data yang diambil dibatasi 100 dialog pertama; `description` dan `topic_list` tidak disertakan karena butuh pemanggilan Telegram tambahan.

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/MyChat' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'page=1' \
  --data-urlencode 'max_result=10' \
  --data-urlencode 'type=all'
```

**Contoh response (HTTP 200) — berupa array JSON:**

```json
[
  {
    "id": "1761729952",
    "name": "Rian Note",
    "type": "user",
    "unread_count": 2,
    "last_message": "latest"
  },
  {
    "id": "-123456",
    "name": "Grup Biasa",
    "type": "group",
    "unread_count": 0,
    "last_message": "halo semua"
  },
  {
    "id": "-1001234567890",
    "name": "Broadcast",
    "type": "channel",
    "unread_count": 5,
    "last_message": "pengumuman"
  }
]
```

Field `id` mengikuti [Format ID](#format-id-gaya-bot-api) (negatif untuk grup/channel). Jika halaman melewati jumlah data, response-nya array kosong:

```json
[]
```

**Contoh response error** — filter `type` tidak dikenal (HTTP 400):

```json
{ "ok": false, "error": "unsupported type filter \"foo\"; use user, group, channel, or all" }
```

#### `GET` atau `POST /api/joinChat`

Akun benar-benar bergabung ke grup/channel Telegram.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif. |
| `target_chat` | Ya | Username publik (`@nama` atau `nama`), URL publik (`https://t.me/nama`), ID gaya Bot API channel/supergroup yang ada di dialog akun, atau tautan undangan privat (`https://t.me/+HASH`, `https://t.me/joinchat/HASH`). Diterima dari query atau form. |

**Contoh — tautan undangan privat:**

```sh
curl --get 'http://localhost:3500/api/joinChat' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'target_chat=https://t.me/+INVITE_HASH'
```

**Contoh response — berhasil (HTTP 200):**

```json
{ "ok": true, "status": "success" }
```

**Contoh response — perlu persetujuan admin (HTTP 200):**

```json
{ "ok": true, "status": "pending" }
```

**Contoh response — gagal (HTTP 502):**

```json
{ "ok": false, "status": "failed", "description": "Invite link is invalid" }
```

Deskripsi gagal menerjemahkan beberapa error Telegram, mis. `Invite link has expired`, `Channel is private; use a valid invite link`, atau `Telegram account has reached its channel/group limit`. Jika akun sudah menjadi anggota, hasilnya `success`.

Jika `target_chat` kosong (HTTP 400):

```json
{ "ok": false, "status": "failed", "description": "target_chat is required" }
```

> Tanda `+` pada URL invite dipertahankan oleh handler. Jika menyusun URL manual di Postman, Anda juga dapat mengirimnya sebagai `%2B` (`https://t.me/%2BHASH`) atau memakai Params/URL encoding Postman.

#### `GET` atau `POST /api/readChat`

Membaca riwayat pesan sebuah chat.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif. |
| `chat_id` | Ya | Format sama seperti `/api/sendMessage` (lihat [Format ID](#format-id-gaya-bot-api)). |
| `chat_type` | Tidak | Memaksa tipe peer untuk ID numerik: `user`/`private`, `group`/`chat`, `channel`/`supergroup`. |
| `message_id` | Tidak | Offset Telegram: mulai membaca dari pesan ini. |
| `max_result` | Tidak | Jumlah pesan. Bawaan `10`, maksimum `100`. |
| `page` | Tidak | Nomor halaman. Nilai < 1 dianggap `1`. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/readChat' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=1761729952' \
  --data-urlencode 'max_result=10' \
  --data-urlencode 'page=1'
```

**Contoh response (HTTP 200):**

```json
{
  "chat_id": "1761729952",
  "page": 1,
  "message_id": "",
  "result": [
    {
      "from": {
        "id": "1761729952",
        "name": "Chat Partner",
        "username": "partner",
        "type": "user"
      },
      "message": { "text": "Halo", "type": "message" },
      "date": 1710000000,
      "outgoing": false
    },
    {
      "from": { "id": "-123456", "name": "Grup Biasa", "type": "group" },
      "message": {
        "text": "caption foto",
        "type": "photo",
        "file_id": "eyJjIjoiMTc2MTcyOTk1MiIsIm0iOjg4fQ"
      },
      "date": 1710000100,
      "outgoing": true
    }
  ]
}
```

Keterangan field:

- `from.id` mengikuti [Format ID](#format-id-gaya-bot-api) (negatif untuk grup/channel); `from.username` hanya ada bila tersedia; `from.type` bernilai `user`, `group`, `channel`, atau `supergroup`.
- Untuk private chat, jika Telegram tidak menyertakan `FromID`, pengirim diturunkan dari ID lawan bicara atau profil akun sendiri untuk pesan keluar.
- `message.text` berisi teks (caption untuk media). `message.type` bernilai `message`, `photo`, `video`, `audio`, `document`, `animation`, `sticker`, atau `service`.
- Media yang bisa diunduh juga memiliki `message.file_id` (token untuk `/api/getAttachment`).
- Pesan service memakai `message.type:"service"` dan field `message` hanya berisi `{ "text": "", "type": "service" }`.
- `date` adalah unix timestamp pesan, `outgoing` menandai pesan keluar.

Jika belum ada pesan atau offset tidak menghasilkan data, `result` berupa array kosong:

```json
{ "chat_id": "1761729952", "page": 1, "message_id": "", "result": [] }
```

#### `GET` atau `POST /api/getAttachment`

Mengunduh media pesan sebagai file. Endpoint mengambil pesan dan media kembali dari Telegram, lalu men-stream file aktual.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif yang dapat membaca pesan tersebut. |
| `file_id` | Ya | Token opaque dari `message.file_id` di `/api/readChat`. Token ini merujuk ke chat dan message Telegram, bukan akses publik — harus dipakai bersama `session_id` akun yang berhak. |
| `chat_type` | Tidak | Memaksa tipe peer untuk ID numerik. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/getAttachment' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'file_id=eyJjIjoiMTc2MTcyOTk1MiIsIm0iOjg4fQ' \
  --output hasil.jpg
```

**Contoh response — sukses (file biner):**

```http
HTTP/1.1 200 OK
Content-Type: image/jpeg
Content-Disposition: attachment; filename="photo_1234.jpg"

<isi file biner>
```

**Contoh response — error (JSON):**

```json
{ "ok": false, "error": "invalid file_id" }
```

Kemungkinan error lain: `Telegram message not found`, `message has no downloadable media`, `photo is unavailable`, atau kegagalan unduh dari Telegram. File media yang sudah dihapus, tidak dapat diakses, atau file reference Telegram yang sudah tidak berlaku juga menghasilkan error.

#### `GET` atau `POST /api/leaveChat`

Mengeluarkan akun dari grup biasa, supergroup, atau channel.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif. |
| `target_chat` | Ya | Format sama seperti `/api/joinChat`: username/URL publik, ID gaya Bot API pada dialog akun, atau invite privat yang akunnya sudah menjadi anggota. Diterima dari query atau form. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/leaveChat' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'target_chat=https://t.me/channelname'
```

**Contoh response — sukses (HTTP 200):**

```json
{ "ok": true, "status": "success" }
```

**Contoh response — gagal (HTTP 502):**

```json
{ "ok": false, "status": "failed", "description": "target_chat must be a group, supergroup, or channel; users cannot be left" }
```

Jika target tidak ditemukan, akun bukan anggota, atau Telegram menolak operasi, `description` memuat alasannya. Jika `target_chat` kosong (HTTP 400):

```json
{ "ok": false, "status": "failed", "description": "target_chat is required" }
```

#### `GET` atau `POST /api/deleteChat`

Endpoint berbeda dari leave. **Implementasi saat ini masih kerangka:** handler hanya memvalidasi `session_id` dan mengembalikan `ok:true`; operasi hapus chat Telegram belum diimplementasikan.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | Akun aktif. |
| `chat_id` | Ya | ID chat gaya Bot API (belum diproses lebih lanjut). |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/deleteChat' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=SESSION_ID' \
  --data-urlencode 'chat_id=1761729952'
```

**Contoh response (HTTP 200):**

```json
{ "ok": true }
```

Jika `session_id` tidak dikenal (HTTP 404):

```json
{ "ok": false, "error": "session not found" }
```

### Administrasi akun

#### `GET` atau `POST /api/status`

Statistik server. Hanya memerlukan `api_key`.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/status' \
  --data-urlencode 'api_key=YOUR_API_KEY'
```

**Contoh response (HTTP 200):**

```json
{ "total_accounts": 1, "active_auth": 0, "active_account": 1 }
```

Pada implementasi sekarang, `total_accounts` dan `active_account` sama-sama menghitung akun yang sedang tercatat di memori, bukan jumlah semua file sesi di disk. `active_auth` adalah jumlah sesi login QR yang sedang berjalan.

#### `GET` atau `POST /api/AccountList`

Daftar akun aktif di memori. Hanya memerlukan `api_key`.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/AccountList' \
  --data-urlencode 'api_key=YOUR_API_KEY'
```

**Contoh response (HTTP 200):**

```json
{
  "ok": true,
  "data": [
    {
      "session_id": "e37d573cb0f0a9e27862b1446ec7910a",
      "name": "Reza",
      "id": "2061595198"
    }
  ]
}
```

`name` diambil dari first/last name Telegram (atau username jika nama kosong), disimpan saat login, dan dipulihkan saat restart. Sesi lama tanpa nama akan mengisi nama setelah koneksi Telegram tersambung. `id` adalah `user_id` Telegram.

#### `GET` atau `POST /api/RemoveAccount`

Menghentikan akun dan menghapus berkas sesi dari `database/`. Berbeda dengan endpoint ini, shutdown server melalui Ctrl+C hanya membatalkan koneksi dan mempertahankan berkas sesi.

| Parameter | Wajib | Keterangan |
| --- | --- | --- |
| `api_key` | Ya | API key server. |
| `session_id` | Ya | ID sesi akun yang akan dihapus; diterima dari query atau form. |

**Contoh:**

```sh
curl --get 'http://localhost:3500/api/RemoveAccount' \
  --data-urlencode 'api_key=YOUR_API_KEY' \
  --data-urlencode 'session_id=e37d573cb0f0a9e27862b1446ec7910a'
```

**Contoh response (HTTP 200):**

```json
{ "ok": true }
```

Jika `session_id` tidak diberikan (HTTP 400):

```json
{ "ok": false, "error": "session_id required" }
```

## Menjalankan Program

Persyaratan: Go 1.27 atau lebih baru. Versi minimum proyek dideklarasikan di `go.mod`; periksa versi yang terpasang dengan `go version`. Pastikan dependensi modul telah diunduh. `--app-id` dan `--app-hash` bisa didapatkan dari [my.telegram.org](https://my.telegram.org). Ganti nilai contoh dengan kredensial dan API key milik sendiri.

Jalankan langsung dari source:

```sh
go run main.go --port="3500" --run-dir="./" --api-key="YOUR_API_KEY" --app-id="123456" --app-hash="YOUR_APP_HASH"
```

Atau build binary:

```sh
go build -o belajar-go main.go
./belajar-go --port="3500" --run-dir="./" --api-key="YOUR_API_KEY" --app-id="123456" --app-hash="YOUR_APP_HASH"
```

### Uji Cepat (Smoke Test)

Setelah server berjalan, cek status dan daftar akun:

```sh
curl --get 'http://localhost:3500/api/status' \
  --data-urlencode 'api_key=YOUR_API_KEY'
```

```sh
curl --get 'http://localhost:3500/api/AccountList' \
  --data-urlencode 'api_key=YOUR_API_KEY'
```

Jika akun sudah terdaftar di `data`, pakai `session_id` dari response tersebut untuk endpoint pesan/chat.
