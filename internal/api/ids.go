// File: internal/api/ids.go
// Standarisasi ID gaya Bot API untuk seluruh endpoint.
//
// Aturan representasi eksternal (yang dipakai SEMUA endpoint, masuk maupun keluar):
//
//	user               : "<id>"             (positif, mis. "1761729952")
//	grup biasa (chat)  : "-<chat_id>"       (mis. "-123456")
//	channel/supergroup : "-100<channel_id>" (mis. "-1001234567890")
//
// Pustaka MTProto (gotd/td) selalu memakai ID internal yang POSITIF
// (user.id, chat.id, channel.id). Karena itu setiap ID negatif dari klien
// dikonversi ke bentuk internal positif di sini lewat parseBotAPIID, dan
// nilai internal positif TIDAK boleh bocor kembali ke luar; gunakan helper
// format* untuk membentuk ID gaya Bot API saat menulis response.
//
// Catatan ambiguitas: standar Bot API membedakan supergroup/channel lewat
// prefix "-100". Ini berarti grup biasa yang chat_id-nya kebetulan diawali
// "100" (mis. -1009999) secara default terbaca sebagai channel 9999. Kirim
// parameter chat_type ("group"/"chat") untuk memaksa namespace yang benar.

package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	botAPIChannelPrefix = "-100"
	botAPIGroupPrefix   = "-"
)

// peerKind adalah namespace peer pada skema ID gaya Bot API.
type peerKind int

const (
	peerKindUser peerKind = iota
	peerKindGroup
	peerKindChannel
)

func (k peerKind) String() string {
	switch k {
	case peerKindUser:
		return "user"
	case peerKindGroup:
		return "group"
	case peerKindChannel:
		return "channel"
	default:
		return "unknown"
	}
}

// botAPIID adalah hasil normalisasi chat_id: tipe peer + ID internal MTProto
// yang selalu positif dan siap dipakai pustaka gotd/td.
type botAPIID struct {
	Kind peerKind
	ID   int64
	Raw  string
}

// formatUserID mengonversi user.id internal menjadi ID gaya Bot API (positif).
func formatUserID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// formatGroupID mengonversi chat.id internal (grup biasa) menjadi "-<id>".
func formatGroupID(id int64) string {
	return botAPIGroupPrefix + strconv.FormatInt(id, 10)
}

// formatChannelID mengonversi channel.id internal menjadi "-100<id>".
func formatChannelID(id int64) string {
	return botAPIChannelPrefix + strconv.FormatInt(id, 10)
}

// looksLikeBotAPIID melaporkan apakah target berbentuk ID numerik gaya Bot API
// (digit, boleh diawali tanda minus). Dipakai untuk memutuskan apakah sebuah
// string harus di-resolve sebagai ID atau sebagai username/tautan.
func looksLikeBotAPIID(target string) bool {
	value := strings.TrimSpace(target)
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseBotAPIID menormalkan chat_id gaya Bot API menjadi tipe peer dan ID
// internal positif. chatType opsional bersifat memaksa (override) dan dipakai
// untuk memecah ambiguitas prefix "-100".
func parseBotAPIID(rawID, chatType string) (botAPIID, error) {
	trimmed := strings.TrimSpace(rawID)
	if trimmed == "" {
		return botAPIID{}, fmt.Errorf("chat_id is required")
	}
	if !looksLikeBotAPIID(trimmed) {
		return botAPIID{}, fmt.Errorf("chat_id %q must be a Bot API numeric id", rawID)
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return botAPIID{}, fmt.Errorf("chat_id %q is out of range", rawID)
	}

	kind := peerKindFromID(trimmed, value)
	if forced, ok := peerKindFromChatType(chatType); ok {
		kind = forced
	}

	var id int64
	switch kind {
	case peerKindUser:
		id = value
	case peerKindGroup:
		// "-<chat_id>" -> chat_id positif.
		id = -value
	case peerKindChannel:
		// "-100<channel_id>" -> channel_id positif.
		if suffix, ok := strings.CutPrefix(trimmed, botAPIChannelPrefix); ok && suffix != "" {
			if parsed, parseErr := strconv.ParseInt(suffix, 10, 64); parseErr == nil && parsed > 0 {
				id = parsed
				break
			}
		}
		// Fallback: klien mengirim "-<channel_id>" tanpa prefix 100.
		id = -value
	}

	if id <= 0 {
		return botAPIID{}, fmt.Errorf("chat_id %q is not a valid %s id", rawID, kind)
	}
	return botAPIID{Kind: kind, ID: id, Raw: trimmed}, nil
}

// peerKindFromID menebak namespace peer dari bentuk ID gaya Bot API.
func peerKindFromID(raw string, value int64) peerKind {
	switch {
	case value > 0:
		return peerKindUser
	case strings.HasPrefix(raw, botAPIChannelPrefix) && len(raw) > len(botAPIChannelPrefix):
		return peerKindChannel
	default:
		return peerKindGroup
	}
}

// peerKindFromChatType memetakan nilai parameter chat_type ke namespace peer.
// Nilai kosong, "all", atau nilai tak dikenal berarti tidak memaksa (ok=false).
func peerKindFromChatType(chatType string) (peerKind, bool) {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "", "all":
		return 0, false
	case "user", "private":
		return peerKindUser, true
	case "group", "chat":
		return peerKindGroup, true
	case "channel", "supergroup":
		return peerKindChannel, true
	default:
		return 0, false
	}
}

// normalizeChatTarget membersihkan username/tautan Telegram agar bisa diserahkan
// ke message.Sender.Resolve. Mendukung "@nama", "nama", "t.me/nama",
// "https://t.me/nama", "https://t.me/s/nama", dan tautan pesan "t.me/nama/123".
func normalizeChatTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if parsed, err := url.Parse(target); err == nil && parsed.Host != "" {
		target = parsed.Path
	}
	target = strings.Trim(target, "/")
	target = strings.TrimPrefix(target, "www.")
	target = strings.TrimPrefix(target, "@")
	for _, prefix := range []string{"t.me/", "telegram.me/", "telegram.dog/"} {
		if strings.HasPrefix(target, prefix) {
			target = strings.TrimPrefix(target, prefix)
			break
		}
	}
	target = strings.TrimPrefix(target, "s/")
	if slash := strings.IndexByte(target, '/'); slash >= 0 {
		target = target[:slash]
	}
	return target
}
