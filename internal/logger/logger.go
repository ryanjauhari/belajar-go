// File: internal/logger/logger.go
// Logger custom untuk menulis log ke file dengan rotasi otomatis.
// Syarat dari user: max file log 1 MB, ada go-error.log dan go-running.log.
// Kita juga harus bisa flush agar tidak ada data yang hilang saat long-running.

package logger

import (
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"
)

// maxLogSize adalah batas ukuran file log dalam byte (1 MB).
const maxLogSize = 1 * 1024 * 1024

// Logger menangani penulisan dua file log: running dan error.
// Menggunakan mutex agar aman dipakai dari banyak goroutine.
type Logger struct {
    mu        sync.Mutex // melindungi akses ke file
    dir       string     // direktori kerja
    runFile   *os.File   // file go-running.log
    errFile   *os.File   // file go-error.log
}

// New membuat Logger baru di direktori runDir.
// Akan membuka/membuat file log.
func New(runDir string) (*Logger, error) {
    // Pastikan direktori ada.
    if err := os.MkdirAll(runDir, 0755); err != nil {
        return nil, err
    }

    // Buka (atau buat) file log running.
    runPath := filepath.Join(runDir, "go-running.log")
    runFile, err := os.OpenFile(runPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
    if err != nil {
        return nil, err
    }

    // Buka (atau buat) file log error.
    errPath := filepath.Join(runDir, "go-error.log")
    errFile, err := os.OpenFile(errPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
    if err != nil {
        // Tutup file running jika errFile gagal.
        runFile.Close()
        return nil, err
    }

    return &Logger{dir: runDir, runFile: runFile, errFile: errFile}, nil
}

// rotateIfNeeded memutar file log jika sudah melewati maxLogSize.
// Dipanggil sebelum menulis log baru.
func (l *Logger) rotateIfNeeded(f *os.File, path string) {
    info, err := f.Stat()
    if err != nil {
        return
    }
    if info.Size() < maxLogSize {
        return // belum perlu rotasi
    }

    // Tutup file lama, rename jadi .old, buka file baru.
    f.Close()
    _ = os.Rename(path, path+".old")
    newF, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)

    // Ganti pointer di struct (caller bertanggung jawab memastikan ini aman).
    if path == filepath.Join(l.dir, "go-running.log") {
        l.runFile = newF
    } else {
        l.errFile = newF
    }
}

// Info menulis log informasi umum (ke go-running.log).
func (l *Logger) Info(format string, args ...interface{}) {
    l.mu.Lock()
    defer l.mu.Unlock()

    path := filepath.Join(l.dir, "go-running.log")
    l.rotateIfNeeded(l.runFile, path)

    // Format pesan dengan timestamp.
    msg := fmt.Sprintf("[%s] INFO: %s\n", time.Now().Format("2006/01/02 15:04:05"), fmt.Sprintf(format, args...))
    l.runFile.WriteString(msg)
    // Flush ke disk supaya log tidak hilang.
    l.runFile.Sync()
}

// Error menulis log error (ke go-error.log dan juga running.log).
func (l *Logger) Error(format string, args ...interface{}) {
    l.mu.Lock()
    defer l.mu.Unlock()

    runPath := filepath.Join(l.dir, "go-running.log")
    errPath := filepath.Join(l.dir, "go-error.log")

    l.rotateIfNeeded(l.runFile, runPath)
    l.rotateIfNeeded(l.errFile, errPath)

    msg := fmt.Sprintf("[%s] ERROR: %s\n", time.Now().Format("2006/01/02 15:04:05"), fmt.Sprintf(format, args...))
    l.errFile.WriteString(msg)
    l.errFile.Sync()

    // Error juga dicatat ke running log supaya konteks tidak hilang.
    l.runFile.WriteString(msg)
    l.runFile.Sync()
}

// Close menutup semua file log. Dipanggil saat shutdown.
func (l *Logger) Close() {
    l.mu.Lock()
    defer l.mu.Unlock()
    if l.runFile != nil {
        l.runFile.Sync()
        l.runFile.Close()
    }
    if l.errFile != nil {
        l.errFile.Sync()
        l.errFile.Close()
    }
}