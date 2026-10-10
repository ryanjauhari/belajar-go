// File: internal/api/handlers.go
// Berisi semua HTTP handler untuk REST API.
// Setiap handler memvalidasi api_key terlebih dahulu.

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"belajar-go/internal/account"
	"belajar-go/internal/auth"
	"belajar-go/internal/config"
	"belajar-go/internal/logger"
	"belajar-go/internal/webhook"
)

const asyncUploadThreshold = 1 << 20

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
	sess, err := h.authMgr.Start(r.URL.Query().Get("pass"), r.URL.Query().Get("callback"))
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
	messageText := r.URL.Query().Get("message")
	if messageText == "" {
		messageText = r.FormValue("message")
	}
	isMedia := parseBoolFlag(r.URL.Query().Get("is_media"))
	mediaSource := r.URL.Query().Get("media_source")
	mediaInfo := r.URL.Query().Get("media_info")
	replyTo := r.URL.Query().Get("reply_to_message_id")
	replyToID, err := parseReplyToMessageID(replyTo)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	callback := r.URL.Query().Get("callback")
	topicID, err := parseTopicID(r.URL.Query().Get("topic_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	// Validasi session.
	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	// Jalur teks.
	if !isMedia {
		if messageText == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "message is required when is_media is not set"})
			return
		}
		h.log.Info("SendMessage(teks): session=%s chat=%s type=%s reply=%s topic=%d",
			acc.SessionID, chatID, chatType, replyTo, topicID)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		peer, err := resolveChatPeer(ctx, acc.Client, chatID, chatType)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "chat_id": chatID, "error": err.Error()})
			return
		}
		updates, err := sendTelegramText(ctx, acc.Client, peer, messageText, replyToID, topicID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "chat_id": chatID, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "chat_id": chatID, "message_id": sentMessageID(updates)})
		return
	}

	// Jalur media.
	if mediaSource == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "media_source is required"})
		return
	}
	fileInfo, err := os.Stat(mediaSource)
	if err != nil || !fileInfo.Mode().IsRegular() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "media_source must point to a readable local file"})
		return
	}
	spec, err := resolveMediaSpec(mediaInfo, mediaSource)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	h.log.Info("SendMessage(media): session=%s chat=%s type=%s jenis=%s size=%d reply=%s topic=%d mime=%s callback=%s",
		acc.SessionID, chatID, chatType, spec.kind, fileInfo.Size(), replyTo, topicID, spec.mime, callback)

	// File besar: unggah di background, hasilnya dilaporkan lewat callback.
	if fileInfo.Size() > asyncUploadThreshold {
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"ok":         true,
			"chat_id":    chatID,
			"message_id": "PENDING",
		})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			messageID, err := h.sendTelegramFile(ctx, acc.Client, chatID, chatType, mediaSource, messageText, spec, replyToID, topicID)
			result := map[string]interface{}{"ok": err == nil, "chat_id": chatID}
			if err != nil {
				result["error"] = err.Error()
			} else {
				result["message_id"] = messageID
			}
			h.deliverCallback(callback, result)
		}()
		return
	}

	// File kecil: kirim langsung, hasil tersedia di response.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	messageID, err := h.sendTelegramFile(ctx, acc.Client, chatID, chatType, mediaSource, messageText, spec, replyToID, topicID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "chat_id": chatID, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"chat_id":    chatID,
		"message_id": messageID,
	})
	// Callback tetap dikirim bila diminta, walau hasil sudah ada di response.
	if strings.TrimSpace(callback) != "" {
		go h.deliverCallback(callback, map[string]interface{}{"ok": true, "chat_id": chatID, "message_id": messageID})
	}
}

func parseReplyToMessageID(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	messageID, err := strconv.Atoi(raw)
	if err != nil || messageID <= 0 {
		return 0, fmt.Errorf("reply_to_message_id must be a positive integer")
	}
	return messageID, nil
}

// parseTopicID memvalidasi topic_id forum (integer positif). Nilai kosong
// berarti tidak dikirim ke topik tertentu.
func parseTopicID(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	topicID, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || topicID <= 0 {
		return 0, fmt.Errorf("topic_id must be a positive integer")
	}
	return topicID, nil
}

// parseBoolFlag menafsirkan flag query: nilai kosong, 0, false, no, dan off
// dianggap nonaktif; nilai lain (mis. true atau 1) dianggap aktif.
func parseBoolFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func sendTelegramText(ctx context.Context, client *telegram.Client, peer tg.InputPeerClass, text string, replyTo, topicID int) (tg.UpdatesClass, error) {
	randomID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return nil, fmt.Errorf("generate random id: %w", err)
	}
	return client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  text,
		RandomID: randomID,
		ReplyTo:  buildReplyTo(replyTo, topicID),
	})
}

func (h *Handler) sendTelegramFile(ctx context.Context, client *telegram.Client, chatID, chatType, path, caption string, spec mediaSpec, replyTo, topicID int) (string, error) {
	peer, err := resolveChatPeer(ctx, client, chatID, chatType)
	if err != nil {
		return "", err
	}
	started := time.Now()
	h.log.Info("Upload mulai: %s (jenis=%s, mime=%s) ke %s", filepath.Base(path), spec.kind, spec.mime, chatID)

	// Unggah dengan progress, lalu kirim sebagai jenis media yang diminta.
	upl := uploader.NewUploader(client.API()).WithProgress(&logProgress{log: h.log, name: filepath.Base(path), lastPercent: -1})
	sender := message.NewSender(client.API()).WithUploader(upl)
	file, err := sender.To(peer).Upload(message.FromPath(path)).AsInputFile(ctx)
	if err != nil {
		return "", fmt.Errorf("telegram upload failed: %w", err)
	}
	h.log.Info("Upload selesai: %s dalam %s, mengirim sebagai %s", filepath.Base(path), time.Since(started).Round(time.Second), spec.kind)

	randomID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	updates, err := client.API().MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    buildUploadedMedia(spec, file, path),
		Message:  caption,
		RandomID: randomID,
		ReplyTo:  buildReplyTo(replyTo, topicID),
	})
	if err != nil {
		return "", fmt.Errorf("telegram upload/send failed: %w", err)
	}
	if value := sentMessageID(updates); value != 0 {
		return strconv.Itoa(value), nil
	}
	return "", nil
}

// deliverCallback mengirim hasil ke URL callback dan mencatatnya ke log.
func (h *Handler) deliverCallback(url string, payload map[string]interface{}) {
	if strings.TrimSpace(url) == "" {
		return
	}
	h.log.Info("Callback: mengirim ke %s payload=%v", url, payload)
	if err := webhook.PostJSON(url, payload); err != nil {
		h.log.Error("Callback: gagal ke %s: %v", url, err)
		return
	}
	h.log.Info("Callback: sukses ke %s", url)
}

// mediaSpec adalah jenis media + MIME yang dipakai saat mengirim file.
type mediaSpec struct {
	kind string // document, photo, video, voice, audio, animation
	mime string
}

// resolveMediaSpec menerjemahkan media_info. Nilainya boleh kata kunci
// sederhana (document, photo, video, voice, audio, animation) atau sebuah MIME
// type (mis. video/mp4). Kosong berarti dokumen dengan MIME dari ekstensi file.
func resolveMediaSpec(mediaInfo, path string) (mediaSpec, error) {
	raw := strings.ToLower(strings.TrimSpace(mediaInfo))
	if raw == "" {
		return mediaSpec{kind: "document", mime: detectMIME(path)}, nil
	}
	// Nilai berbentuk MIME (mengandung "/"): pakai MIME dan simpulkan jenisnya.
	if strings.Contains(raw, "/") {
		switch {
		case strings.HasPrefix(raw, "image/"):
			return mediaSpec{kind: "photo", mime: raw}, nil
		case strings.HasPrefix(raw, "video/"):
			return mediaSpec{kind: "video", mime: raw}, nil
		case strings.HasPrefix(raw, "audio/"):
			return mediaSpec{kind: "audio", mime: raw}, nil
		default:
			return mediaSpec{kind: "document", mime: raw}, nil
		}
	}
	// Nilai berupa kata kunci jenis media.
	switch raw {
	case "document", "file":
		return mediaSpec{kind: "document", mime: detectMIME(path)}, nil
	case "photo", "image":
		return mediaSpec{kind: "photo"}, nil
	case "video":
		return mediaSpec{kind: "video", mime: "video/mp4"}, nil
	case "voice":
		return mediaSpec{kind: "voice", mime: "audio/ogg"}, nil
	case "audio", "music":
		return mediaSpec{kind: "audio", mime: "audio/mp3"}, nil
	case "animation", "gif":
		return mediaSpec{kind: "animation", mime: "video/mp4"}, nil
	default:
		return mediaSpec{}, fmt.Errorf("media_info %q tidak dikenal; isi document, photo, video, voice, audio, animation, atau sebuah MIME type", mediaInfo)
	}
}

func detectMIME(path string) string {
	if detected := mime.TypeByExtension(filepath.Ext(path)); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

// buildUploadedMedia menyusun InputMedia sesuai jenis yang diminta.
func buildUploadedMedia(spec mediaSpec, file tg.InputFileClass, path string) tg.InputMediaClass {
	name := filepath.Base(path)
	switch spec.kind {
	case "photo":
		return &tg.InputMediaUploadedPhoto{File: file}
	case "video":
		video := &tg.DocumentAttributeVideo{SupportsStreaming: true}
		video.SetFlags()
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: spec.mime,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
				video,
			},
		}
	case "voice":
		voice := &tg.DocumentAttributeAudio{Voice: true}
		voice.SetFlags()
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: spec.mime,
			Attributes: []tg.DocumentAttributeClass{
				voice,
			},
		}
	case "audio":
		audio := &tg.DocumentAttributeAudio{Title: name}
		audio.SetFlags()
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: spec.mime,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
				audio,
			},
		}
	case "animation":
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: spec.mime,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
				&tg.DocumentAttributeAnimated{},
			},
		}
	default: // document
		return &tg.InputMediaUploadedDocument{
			ForceFile: true,
			File:      file,
			MimeType:  spec.mime,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
			},
		}
	}
}

// logProgress mencatat persentase unggah ke go-running.log tiap kelipatan 10%.
type logProgress struct {
	log         *logger.Logger
	name        string
	lastPercent int
}

func (p *logProgress) Chunk(_ context.Context, state uploader.ProgressState) error {
	if state.Total <= 0 {
		return nil
	}
	percent := int(float64(state.Uploaded) / float64(state.Total) * 100)
	if percent >= p.lastPercent+10 || (percent >= 100 && p.lastPercent < 100) {
		p.lastPercent = percent
		p.log.Info("Upload %s: %d%% (%d/%d bytes)", p.name, percent, state.Uploaded, state.Total)
	}
	return nil
}

// buildReplyTo menyusun target balasan/topik untuk messages.sendMessage dan
// messages.sendMedia. Bila topicID diisi, pesan dikirim ke topik forum tersebut.
func buildReplyTo(replyTo, topicID int) tg.InputReplyToClass {
	if replyTo <= 0 && topicID <= 0 {
		return nil
	}
	reply := &tg.InputReplyToMessage{}
	if replyTo > 0 {
		reply.ReplyToMsgID = replyTo
	}
	if topicID > 0 {
		if replyTo <= 0 {
			// Menunjuk pesan topik itu sendiri agar pesan masuk ke topik, bukan "General".
			reply.ReplyToMsgID = topicID
		}
		reply.SetTopMsgID(topicID)
	}
	return reply
}

func resolveChatPeer(ctx context.Context, client *telegram.Client, chatID, chatType string) (tg.InputPeerClass, error) {
	target := strings.TrimSpace(chatID)
	if target == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	// ID numerik selalu memakai skema gaya Bot API (negatif); username/tautan di-resolve.
	if looksLikeBotAPIID(target) {
		return resolveDialogPeer(ctx, client.API(), target, chatType)
	}
	peer, err := message.NewSender(client.API()).Resolve(normalizeChatTarget(target)).AsInputPeer(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve chat_id: %w", err)
	}
	return peer, nil
}

func resolveDialogPeer(ctx context.Context, client *tg.Client, rawID, chatType string) (tg.InputPeerClass, error) {
	// Normalkan ID gaya Bot API menjadi tipe peer + ID internal positif.
	parsed, err := parseBotAPIID(rawID, chatType)
	if err != nil {
		return nil, err
	}
	result, err := client.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return nil, fmt.Errorf("load dialogs to resolve numeric chat_id: %w", err)
	}

	var users []tg.UserClass
	var chats []tg.ChatClass
	switch dialogs := result.(type) {
	case *tg.MessagesDialogs:
		users, chats = dialogs.Users, dialogs.Chats
	case *tg.MessagesDialogsSlice:
		users, chats = dialogs.Users, dialogs.Chats
	default:
		return nil, fmt.Errorf("unexpected Telegram dialogs response %T", result)
	}

	// Pencocokan dibatasi ke namespace yang diminta dan memakai ID internal
	// positif, sehingga tidak ada lagi pencocokan abs() yang bisa menabrak
	// namespace user/group/channel lain.
	for _, dialog := range dialogsList(result) {
		switch peer := dialog.(type) {
		case *tg.PeerUser:
			if parsed.Kind != peerKindUser || peer.UserID != parsed.ID {
				continue
			}
			for _, item := range users {
				if user, ok := item.AsNotEmpty(); ok && user.ID == parsed.ID {
					return user.AsInputPeer(), nil
				}
			}
		case *tg.PeerChat:
			if parsed.Kind != peerKindGroup || peer.ChatID != parsed.ID {
				continue
			}
			for _, item := range chats {
				if chat, ok := item.AsNotEmpty(); ok {
					if value, ok := chat.(*tg.Chat); ok && value.ID == parsed.ID {
						return value.AsInputPeer(), nil
					}
				}
			}
		case *tg.PeerChannel:
			if parsed.Kind != peerKindChannel || peer.ChannelID != parsed.ID {
				continue
			}
			for _, item := range chats {
				if chat, ok := item.AsNotEmpty(); ok {
					if value, ok := chat.(*tg.Channel); ok && value.ID == parsed.ID {
						return value.AsInputPeer(), nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("chat_id %q was not found in the account's first 100 dialogs; use a username or t.me link", rawID)
}

func dialogsList(result tg.MessagesDialogsClass) []tg.PeerClass {
	switch dialogs := result.(type) {
	case *tg.MessagesDialogs:
		peers := make([]tg.PeerClass, 0, len(dialogs.Dialogs))
		for _, dialog := range dialogs.Dialogs {
			if value, ok := dialog.(*tg.Dialog); ok {
				peers = append(peers, value.Peer)
			}
		}
		return peers
	case *tg.MessagesDialogsSlice:
		peers := make([]tg.PeerClass, 0, len(dialogs.Dialogs))
		for _, dialog := range dialogs.Dialogs {
			if value, ok := dialog.(*tg.Dialog); ok {
				peers = append(peers, value.Peer)
			}
		}
		return peers
	default:
		return nil
	}
}

func sentMessageID(updates tg.UpdatesClass) int {
	switch value := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return value.ID
	case *tg.Updates:
		for _, update := range value.Updates {
			switch item := update.(type) {
			case *tg.UpdateNewMessage:
				if item.Message != nil {
					return item.Message.GetID()
				}
			case *tg.UpdateNewChannelMessage:
				if item.Message != nil {
					return item.Message.GetID()
				}
			}
		}
	case *tg.UpdatesCombined:
		for _, update := range value.Updates {
			switch item := update.(type) {
			case *tg.UpdateNewMessage:
				if item.Message != nil {
					return item.Message.GetID()
				}
			case *tg.UpdateNewChannelMessage:
				if item.Message != nil {
					return item.Message.GetID()
				}
			}
		}
	}
	return 0
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

	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
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
	if maxNum > 100 {
		maxNum = 100
	}

	h.log.Info("MyChat: session=%s page=%d max=%d type=%s", sessionID, pageNum, maxNum, chatType)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	response, err := acc.Client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": fmt.Sprintf("get Telegram dialogs: %v", err)})
		return
	}
	items, err := mapDialogs(response, chatType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	start := (pageNum - 1) * maxNum
	if start >= len(items) {
		writeJSON(w, http.StatusOK, []map[string]interface{}{})
		return
	}
	end := start + maxNum
	if end > len(items) {
		end = len(items)
	}
	writeJSON(w, http.StatusOK, items[start:end])
}

func mapDialogs(response tg.MessagesDialogsClass, filter string) ([]map[string]interface{}, error) {
	var dialogs []tg.DialogClass
	var messages []tg.MessageClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch result := response.(type) {
	case *tg.MessagesDialogs:
		dialogs, messages, users, chats = result.Dialogs, result.Messages, result.Users, result.Chats
	case *tg.MessagesDialogsSlice:
		dialogs, messages, users, chats = result.Dialogs, result.Messages, result.Users, result.Chats
	default:
		return nil, fmt.Errorf("unexpected Telegram dialogs response %T", response)
	}

	wantedType := strings.ToLower(strings.TrimSpace(filter))
	switch wantedType {
	case "", "all", "user", "private", "group", "chat", "channel", "supergroup":
	default:
		return nil, fmt.Errorf("unsupported type filter %q; use user, group, channel, or all", filter)
	}

	lastMessages := make(map[int]string, len(messages))
	for _, item := range messages {
		if msg, ok := item.(*tg.Message); ok {
			lastMessages[msg.ID] = msg.Message
		}
	}
	result := make([]map[string]interface{}, 0, len(dialogs))
	for _, item := range dialogs {
		dialog, ok := item.(*tg.Dialog)
		if !ok || dialog.Peer == nil {
			continue
		}
		id, name, kind := dialogIdentity(dialog.Peer, users, chats)
		if id == "" || !dialogMatchesFilter(kind, wantedType) {
			continue
		}
		result = append(result, map[string]interface{}{
			"id":           id,
			"name":         name,
			"type":         kind,
			"unread_count": dialog.UnreadCount,
			"last_message": lastMessages[dialog.TopMessage],
		})
	}
	return result, nil
}

func dialogIdentity(peer tg.PeerClass, users []tg.UserClass, chats []tg.ChatClass) (id, name, kind string) {
	switch target := peer.(type) {
	case *tg.PeerUser:
		for _, item := range users {
			if user, ok := item.AsNotEmpty(); ok && user.ID == target.UserID {
				name = strings.TrimSpace(user.FirstName + " " + user.LastName)
				if name == "" {
					name = user.Username
				}
				return formatUserID(user.ID), name, "user"
			}
		}
	case *tg.PeerChat:
		for _, item := range chats {
			if value, ok := item.AsNotEmpty(); ok {
				if chat, ok := value.(*tg.Chat); ok && chat.ID == target.ChatID {
					return formatGroupID(chat.ID), chat.Title, "group"
				}
			}
		}
	case *tg.PeerChannel:
		for _, item := range chats {
			if value, ok := item.AsNotEmpty(); ok {
				if channel, ok := value.(*tg.Channel); ok && channel.ID == target.ChannelID {
					kind = "channel"
					if channel.Megagroup {
						kind = "supergroup"
					}
					return formatChannelID(channel.ID), channel.Title, kind
				}
			}
		}
	}
	return "", "", ""
}

func dialogMatchesFilter(kind, filter string) bool {
	switch filter {
	case "", "all":
		return true
	case "user", "private":
		return kind == "user"
	case "group", "chat":
		return kind == "group" || kind == "supergroup"
	case "channel":
		return kind == "channel"
	case "supergroup":
		return kind == "supergroup"
	default:
		return false
	}
}

// ========== /api/joinChat ==========

// HandleJoinChat menangani permintaan join ke grup/channel.
func (h *Handler) HandleJoinChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	target := rawQueryParameter(r.URL.RawQuery, "target_chat")
	if target == "" {
		target = r.FormValue("target_chat")
	}
	if strings.TrimSpace(target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "status": "failed", "description": "target_chat is required"})
		return
	}

	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "status": "failed", "description": "session not found"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	status, description := joinTelegramTarget(ctx, acc.Client, target)
	if status == "failed" {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "status": status, "description": description})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": status})
}

func rawQueryParameter(rawQuery, name string) string {
	for _, pair := range strings.Split(rawQuery, "&") {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil || decodedKey != name {
			continue
		}
		decodedValue, err := url.PathUnescape(value)
		if err != nil {
			return ""
		}
		return decodedValue
	}
	return ""
}

func joinTelegramTarget(ctx context.Context, client *telegram.Client, target string) (string, string) {
	target = strings.TrimSpace(target)
	if inviteHash, ok := privateInviteHash(target); ok {
		invite, err := client.API().MessagesCheckChatInvite(ctx, inviteHash)
		if err != nil {
			return joinFailure(err)
		}
		if _, alreadyJoined := invite.(*tg.ChatInviteAlready); alreadyJoined {
			return "success", ""
		}
		_, err = client.API().MessagesImportChatInvite(ctx, inviteHash)
		if err != nil {
			if tgerr.Is(err, "INVITE_REQUEST_SENT") {
				return "pending", ""
			}
			if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
				return "success", ""
			}
			return joinFailure(err)
		}
		return "success", ""
	}

	peer, err := resolveJoinPeer(ctx, client, target)
	if err != nil {
		return "failed", err.Error()
	}

	switch value := peer.(type) {
	case *tg.InputPeerChannel:
		_, err := client.API().ChannelsJoinChannel(ctx, &tg.InputChannel{
			ChannelID:  value.ChannelID,
			AccessHash: value.AccessHash,
		})
		if err != nil {
			if tgerr.Is(err, "INVITE_REQUEST_SENT") {
				return "pending", ""
			}
			if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
				return "success", ""
			}
			return joinFailure(err)
		}
		return "success", ""
	case *tg.InputPeerChat:
		// Peer grup biasa hanya bisa muncul dari daftar dialog akun, yang berarti
		// akun sudah menjadi anggota. Telegram tidak menyediakan operasi "join"
		// untuk grup biasa tanpa tautan undangan, jadi ini sudah dianggap sukses.
		return "success", ""
	case *tg.InputPeerUser:
		return "failed", "target_chat points to a user; use a group, supergroup, or channel"
	default:
		return "failed", "target_chat must be a public channel/supergroup, a private invite link, or a Bot API id present in the dialogs"
	}
}

func privateInviteHash(target string) (string, bool) {
	value := target
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", false
		}
		value = parsed.Host + parsed.Path
	}
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "www.")
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "telegram.me/")
	value = strings.Trim(value, "/")
	if strings.HasPrefix(value, "+") {
		return strings.TrimPrefix(value, "+"), true
	}
	if strings.HasPrefix(value, "joinchat/") {
		return strings.TrimPrefix(value, "joinchat/"), true
	}
	return "", false
}

func resolveJoinPeer(ctx context.Context, client *telegram.Client, target string) (tg.InputPeerClass, error) {
	target = strings.TrimSpace(target)
	if looksLikeBotAPIID(target) {
		return resolveDialogPeer(ctx, client.API(), target, "")
	}
	peer, err := message.NewSender(client.API()).Resolve(normalizeChatTarget(target)).AsInputPeer(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve target_chat: %w", err)
	}
	return peer, nil
}

func joinFailure(err error) (string, string) {
	description := err.Error()
	if tgerr.Is(err, "INVITE_HASH_EXPIRED") {
		description = "Invite link has expired"
	} else if tgerr.Is(err, "INVITE_HASH_INVALID") {
		description = "Invite link is invalid"
	} else if tgerr.Is(err, "CHANNEL_PRIVATE") {
		description = "Channel is private; use a valid invite link"
	} else if tgerr.Is(err, "CHANNELS_TOO_MUCH") || tgerr.Is(err, "USER_CHANNELS_TOO_MUCH") {
		description = "Telegram account has reached its channel/group limit"
	}
	return "failed", description
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

	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
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
	if maxNum > 100 {
		maxNum = 100
	}

	h.log.Info("ReadChat: session=%s chat=%s msg=%s max=%d page=%d", sessionID, chatID, messageID, maxNum, pageNum)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	peer, err := resolveChatPeer(ctx, acc.Client, chatID, r.URL.Query().Get("chat_type"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	offsetID, _ := strconv.Atoi(messageID)
	response, err := acc.Client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:      peer,
		OffsetID:  offsetID,
		AddOffset: -(pageNum - 1) * maxNum,
		Limit:     maxNum,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": fmt.Sprintf("get Telegram chat history: %v", err)})
		return
	}
	self, _ := acc.Client.Self(ctx)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chat_id":    chatID,
		"page":       pageNum,
		"message_id": messageID,
		"result":     mapHistory(response, chatID, self),
	})
}

func mapHistory(response tg.MessagesMessagesClass, chatID string, self *tg.User) []map[string]interface{} {
	var messages []tg.MessageClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch result := response.(type) {
	case *tg.MessagesMessages:
		messages, users, chats = result.Messages, result.Users, result.Chats
	case *tg.MessagesMessagesSlice:
		messages, users, chats = result.Messages, result.Users, result.Chats
	case *tg.MessagesChannelMessages:
		messages, users, chats = result.Messages, result.Users, result.Chats
	}
	result := make([]map[string]interface{}, 0, len(messages))
	for _, item := range messages {
		entry := map[string]interface{}{"from": map[string]interface{}{}, "message": map[string]interface{}{"text": "", "type": "service"}}
		if msg, ok := item.(*tg.Message); ok {
			from := peerSummary(msg.FromID, users, chats)
			if (msg.FromID == nil || len(from) == 0) && msg.Out && self != nil {
				from = userSummary(self)
			} else if (msg.FromID == nil || len(from) == 0) && !msg.Out {
				// Hanya chat pribadi (ID user positif) yang pengirimnya bisa ditebak.
				if parsed, err := parseBotAPIID(chatID, ""); err == nil && parsed.Kind == peerKindUser {
					from = peerSummary(&tg.PeerUser{UserID: parsed.ID}, users, chats)
				}
			}
			if from == nil {
				from = map[string]interface{}{}
			}
			entry["from"] = from
			entry["message"] = map[string]interface{}{"text": msg.Message, "type": "message"}
			entry["date"] = msg.Date
			entry["outgoing"] = msg.Out
			if msg.Media != nil {
				if media, ok := mapMedia(msg.Media, chatID, msg.ID); ok {
					entry["message"] = map[string]interface{}{"text": msg.Message, "type": media["type"], "file_id": media["file_id"]}
				}
			}
		} else if msg, ok := item.(*tg.MessageService); ok {
			entry["date"] = msg.Date
			from := peerSummary(msg.FromID, users, chats)
			if from == nil {
				from = map[string]interface{}{}
			}
			entry["from"] = from
		}
		result = append(result, entry)
	}
	return result
}

func peerSummary(peer tg.PeerClass, users []tg.UserClass, chats []tg.ChatClass) map[string]interface{} {
	if peer == nil {
		return nil
	}
	switch value := peer.(type) {
	case *tg.PeerUser:
		for _, item := range users {
			if user, ok := item.AsNotEmpty(); ok && user.ID == value.UserID {
				return userSummary(user)
			}
		}
		return map[string]interface{}{"id": formatUserID(value.UserID), "name": "", "type": "user"}
	case *tg.PeerChat:
		for _, item := range chats {
			if chat, ok := item.AsNotEmpty(); ok {
				if group, ok := chat.(*tg.Chat); ok && group.ID == value.ChatID {
					return map[string]interface{}{"id": formatGroupID(value.ChatID), "name": group.Title, "type": "group"}
				}
			}
		}
		return map[string]interface{}{"id": formatGroupID(value.ChatID), "name": "", "type": "group"}
	case *tg.PeerChannel:
		for _, item := range chats {
			if chat, ok := item.AsNotEmpty(); ok {
				if channel, ok := chat.(*tg.Channel); ok && channel.ID == value.ChannelID {
					return map[string]interface{}{"id": formatChannelID(value.ChannelID), "name": channel.Title, "type": "channel"}
				}
			}
		}
		return map[string]interface{}{"id": formatChannelID(value.ChannelID), "name": "", "type": "channel"}
	default:
		return map[string]interface{}{}
	}
}

func userSummary(user *tg.User) map[string]interface{} {
	if user == nil {
		return map[string]interface{}{}
	}
	result := map[string]interface{}{
		"id":   strconv.FormatInt(user.ID, 10),
		"name": strings.TrimSpace(user.FirstName + " " + user.LastName),
		"type": "user",
	}
	if user.Username != "" {
		result["username"] = user.Username
	}
	return result
}

type attachmentRef struct {
	ChatID    string `json:"c"`
	MessageID int    `json:"m"`
}

func makeAttachmentID(chatID string, messageID int) string {
	value, _ := json.Marshal(attachmentRef{ChatID: chatID, MessageID: messageID})
	return base64.RawURLEncoding.EncodeToString(value)
}

func parseAttachmentID(fileID string) (attachmentRef, error) {
	var ref attachmentRef
	value, err := base64.RawURLEncoding.DecodeString(fileID)
	if err != nil {
		return ref, fmt.Errorf("invalid file_id")
	}
	if err := json.Unmarshal(value, &ref); err != nil || ref.ChatID == "" || ref.MessageID <= 0 {
		return attachmentRef{}, fmt.Errorf("invalid file_id")
	}
	return ref, nil
}

func mapMedia(media tg.MessageMediaClass, chatID string, messageID int) (map[string]interface{}, bool) {
	switch value := media.(type) {
	case *tg.MessageMediaPhoto:
		if _, ok := value.Photo.(*tg.Photo); ok {
			return map[string]interface{}{"type": "photo", "file_id": makeAttachmentID(chatID, messageID)}, true
		}
	case *tg.MessageMediaDocument:
		if document, ok := value.Document.(*tg.Document); ok {
			kind := "document"
			if strings.HasPrefix(document.MimeType, "image/") {
				kind = "photo"
			} else if strings.HasPrefix(document.MimeType, "video/") || value.Video {
				kind = "video"
			} else if strings.HasPrefix(document.MimeType, "audio/") || value.Voice {
				kind = "audio"
			}
			for _, attribute := range document.Attributes {
				switch attribute.(type) {
				case *tg.DocumentAttributeAnimated:
					kind = "animation"
				case *tg.DocumentAttributeSticker:
					kind = "sticker"
				}
			}
			return map[string]interface{}{"type": kind, "file_id": makeAttachmentID(chatID, messageID)}, true
		}
	}
	return nil, false
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

	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}

	ref, err := parseAttachmentID(fileID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	peer, err := resolveChatPeer(ctx, acc.Client, ref.ChatID, r.URL.Query().Get("chat_type"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	history, err := acc.Client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     peer,
		OffsetID: ref.MessageID + 1,
		Limit:    1,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": fmt.Sprintf("get Telegram message: %v", err)})
		return
	}
	messageItem := findMessage(history, ref.MessageID)
	if messageItem == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "Telegram message not found"})
		return
	}
	location, fileName, contentType, err := attachmentLocation(messageItem)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	tempFile, err := os.CreateTemp("", "telegram-attachment-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "could not create temporary download file"})
		return
	}
	tempPath := tempFile.Name()
	_ = tempFile.Close()
	defer os.Remove(tempPath)
	if _, err := downloader.NewDownloader().Download(acc.Client.API(), location).ToPath(ctx, tempPath); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": fmt.Sprintf("download Telegram attachment: %v", err)})
		return
	}
	file, err := os.Open(tempPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "could not open downloaded attachment"})
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(fileName)}))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func findMessage(response tg.MessagesMessagesClass, messageID int) *tg.Message {
	var messages []tg.MessageClass
	switch result := response.(type) {
	case *tg.MessagesMessages:
		messages = result.Messages
	case *tg.MessagesMessagesSlice:
		messages = result.Messages
	case *tg.MessagesChannelMessages:
		messages = result.Messages
	}
	for _, item := range messages {
		if msg, ok := item.(*tg.Message); ok && msg.ID == messageID {
			return msg
		}
	}
	return nil
}

func attachmentLocation(msg *tg.Message) (tg.InputFileLocationClass, string, string, error) {
	if msg.Media == nil {
		return nil, "", "", fmt.Errorf("message has no downloadable media")
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok {
			return nil, "", "", fmt.Errorf("photo is unavailable")
		}
		bestType := ""
		bestArea := int64(-1)
		for _, item := range photo.Sizes {
			var sizeType string
			var area int64
			switch size := item.(type) {
			case *tg.PhotoSize:
				sizeType = size.Type
				area = int64(size.W) * int64(size.H)
			case *tg.PhotoSizeProgressive:
				sizeType = size.Type
				area = int64(size.W) * int64(size.H)
			}
			if sizeType != "" && area > bestArea {
				bestType, bestArea = sizeType, area
			}
		}
		if bestType == "" {
			return nil, "", "", fmt.Errorf("photo has no downloadable size")
		}
		return &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: bestType}, fmt.Sprintf("photo_%d.jpg", photo.ID), "image/jpeg", nil
	case *tg.MessageMediaDocument:
		document, ok := media.Document.(*tg.Document)
		if !ok {
			return nil, "", "", fmt.Errorf("document is unavailable")
		}
		fileName := fmt.Sprintf("file_%d", document.ID)
		for _, attribute := range document.Attributes {
			if value, ok := attribute.(*tg.DocumentAttributeFilename); ok && value.FileName != "" {
				fileName = value.FileName
				break
			}
		}
		contentType := document.MimeType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return &tg.InputDocumentFileLocation{ID: document.ID, AccessHash: document.AccessHash, FileReference: document.FileReference}, fileName, contentType, nil
	default:
		return nil, "", "", fmt.Errorf("unsupported Telegram media type %T", msg.Media)
	}
}

// ========== /api/leaveChat ==========

// HandleLeaveChat menangani leave dari chat.
func (h *Handler) HandleLeaveChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "invalid api_key"})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	target := rawQueryParameter(r.URL.RawQuery, "target_chat")
	if target == "" {
		target = r.FormValue("target_chat")
	}
	if strings.TrimSpace(target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "status": "failed", "description": "target_chat is required"})
		return
	}

	acc, ok := h.accMgr.Get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "status": "failed", "description": "session not found"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := leaveTelegramTarget(ctx, acc.Client, target); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"ok": false, "status": "failed", "description": err.Error()})
		return
	}
	h.log.Info("LeaveChat: session=%s target=%s", sessionID, target)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "success"})
}

func leaveTelegramTarget(ctx context.Context, client *telegram.Client, target string) error {
	var peer tg.InputPeerClass
	if inviteHash, ok := privateInviteHash(target); ok {
		invite, err := client.API().MessagesCheckChatInvite(ctx, inviteHash)
		if err != nil {
			_, description := joinFailure(err)
			return fmt.Errorf("check invite: %s", description)
		}
		alreadyJoined, ok := invite.(*tg.ChatInviteAlready)
		if !ok {
			return fmt.Errorf("account is not a member of the chat from this invite link")
		}
		switch chat := alreadyJoined.Chat.(type) {
		case *tg.Channel:
			peer = chat.AsInputPeer()
		case *tg.Chat:
			peer = chat.AsInputPeer()
		default:
			return fmt.Errorf("invite does not identify a leaveable group or channel")
		}
	} else {
		resolved, err := resolveJoinPeer(ctx, client, target)
		if err != nil {
			return fmt.Errorf("resolve target_chat: %w", err)
		}
		peer = resolved
	}

	switch target := peer.(type) {
	case *tg.InputPeerChannel:
		_, err := client.API().ChannelsLeaveChannel(ctx, &tg.InputChannel{ChannelID: target.ChannelID, AccessHash: target.AccessHash})
		if err != nil {
			// Channel sudah deaktif/tidak valid: perlakukan sebagai sudah keluar.
			if tgerr.Is(err, "PEER_ID_INVALID") {
				return nil
			}
			return fmt.Errorf("leave Telegram channel: %w", err)
		}
		return nil
	case *tg.InputPeerChat:
		_, err := client.API().MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
			ChatID: target.ChatID,
			UserID: &tg.InputUserSelf{},
		})
		if err != nil {
			if tgerr.Is(err, "PEER_ID_INVALID") {
				return nil
			}
			return fmt.Errorf("leave Telegram group: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("target_chat must be a group, supergroup, or channel; users cannot be left")
	}
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
