// File: internal/auth/auth.go
// Menangani autentikasi via QR ke Telegram MTProto.
// Menggunakan library github.com/gotd/td.
//
// Alur:
//   1. Buat client Telegram dengan app_id + app_hash.
//   2. Minta QR login token dari Telegram.
//   3. Kirim QR string ke client (sebagai base64/URL).
//   4. Tunggu user scan. Jika sukses, dapat session string.
//   5. Simpan session, jalankan account goroutine, hentikan goroutine ini.
//   6. Jika expired, hentikan goroutine ini.

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	telegramauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"belajar-go/internal/config"
	"belajar-go/internal/logger"
	"belajar-go/internal/storage"
	"belajar-go/internal/webhook"
)

// AuthSession merepresentasikan satu proses autentikasi aktif.
type AuthSession struct {
	SessionID   string
	ExpiredAt   time.Time
	QRCode      string             // URL QR (tg://login?token=...)
	Cancel      context.CancelFunc // untuk menghentikan goroutine
	qrReady     chan struct{}
	password    string
	callbackURL string
}

// Manager mengelola semua sesi auth.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*AuthSession
	log      *logger.Logger
	store    *storage.Storage
	cfg      *config.Config
	// Callback ketika login sukses: jalankan account goroutine.
	onLoginSuccess func(sessionID, sessionString, userID, name string)
}

// NewManager membuat Manager baru.
func NewManager(cfg *config.Config, log *logger.Logger, store *storage.Storage,
	onLoginSuccess func(string, string, string, string)) *Manager {
	return &Manager{
		sessions:       make(map[string]*AuthSession),
		log:            log,
		store:          store,
		cfg:            cfg,
		onLoginSuccess: onLoginSuccess,
	}
}

func generateSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start memulai proses auth baru.
func (m *Manager) Start(password, callbackURL string) (*AuthSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessionID := generateSessionID()
	ctx, cancel := context.WithCancel(context.Background())

	// Waktu expired: 2 menit (Telegram QR default ~2 menit).
	expiredAt := time.Now().Add(2 * time.Minute)

	sess := &AuthSession{
		SessionID:   sessionID,
		ExpiredAt:   expiredAt,
		Cancel:      cancel,
		qrReady:     make(chan struct{}),
		password:    password,
		callbackURL: callbackURL,
	}

	m.sessions[sessionID] = sess
	go m.runAuthGoroutine(ctx, sess)

	m.log.Info("Auth session %s dimulai, expired %s", sessionID, expiredAt.Format("2006/01/02 15:04:05"))
	return sess, nil
}

// runAuthGoroutine menjalankan login QR yang sebenarnya menggunakan gotd/td.
func (m *Manager) runAuthGoroutine(ctx context.Context, sess *AuthSession) {
	defer m.cleanup(sess.SessionID)
	defer sess.Cancel()
	defer func() {
		m.mu.Lock()
		sess.password = ""
		m.mu.Unlock()
	}()

	// Storage session di memory (belum login, jadi belum di file).
	memStore := &session.StorageMemory{}

	// Channel untuk menerima QR code dari library.
	qrChan := make(chan string, 1)

	// Buat client Telegram.
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d)
	client := telegram.NewClient(m.cfg.AppID, m.cfg.AppHash, telegram.Options{
		SessionStorage: memStore,
		UpdateHandler:  d,
	})

	// Channel untuk hasil akhir (session string + user ID).
	type loginResult struct {
		sessionString string
		userID        string
		name          string
	}
	resultChan := make(chan loginResult, 1)
	errChan := make(chan error, 1)

	// Jalankan client di goroutine terpisah.
	go func() {
		err := client.Run(ctx, func(ctx context.Context) error {
			// Setup QR login.
			// gotd/td menyediakan qrlogin.NewQR untuk handle QR login.
			qr := client.QR()

			// Mulai login QR.
			authResult, err := qr.Auth(ctx, loggedIn, func(ctx context.Context, token qrlogin.Token) error {
				// Token ini yang di-encode jadi QR code.
				qrString := token.URL()
				select {
				case qrChan <- qrString:
				default:
				}
				return nil
			})
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				if sess.password == "" {
					return fmt.Errorf("QR auth gagal: akun meminta password 2FA; sertakan parameter pass saat memulai /api/auth")
				}
				authResult, err = client.Auth().Password(ctx, sess.password)
				sess.password = ""
				if errors.Is(err, telegramauth.ErrPasswordInvalid) {
					return fmt.Errorf("password 2FA tidak valid")
				}
				if err != nil {
					return fmt.Errorf("gagal verifikasi password 2FA: %w", err)
				}
			}
			if err != nil {
				return fmt.Errorf("QR auth gagal: %w", err)
			}

			_ = authResult

			// Ambil self user untuk dapat user ID.
			self, err := client.Self(ctx)
			if err != nil {
				return fmt.Errorf("gagal ambil self: %w", err)
			}

			// Simpan session string.
			// gotd/td: kita bisa export session dari storage.
			sessData, err := memStore.Bytes(nil)
			if err != nil {
				return fmt.Errorf("gagal export session: %w", err)
			}

			resultChan <- loginResult{
				sessionString: hex.EncodeToString(sessData), // simpan sebagai hex
				userID:        fmt.Sprintf("%d", self.ID),
				name:          profileName(self.FirstName, self.LastName, self.Username),
			}
			return nil
		})
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	// Loop utama: pantau QR, expiry, dan hasil.
	for {
		select {
		case <-ctx.Done():
			m.log.Info("Auth %s: dibatalkan", sess.SessionID)
			return

		case <-time.After(time.Until(sess.ExpiredAt)):
			m.log.Info("Auth %s: expired", sess.SessionID)
			return

		case qrString := <-qrChan:
			// Update QR code di session.
			m.mu.Lock()
			sess.QRCode = qrString
			select {
			case <-sess.qrReady:
			default:
				close(sess.qrReady)
			}
			m.mu.Unlock()
			m.log.Info("Auth %s: QR di-refresh", sess.SessionID)

		case res := <-resultChan:
			// Login sukses!
			m.log.Info("Auth %s: login berhasil, user_id=%s", sess.SessionID, res.userID)

			// Simpan session ke storage.
			err := m.store.Save(&storage.SessionData{
				SessionID: sess.SessionID,
				Session:   res.sessionString,
				LastLogin: time.Now().Format("2006/01/02 15:04:05"),
				UserID:    res.userID,
				Name:      res.name,
			})
			if err != nil {
				m.log.Error("Auth %s: gagal simpan session: %v", sess.SessionID, err)
				return
			}

			// Jalankan account goroutine (sebelum hentikan diri sendiri).
			m.onLoginSuccess(sess.SessionID, res.sessionString, res.userID, res.name)
			if sess.callbackURL != "" {
				go func() {
					if err := webhook.PostJSON(sess.callbackURL, map[string]interface{}{
						"ok":         true,
						"session_id": sess.SessionID,
						"user_id":    res.userID,
					}); err != nil {
						m.log.Error("Auth %s: callback gagal: %v", sess.SessionID, err)
					}
				}()
			}
			return

		case err := <-errChan:
			m.log.Error("Auth %s: error: %v", sess.SessionID, err)
			return
		}
	}
}

func (m *Manager) cleanup(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

func profileName(firstName, lastName, username string) string {
	name := strings.TrimSpace(firstName + " " + lastName)
	if name == "" {
		return username
	}
	return name
}

// Get mengembalikan AuthSession berdasarkan session_id.
func (m *Manager) Get(sessionID string) (*AuthSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return nil, false
	}
	snapshot := *sess
	snapshot.password = ""
	return &snapshot, true
}

// WaitForQRCode menunggu QR tersedia sampai batas waktu atau request dibatalkan.
func (m *Manager) WaitForQRCode(ctx context.Context, sessionID string, timeout time.Duration) (*AuthSession, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		m.mu.Lock()
		sess, ok := m.sessions[sessionID]
		if !ok {
			m.mu.Unlock()
			return nil, false
		}
		snapshot := *sess
		snapshot.password = ""
		qrReady := sess.qrReady
		m.mu.Unlock()

		if snapshot.QRCode != "" {
			return &snapshot, true
		}

		select {
		case <-qrReady:
		case <-ctx.Done():
			return &snapshot, true
		case <-timer.C:
			return &snapshot, true
		}
	}
}

// Logout membatalkan proses auth.
func (m *Manager) Logout(sessionID string) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if ok {
		sess.Cancel()
	}
}

// Count mengembalikan jumlah sesi auth aktif (untuk /api/status).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
