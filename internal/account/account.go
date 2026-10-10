// File: internal/account/account.go
// Satu akun = satu goroutine yang menjaga koneksi MTProto.
// Menggunakan app_id + app_hash + session string untuk reconnect.

package account

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"

	"belajar-go/internal/config"
	"belajar-go/internal/logger"
	"belajar-go/internal/storage"
)

type Account struct {
	SessionID string
	UserID    string
	Name      string
	Cancel    context.CancelFunc
	Client    *telegram.Client // client Telegram, dipakai untuk operasi lain
}

type Manager struct {
	mu       sync.RWMutex
	accounts map[string]*Account
	log      *logger.Logger
	store    *storage.Storage
	cfg      *config.Config
}

func NewManager(cfg *config.Config, log *logger.Logger, store *storage.Storage) *Manager {
	return &Manager{
		accounts: make(map[string]*Account),
		log:      log,
		store:    store,
		cfg:      cfg,
	}
}

// StartAccount menjalankan goroutine untuk satu akun.
func (m *Manager) StartAccount(sessionID, sessionString, userID, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.accounts[sessionID]; exists {
		m.log.Info("Account %s sudah berjalan", sessionID)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Buat session storage dari session string.
	// Di sini kita asumsikan sessionString adalah hex dari bytes session.
	sessBytes, err := hexDecode(sessionString)
	if err != nil {
		m.log.Error("Account %s: session string invalid: %v", sessionID, err)
		cancel()
		return
	}

	// Simpan session bytes ke storage file sementara (gotd/td butuh storage).
	// Alternatif: implementasi custom session.Storage.
	memStore := &session.StorageMemory{}
	if err := memStore.StoreSession(ctx, sessBytes); err != nil {
		m.log.Error("Account %s: gagal menyiapkan session storage: %v", sessionID, err)
		cancel()
		return
	}

	// Buat client.
	client := telegram.NewClient(m.cfg.AppID, m.cfg.AppHash, telegram.Options{
		SessionStorage: memStore,
	})

	acc := &Account{
		SessionID: sessionID,
		UserID:    userID,
		Name:      name,
		Cancel:    cancel,
		Client:    client,
	}
	m.accounts[sessionID] = acc

	go m.runAccountGoroutine(ctx, acc)
	m.log.Info("Account %s (%s) dimulai", sessionID, name)
}

// runAccountGoroutine menjalankan client Telegram dan menjaga koneksi.
func (m *Manager) runAccountGoroutine(ctx context.Context, acc *Account) {
	// Client.Run akan otomatis reconnect jika koneksi putus.
	// Ini yang membuat koneksi MTProto tetap hidup.
	err := acc.Client.Run(ctx, func(ctx context.Context) error {
		// Di sini client sudah terkoneksi.
		// Kita bisa setup handler untuk update, dsb.
		m.log.Info("Account %s: terkoneksi ke MTProto", acc.SessionID)
		if acc.Name == "" {
			if self, err := acc.Client.Self(ctx); err == nil {
				name := strings.TrimSpace(self.FirstName + " " + self.LastName)
				if name == "" {
					name = self.Username
				}
				if name != "" {
					m.mu.Lock()
					acc.Name = name
					m.mu.Unlock()
					if data, err := m.store.Load(acc.SessionID); err == nil {
						data.Name = name
						if err := m.store.Save(data); err != nil {
							m.log.Error("Account %s: gagal menyimpan nama: %v", acc.SessionID, err)
						}
					}
				}
			}
		}

		// Blok sampai context dibatalkan.
		<-ctx.Done()
		return nil
	})

	if err != nil {
		m.log.Error("Account %s: client error: %v", acc.SessionID, err)

		// Jika error fatal (session invalid), hapus session.
		if isSessionInvalid(err) {
			m.log.Info("Account %s: session invalid, menghapus", acc.SessionID)
			_ = m.store.Delete(acc.SessionID)
		}
	}

	// Cleanup dari map.
	m.mu.Lock()
	delete(m.accounts, acc.SessionID)
	m.mu.Unlock()
	m.log.Info("Account %s: goroutine berhenti", acc.SessionID)
}

// Get mengembalikan Account berdasarkan session_id.
func (m *Manager) Get(sessionID string) (*Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[sessionID]
	return acc, ok
}

// StopAccount menghentikan akun dan hapus session (logout).
func (m *Manager) StopAccount(sessionID string) error {
	m.mu.Lock()
	acc, ok := m.accounts[sessionID]
	if ok {
		delete(m.accounts, sessionID)
	}
	m.mu.Unlock()

	if ok {
		// Logout dari Telegram (opsional tapi disarankan).
		// Di implementasi nyata: panggil client.API().AuthLogOut(ctx).
		acc.Cancel()
		m.log.Info("Account %s dihentikan", sessionID)
	}

	// Hapus file session.
	return m.store.Delete(sessionID)
}

// Shutdown menghentikan semua koneksi aktif tanpa menghapus file sesi.
func (m *Manager) Shutdown() {
	m.mu.RLock()
	accounts := make([]*Account, 0, len(m.accounts))
	for _, acc := range m.accounts {
		accounts = append(accounts, acc)
	}
	m.mu.RUnlock()

	for _, acc := range accounts {
		acc.Cancel()
		m.log.Info("Account %s dihentikan; session tetap disimpan", acc.SessionID)
	}
}

func (m *Manager) List() []*Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Account, 0, len(m.accounts))
	for _, acc := range m.accounts {
		copy := *acc
		result = append(result, &copy)
	}
	return result
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.accounts)
}

// LoadAll memuat semua session dari storage saat startup.
func (m *Manager) LoadAll() error {
	sessions, err := m.store.ListAll()
	if err != nil {
		return fmt.Errorf("gagal list session: %w", err)
	}
	for _, s := range sessions {
		m.StartAccount(s.SessionID, s.Session, s.UserID, s.Name)
	}
	m.log.Info("Memuat %d akun dari storage", len(sessions))
	return nil
}

// Helper: decode hex string ke bytes.
func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// Helper: cek apakah error menandakan session invalid.
func isSessionInvalid(err error) bool {
	// Implementasi nyata: cek error code dari Telegram.
	// Contoh: AUTH_KEY_UNREGISTERED, SESSION_REVOKED, dll.
	return false
}
