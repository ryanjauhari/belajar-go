// File: internal/api/handlers.go
// Berisi semua HTTP handler untuk REST API.
// Setiap handler memvalidasi api_key terlebih dahulu.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"belajar-go/internal/account"
	"belajar-go/internal/auth"
	"belajar-go/internal/config"
	"belajar-go/internal/logger"
)

// Handler menyimpan dependency yang dibutuhkan semua handler.
type Handler struct {
	cfg     *config.Config
	log     *logger.Logger
	authMgr *auth.Manager
	accMgr  *account.Manager
}

// NewHandler membuat Handler baru.
func NewHandler(cfg *config.Config, log *logger.Logger, authMgr *auth.Manager, accMgr *account.Manager) *Handler {
	return &Handler{cfg: cfg, log: log, authMgr: authMgr, accMgr: accMgr}
}

// writeJSON helper untuk menulis response JSON.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// checkAPIKey memvalidasi api_key dari query atau form.
// Mengembalikan true jika valid.
func (h *Handler) checkAPIKey(r *http.Request) bool {
	key := r.URL.Query().Get("api_key")
	if key == "" {
		key = r.FormValue("api_key")
	}
	return key == h.cfg.APIKey
}

// ========== /api/auth ==========

// HandleAuth menangani POST/GET /api/auth.
// Membuat sesi auth baru, atau mengembalikan sesi yang sudah ada jika session_id diberikan.
func (h *Handler) HandleAuth(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	// Cek apakah ada session_id di query (untuk hit ulang).
	sessionID := r.URL.Query().Get("session_id")

	if sessionID != "" {
		// Coba ambil sesi yang sudah ada.
		if sess, ok := h.authMgr.WaitForQRCode(r.Context(), sessionID, 5*time.Second); ok {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"ok":         true,
				"session_id": sess.SessionID,
				"expired":    sess.ExpiredAt.Format("2006/01/02 15:04:05"),
				"qrcode":     sess.QRCode,
			})
			return
		}
		// Sesi tidak ditemukan (mungkin sudah expired).
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found or expired"})
		return
	}

	// Buat sesi baru.
	sess, err := h.authMgr.Start(r.URL.Query().Get("pass"))
	if err != nil {
		h.log.Error("Gagal memulai auth: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	if ready, ok := h.authMgr.WaitForQRCode(r.Context(), sess.SessionID, 5*time.Second); ok {
		sess = ready
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"session_id": sess.SessionID,
		"expired":    sess.ExpiredAt.Format("2006/01/02 15:04:05"),
		"qrcode":     sess.QRCode,
	})
}

// ========== /api/auth/logout ==========

// HandleAuthLogout menangani logout proses auth yang sedang berjalan.
func (h *Handler) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = r.FormValue("session_id")
	}
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "session_id required"})
		return
	}

	h.authMgr.Logout(sessionID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ========== /api/sendMessage ==========

// HandleSendMessage menangani pengiriman pesan ke chat.
func (h *Handler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	// Ambil parameter.
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = r.FormValue("session_id")
	}
	chatID := r.URL.Query().Get("chat_id")
	chatType := r.URL.Query().Get("chat_type")
	isMedia := r.URL.Query().Get("is_media")
	mediaSource := r.URL.Query().Get("media_source")
	mediaInfo := r.URL.Query().Get("media_info")
	replyTo := r.URL.Query().Get("reply_to_message_id")
	callback := r.URL.Query().Get("callback")
	topicID := r.URL.Query().Get("topic_id")

	// Validasi session.
	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	// Log semua parameter (untuk debugging).
	h.log.Info("SendMessage: session=%s chat=%s type=%s media=%s reply=%s topic=%s",
		acc.SessionID, chatID, chatType, isMedia, replyTo, topicID)

	// Di implementasi nyata: panggil MTProto API untuk kirim pesan.
	// Karena ini kerangka, kita kembalikan response sukses.

	// Jika ada callback, panggil secara async (simulasi).
	if callback != "" {
		go func() {
			// Di implementasi nyata: HTTP POST ke callback URL.
			h.log.Info("Akan memanggil callback: %s", callback)
		}()
	}

	// Response.
	// Jika is_media tidak kosong, message_id bisa "PENDING".
	messageID := fmt.Sprintf("%d", time.Now().UnixNano()/1e6)
	if isMedia != "" && mediaSource != "" && mediaInfo != "" {
		// Simulasi: media besar butuh waktu, kembalikan PENDING.
		messageID = "PENDING"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"chat_id":    chatID,
		"message_id": messageID,
	})
}

// ========== /api/MyChat ==========

// HandleMyChat menangani daftar chat user.
func (h *Handler) HandleMyChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	page := r.URL.Query().Get("page")
	maxResult := r.URL.Query().Get("max_result")
	chatType := r.URL.Query().Get("type")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	// Parse pagination.
	pageNum, _ := strconv.Atoi(page)
	if pageNum < 1 {
		pageNum = 1
	}
	maxNum, _ := strconv.Atoi(maxResult)
	if maxNum < 1 {
		maxNum = 10
	}

	h.log.Info("MyChat: session=%s page=%d max=%d type=%s", sessionID, pageNum, maxNum, chatType)

	// Di implementasi nyata: panggil MTProto untuk ambil daftar chat.
	// Placeholder response.
	result := []map[string]interface{}{
		{
			"name":          "Contoh Chat",
			"id":            "1234567890",
			"message_count": 5,
			"description":   "Bio contoh",
			"topic_list": []map[string]interface{}{
				{"name": "Topik 1", "id": "1"},
			},
		},
	}

	writeJSON(w, http.StatusOK, result)
}

// ========== /api/joinChat ==========

// HandleJoinChat menangani permintaan join ke grup/channel.
func (h *Handler) HandleJoinChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	chatID := r.URL.Query().Get("chat_id")
	username := r.URL.Query().Get("username")
	joinURL := r.URL.Query().Get("join_url")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	h.log.Info("JoinChat: session=%s chat_id=%s username=%s url=%s", sessionID, chatID, username, joinURL)

	// Di implementasi nyata: panggil MTProto untuk join.
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ========== /api/readChat ==========

// HandleReadChat menangani pembacaan pesan dari chat.
func (h *Handler) HandleReadChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	chatID := r.URL.Query().Get("chat_id")
	messageID := r.URL.Query().Get("message_id")
	maxResult := r.URL.Query().Get("max_result")
	page := r.URL.Query().Get("page")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	pageNum, _ := strconv.Atoi(page)
	if pageNum < 1 {
		pageNum = 1
	}
	maxNum, _ := strconv.Atoi(maxResult)
	if maxNum < 1 {
		maxNum = 10
	}

	h.log.Info("ReadChat: session=%s chat=%s msg=%s max=%d page=%d", sessionID, chatID, messageID, maxNum, pageNum)

	// Placeholder response.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"page":       pageNum,
		"message_id": messageID,
		"result": []map[string]interface{}{
			{
				"type": "message",
				"from": map[string]interface{}{
					"type":    "user",
					"message": "Halo dunia",
					"chat_id": chatID,
				},
			},
		},
	})
}

// ========== /api/getAttachment ==========

// HandleGetAttachment mengunduh attachment dari pesan.
func (h *Handler) HandleGetAttachment(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	fileID := r.URL.Query().Get("file_id")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	h.log.Info("GetAttachment: session=%s file=%s", sessionID, fileID)

	// Di implementasi nyata: download file dari MTProto dan stream ke response.
	// Set header Content-Type sesuai tipe file.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("binary data placeholder"))
}

// ========== /api/leaveChat ==========

// HandleLeaveChat menangani leave dari chat.
func (h *Handler) HandleLeaveChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	chatID := r.URL.Query().Get("chat_id")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	h.log.Info("LeaveChat: session=%s chat=%s", sessionID, chatID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ========== /api/deleteChat ==========

// HandleDeleteChat menangani delete chat.
func (h *Handler) HandleDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	chatID := r.URL.Query().Get("chat_id")

	if _, ok := h.accMgr.Get(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	h.log.Info("DeleteChat: session=%s chat=%s", sessionID, chatID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ========== /api/status ==========

// HandleStatus menangani status server.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_accounts": h.accMgr.Count(),  // akun yang tersimpan di memory
		"active_auth":    h.authMgr.Count(), // sesi auth yang sedang berjalan
		"active_account": h.accMgr.Count(),  // akun yang aktif
	})
}

// ========== /api/AccountList ==========

// HandleAccountList menangani daftar akun aktif.
func (h *Handler) HandleAccountList(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	accounts := h.accMgr.List()
	data := make([]map[string]interface{}, 0, len(accounts))
	for _, acc := range accounts {
		data = append(data, map[string]interface{}{
			"session_id": acc.SessionID,
			"name":       acc.Name,
			"id":         acc.UserID,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": data,
	})
}

// ========== /api/RemoveAccount ==========

// HandleRemoveAccount menghapus akun tertentu.
// Di balik layar harus LOGOUT dan hapus session.
func (h *Handler) HandleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = r.FormValue("session_id")
	}
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "session_id required"})
		return
	}

	// Stop account (termasuk logout dan hapus file).
	if err := h.accMgr.StopAccount(sessionID); err != nil {
		h.log.Error("Gagal remove account %s: %v", sessionID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// RegisterRoutes mendaftarkan semua route ke mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth", h.HandleAuth)
	mux.HandleFunc("/api/auth/logout", h.HandleAuthLogout)
	mux.HandleFunc("/api/sendMessage", h.HandleSendMessage)
	mux.HandleFunc("/api/MyChat", h.HandleMyChat)
	mux.HandleFunc("/api/joinChat", h.HandleJoinChat)
	mux.HandleFunc("/api/readChat", h.HandleReadChat)
	mux.HandleFunc("/api/getAttachment", h.HandleGetAttachment)
	mux.HandleFunc("/api/leaveChat", h.HandleLeaveChat)
	mux.HandleFunc("/api/deleteChat", h.HandleDeleteChat)
	mux.HandleFunc("/api/status", h.HandleStatus)
	mux.HandleFunc("/api/AccountList", h.HandleAccountList)
	mux.HandleFunc("/api/RemoveAccount", h.HandleRemoveAccount)
}
