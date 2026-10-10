package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gotd/td/tg"

	"belajar-go/internal/logger"
)

func TestSentMessageID(t *testing.T) {
	tests := []struct {
		name    string
		updates tg.UpdatesClass
		want    int
	}{
		{
			name:    "short sent message",
			updates: &tg.UpdateShortSentMessage{ID: 42},
			want:    42,
		},
		{
			name: "new message update",
			updates: &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{Message: &tg.Message{ID: 73}},
			}},
			want: 73,
		},
		{
			name: "missing message update",
			updates: &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{Message: &tg.Message{ID: 0}},
			}},
			want: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sentMessageID(test.updates); got != test.want {
				t.Fatalf("sentMessageID() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestParseReplyToMessageID(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{input: "", want: 0},
		{input: "74928", want: 74928},
		{input: "0", wantErr: true},
		{input: "not-an-id", wantErr: true},
	}

	for _, test := range tests {
		got, err := parseReplyToMessageID(test.input)
		if (err != nil) != test.wantErr {
			t.Errorf("parseReplyToMessageID(%q) error = %v, wantErr %t", test.input, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Errorf("parseReplyToMessageID(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestPrivateInviteHash(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "https://t.me/+AbC123", want: "AbC123"},
		{input: "https://t.me/joinchat/AbC123", want: "AbC123"},
		{input: "t.me/+AbC123", want: "AbC123"},
		{input: "@publicchannel", want: ""},
	}

	for _, test := range tests {
		got, ok := privateInviteHash(test.input)
		if test.want == "" {
			if ok {
				t.Errorf("privateInviteHash(%q) unexpectedly returned %q", test.input, got)
			}
			continue
		}
		if !ok || got != test.want {
			t.Errorf("privateInviteHash(%q) = %q, %t; want %q, true", test.input, got, ok, test.want)
		}
	}
}

func TestRawQueryParameterPreservesInvitePlus(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"api_key=k&target_chat=https://t.me/+AbC123&session_id=s", "https://t.me/+AbC123"},
		{"target_chat=https%3A%2F%2Ft.me%2F%2BAbC123", "https://t.me/+AbC123"},
		{"target_chat=@postaja", "@postaja"},
	}

	for _, test := range tests {
		if got := rawQueryParameter(test.query, "target_chat"); got != test.want {
			t.Errorf("rawQueryParameter(%q) = %q, want %q", test.query, got, test.want)
		}
	}
}

func TestMapDialogsFiltersAndMapsUser(t *testing.T) {
	response := &tg.MessagesDialogs{
		Dialogs: []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 1761729952}, UnreadCount: 2, TopMessage: 9},
		},
		Messages: []tg.MessageClass{
			&tg.Message{ID: 9, Message: "latest"},
		},
		Users: []tg.UserClass{
			&tg.User{ID: 1761729952, FirstName: "Rian", LastName: "Note"},
		},
	}

	got, err := mapDialogs(response, "user")
	if err != nil {
		t.Fatalf("mapDialogs() error = %v", err)
	}
	if len(got) != 1 || got[0]["id"] != "1761729952" || got[0]["name"] != "Rian Note" || got[0]["last_message"] != "latest" {
		t.Fatalf("unexpected mapped dialogs: %#v", got)
	}

	got, err = mapDialogs(response, "channel")
	if err != nil {
		t.Fatalf("mapDialogs() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("channel filter returned non-channel dialog: %#v", got)
	}
}

func TestMapHistoryReturnsTelegramMessage(t *testing.T) {
	got := mapHistory(&tg.MessagesMessages{
		Messages: []tg.MessageClass{
			&tg.Message{
				ID:      31,
				Date:    1710000000,
				Message: "actual Telegram text",
				FromID:  &tg.PeerUser{UserID: 77},
			},
		},
		Users: []tg.UserClass{&tg.User{ID: 77, FirstName: "Chat", LastName: "Partner"}},
	}, "1761729952", nil)
	from, _ := got[0]["from"].(map[string]interface{})
	message, _ := got[0]["message"].(map[string]interface{})
	if len(got) != 1 || message["text"] != "actual Telegram text" || message["type"] != "message" || from["id"] != "77" || from["name"] != "Chat Partner" {
		t.Fatalf("unexpected mapped history: %#v", got)
	}
}

func TestMapHistoryInfersPrivateChatSender(t *testing.T) {
	got := mapHistory(&tg.MessagesMessages{
		Messages: []tg.MessageClass{&tg.Message{ID: 51, Message: "incoming", FromID: nil}},
		Users:    []tg.UserClass{&tg.User{ID: 1761729952, FirstName: "Chat", LastName: "Partner", Username: "partner"}},
	}, "1761729952", nil)
	from, _ := got[0]["from"].(map[string]interface{})
	if from["id"] != "1761729952" || from["name"] != "Chat Partner" || from["username"] != "partner" {
		t.Fatalf("unexpected inferred sender: %#v", from)
	}
}

func TestMapHistoryAddsAttachmentIDForPhoto(t *testing.T) {
	got := mapHistory(&tg.MessagesMessages{Messages: []tg.MessageClass{
		&tg.Message{
			ID:      88,
			Message: "photo caption",
			Media:   &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1234}},
		},
	}}, "1761729952", nil)
	message, _ := got[0]["message"].(map[string]interface{})
	if message["type"] != "photo" {
		t.Fatalf("message type = %v, want photo", message["type"])
	}
	fileID, ok := message["file_id"].(string)
	if !ok || fileID == "" {
		t.Fatalf("expected a non-empty file_id, got %v", message["file_id"])
	}
	ref, err := parseAttachmentID(fileID)
	if err != nil || ref.ChatID != "1761729952" || ref.MessageID != 88 {
		t.Fatalf("file_id reference = %#v, error = %v", ref, err)
	}
}

func TestAttachmentIDRoundTrip(t *testing.T) {
	fileID := makeAttachmentID("-100123456", 987)
	ref, err := parseAttachmentID(fileID)
	if err != nil {
		t.Fatalf("parseAttachmentID() error = %v", err)
	}
	if ref.ChatID != "-100123456" || ref.MessageID != 987 {
		t.Fatalf("unexpected attachment reference: %#v", ref)
	}
}

func TestParseTopicID(t *testing.T) {
	for _, test := range []struct {
		input   string
		want    int
		wantErr bool
	}{
		{input: "", want: 0},
		{input: "15", want: 15},
		{input: " 42 ", want: 42},
		{input: "0", wantErr: true},
		{input: "-3", wantErr: true},
		{input: "abc", wantErr: true},
	} {
		got, err := parseTopicID(test.input)
		if (err != nil) != test.wantErr {
			t.Errorf("parseTopicID(%q) error = %v, wantErr %t", test.input, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Errorf("parseTopicID(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestParseBoolFlag(t *testing.T) {
	for _, value := range []string{"", "0", "false", "FALSE", "no", "off", " off "} {
		if parseBoolFlag(value) {
			t.Errorf("parseBoolFlag(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"true", "TRUE", "1", "yes", "on", "anything"} {
		if !parseBoolFlag(value) {
			t.Errorf("parseBoolFlag(%q) = false, want true", value)
		}
	}
}

func TestBuildReplyTo(t *testing.T) {
	if got := buildReplyTo(0, 0); got != nil {
		t.Errorf("buildReplyTo(0,0) = %#v, want nil", got)
	}

	reply, ok := buildReplyTo(5, 0).(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 5 {
		t.Fatalf("buildReplyTo(5,0) = %#v", reply)
	}
	if _, hasTopic := reply.GetTopMsgID(); hasTopic {
		t.Errorf("buildReplyTo(5,0) unexpectedly set topic")
	}

	// Tanpa balasan spesifik: pesan diarahkan ke topik, reply_to_msg_id = topik.
	reply, ok = buildReplyTo(0, 15).(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 15 {
		t.Fatalf("buildReplyTo(0,15) = %#v", reply)
	}
	if topic, hasTopic := reply.GetTopMsgID(); !hasTopic || topic != 15 {
		t.Errorf("buildReplyTo(0,15) topic = %d, %t; want 15, true", topic, hasTopic)
	}

	// Balasan di dalam topik: reply ke pesan (5) di topik (15).
	reply, ok = buildReplyTo(5, 15).(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 5 {
		t.Fatalf("buildReplyTo(5,15) = %#v", reply)
	}
	if topic, hasTopic := reply.GetTopMsgID(); !hasTopic || topic != 15 {
		t.Errorf("buildReplyTo(5,15) topic = %d, %t; want 15, true", topic, hasTopic)
	}
}

func TestResolveMediaSpec(t *testing.T) {
	tests := []struct {
		input    string
		path     string
		wantKind string
		wantMime string
		wantErr  bool
	}{
		{input: "", path: "laporan.pdf", wantKind: "document", wantMime: "application/pdf"},
		{input: "document", path: "data.unknownext", wantKind: "document", wantMime: "application/octet-stream"},
		{input: "photo", path: "x.jpg", wantKind: "photo"},
		{input: "image", path: "x.jpg", wantKind: "photo"},
		{input: "video", path: "x.mp4", wantKind: "video", wantMime: "video/mp4"},
		{input: "video/mp4", path: "x.mp4", wantKind: "video", wantMime: "video/mp4"},
		{input: "audio/mpeg", path: "x.mp3", wantKind: "audio", wantMime: "audio/mpeg"},
		{input: "voice", path: "x.ogg", wantKind: "voice", wantMime: "audio/ogg"},
		{input: "gif", path: "x.mp4", wantKind: "animation", wantMime: "video/mp4"},
		{input: "nonsense", path: "x.bin", wantErr: true},
	}
	for _, test := range tests {
		got, err := resolveMediaSpec(test.input, test.path)
		if (err != nil) != test.wantErr {
			t.Errorf("resolveMediaSpec(%q,%q) error = %v, wantErr %t", test.input, test.path, err, test.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if got.kind != test.wantKind || got.mime != test.wantMime {
			t.Errorf("resolveMediaSpec(%q,%q) = %s/%s, want %s/%s", test.input, test.path, got.kind, got.mime, test.wantKind, test.wantMime)
		}
	}
}

func TestBuildUploadedMedia(t *testing.T) {
	file := &tg.InputFile{}

	if _, ok := buildUploadedMedia(mediaSpec{kind: "photo"}, file, "a.jpg").(*tg.InputMediaUploadedPhoto); !ok {
		t.Errorf("photo spec should build InputMediaUploadedPhoto")
	}

	videoMedia, ok := buildUploadedMedia(mediaSpec{kind: "video", mime: "video/mp4"}, file, "a.mp4").(*tg.InputMediaUploadedDocument)
	if !ok || videoMedia.MimeType != "video/mp4" {
		t.Fatalf("video spec = %#v", videoMedia)
	}
	hasVideo := false
	for _, attr := range videoMedia.Attributes {
		if v, ok := attr.(*tg.DocumentAttributeVideo); ok {
			hasVideo = true
			if !v.GetSupportsStreaming() {
				t.Errorf("video attribute supports_streaming not set")
			}
		}
	}
	if !hasVideo {
		t.Errorf("video spec missing DocumentAttributeVideo")
	}

	doc, ok := buildUploadedMedia(mediaSpec{kind: "document", mime: "application/pdf"}, file, "a.pdf").(*tg.InputMediaUploadedDocument)
	if !ok || !doc.ForceFile {
		t.Errorf("document spec should set ForceFile")
	}

	voiceMedia, ok := buildUploadedMedia(mediaSpec{kind: "voice", mime: "audio/ogg"}, file, "a.ogg").(*tg.InputMediaUploadedDocument)
	if !ok {
		t.Fatalf("voice spec = %#v", voiceMedia)
	}
	hasVoice := false
	for _, attr := range voiceMedia.Attributes {
		if a, ok := attr.(*tg.DocumentAttributeAudio); ok {
			hasVoice = true
			if !a.GetVoice() {
				t.Errorf("voice attribute not set")
			}
		}
	}
	if !hasVoice {
		t.Errorf("voice spec missing DocumentAttributeAudio")
	}
}

func TestDeliverCallbackPostsJSON(t *testing.T) {
	var payload map[string]interface{}
	var ua, contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		ua = r.Header.Get("User-Agent")
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	log, err := logger.New(t.TempDir())
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer log.Close()

	h := &Handler{log: log}
	h.deliverCallback(server.URL, map[string]interface{}{"ok": true, "chat_id": "1761729952", "message_id": "9"})

	if payload["ok"] != true || payload["chat_id"] != "1761729952" || payload["message_id"] != "9" {
		t.Fatalf("unexpected callback payload: %#v", payload)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if ua == "" {
		t.Errorf("User-Agent tidak diset")
	}
}

func TestDeliverCallbackEmptyURLIsNoop(t *testing.T) {
	log, err := logger.New(t.TempDir())
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer log.Close()

	h := &Handler{log: log}
	// Tidak boleh panic / tidak melakukan apa-apa bila URL kosong.
	h.deliverCallback("", map[string]interface{}{"ok": true})
}
