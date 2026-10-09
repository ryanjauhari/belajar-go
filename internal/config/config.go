// File: internal/config/config.go
// Bertanggung jawab untuk parsing CLI flags.
// Contoh eksekusi:
//   go run main.go --port="3500" --run-dir="./" --api-key="xxxxx" \
//                  --app-id="123456" --app-hash="abcdef0123456789"

package config

import (
    "flag"
    "fmt"
    "os"
    "path/filepath"
    "strconv"
)

// Config menyimpan semua konfigurasi runtime.
type Config struct {
    Port    string // Port HTTP server
    RunDir  string // Direktori kerja (log & database)
    APIKey  string // API key untuk autentikasi REST API kita

    // AppID dan AppHash WAJIB untuk login ke Telegram MTProto.
    // Didapat dari https://my.telegram.org
    AppID   int    // Telegram API ID (integer)
    AppHash string // Telegram API hash (string 32 char)
}

// Load membaca flags dari command-line.
func Load() *Config {
    port := flag.String("port", "3500", "Port untuk HTTP server")
    runDir := flag.String("run-dir", "./", "Direktori kerja (log & database)")
    apiKey := flag.String("api-key", "", "API key untuk autentikasi REST API")

    // Flag baru: app-id dan app-hash (WAJIB untuk MTProto).
    appIDStr := flag.String("app-id", "", "Telegram API ID (dari my.telegram.org)")
    appHash := flag.String("app-hash", "", "Telegram API Hash (dari my.telegram.org)")

    flag.Parse()

    // Validasi: app-id dan app-hash wajib.
    if *appIDStr == "" || *appHash == "" {
        fmt.Println("Error: --app-id dan --app-hash wajib diisi.")
        fmt.Println("Dapatkan dari https://my.telegram.org")
        flag.Usage()
        os.Exit(1)
    }

    // Konversi app-id dari string ke int.
    appID, err := strconv.Atoi(*appIDStr)
    if err != nil || appID <= 0 {
        fmt.Println("Error: --app-id harus berupa angka positif.")
        os.Exit(1)
    }

    // Normalisasi run-dir.
    absDir, err := filepath.Abs(*runDir)
    if err != nil {
        absDir = *runDir
    }

    return &Config{
        Port:    *port,
        RunDir:  absDir,
        APIKey:  *apiKey,
        AppID:   appID,
        AppHash: *appHash,
    }
}