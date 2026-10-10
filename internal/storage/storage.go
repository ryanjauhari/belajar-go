// File: internal/storage/storage.go
// Mengelola penyimpanan session di folder "database" di atas run-dir.
// File disimpan dengan nama {session_id}.json.

package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionData adalah struktur yang disimpan di file JSON.
type SessionData struct {
	SessionID string `json:"session_id"`     // ID sesi unik
	Session   string `json:"session"`        // String session hasil login MTProto
	LastLogin string `json:"last_login"`     // Waktu login terakhir
	UserID    string `json:"user_id"`        // ID user Telegram
	Name      string `json:"name,omitempty"` // Nama profil Telegram
}

// Storage mengelola folder database.
type Storage struct {
	mu      sync.Mutex // melindungi operasi file
	dataDir string     // path folder database
}

// New membuat Storage baru.
// Folder "database" dibuat di DALAM runDir (sesuai permintaan: "di atas folder run dir").
func New(runDir string) (*Storage, error) {
	dataDir := filepath.Join(runDir, "database")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	return &Storage{dataDir: dataDir}, nil
}

// Save menyimpan SessionData ke file {session_id}.json.
func (s *Storage) Save(data *SessionData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Set last_login jika belum diisi.
	if data.LastLogin == "" {
		data.LastLogin = time.Now().Format("2006/01/02 15:04:05")
	}

	// Encode ke JSON.
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	// Tulis ke file.
	path := filepath.Join(s.dataDir, data.SessionID+".json")
	return os.WriteFile(path, raw, 0644)
}

// Load membaca SessionData dari file berdasarkan session_id.
func (s *Storage) Load(sessionID string) (*SessionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dataDir, sessionID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data SessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// Delete menghapus file session.
func (s *Storage) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dataDir, sessionID+".json")
	return os.Remove(path)
}

// ListAll mengembalikan semua SessionData yang ada di folder database.
// Dipakai saat startup untuk memuat ulang semua akun.
func (s *Storage) ListAll() ([]*SessionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return nil, err
	}

	var result []*SessionData
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		// Baca file.
		path := filepath.Join(s.dataDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // skip file yang tidak bisa dibaca
		}
		var data SessionData
		if err := json.Unmarshal(raw, &data); err != nil {
			continue // skip file korup
		}
		result = append(result, &data)
	}
	return result, nil
}
