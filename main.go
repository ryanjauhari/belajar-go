// File: main.go
// Entry point. Sekarang butuh --app-id dan --app-hash.

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"belajar-go/internal/account"
	"belajar-go/internal/api"
	"belajar-go/internal/auth"
	"belajar-go/internal/config"
	"belajar-go/internal/logger"
	"belajar-go/internal/storage"
)

func main() {
	// 1. Parsing config (sekarang termasuk app-id & app-hash).
	cfg := config.Load()

	if cfg.APIKey == "" {
		fmt.Println("Error: --api-key wajib diisi")
		os.Exit(1)
	}

	// 2. Logger.
	log, err := logger.New(cfg.RunDir)
	if err != nil {
		fmt.Printf("Gagal membuat logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.Info("=== Telegram Wrapper dimulai ===")
	log.Info("Port: %s | RunDir: %s | AppID: %d", cfg.Port, cfg.RunDir, cfg.AppID)

	// 3. Storage.
	store, err := storage.New(cfg.RunDir)
	if err != nil {
		log.Error("Gagal membuat storage: %v", err)
		os.Exit(1)
	}

	// 4. Account manager (butuh cfg untuk app_id/app_hash).
	accMgr := account.NewManager(cfg, log, store)

	// 5. Auth manager (butuh cfg untuk app_id/app_hash).
	authMgr := auth.NewManager(cfg, log, store, func(sessionID, sessionString, userID string) {
		// Session sudah disimpan oleh auth manager; di sini hanya jalankan account.
		accMgr.StartAccount(sessionID, sessionString, userID, "")
	})

	// 6. Muat akun yang sudah ada.
	if err := accMgr.LoadAll(); err != nil {
		log.Error("Gagal memuat akun: %v", err)
	}

	// 7. HTTP server.
	handler := api.NewHandler(cfg, log, authMgr, accMgr)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	go func() {
		log.Info("HTTP server listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error: %v", err)
		}
	}()

	// 8. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutdown signal diterima...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	for _, acc := range accMgr.List() {
		_ = accMgr.StopAccount(acc.SessionID)
	}

	log.Info("=== Telegram Wrapper berhenti ===")
}
