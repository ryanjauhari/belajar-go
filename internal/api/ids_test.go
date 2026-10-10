package api

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestParseBotAPIID(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		chatType  string
		wantKind  peerKind
		wantID    int64
		wantError bool
	}{
		{name: "user positif", raw: "1761729952", wantKind: peerKindUser, wantID: 1761729952},
		{name: "grup biasa", raw: "-123456", wantKind: peerKindGroup, wantID: 123456},
		{name: "channel -100", raw: "-1001234567890", wantKind: peerKindChannel, wantID: 1234567890},
		{name: "negatif tanpa -100 tetap grup", raw: "-1234567890", wantKind: peerKindGroup, wantID: 1234567890},
		{name: "negatif tanpa -100 dipaksa channel", raw: "-1234567890", chatType: "channel", wantKind: peerKindChannel, wantID: 1234567890},
		{name: "chat_type memaksa channel", raw: "-123456", chatType: "channel", wantKind: peerKindChannel, wantID: 123456},
		{name: "chat_type memaksa group pada -100", raw: "-1009999", chatType: "group", wantKind: peerKindGroup, wantID: 1009999},
		{name: "ambigu -1009999 default channel", raw: "-1009999", wantKind: peerKindChannel, wantID: 9999},
		{name: "-100 saja jadi grup 100", raw: "-100", wantKind: peerKindGroup, wantID: 100},
		{name: "nol ditolak", raw: "0", wantError: true},
		{name: "kosong ditolak", raw: "", wantError: true},
		{name: "bukan angka ditolak", raw: "abc", wantError: true},
		{name: "plus ditolak", raw: "+123", wantError: true},
		{name: "user positif dipaksa channel ditolak", raw: "2000000000", chatType: "channel", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseBotAPIID(test.raw, test.chatType)
			if test.wantError {
				if err == nil {
					t.Fatalf("parseBotAPIID(%q, %q) = %+v, want error", test.raw, test.chatType, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBotAPIID(%q, %q) error = %v", test.raw, test.chatType, err)
			}
			if got.Kind != test.wantKind || got.ID != test.wantID {
				t.Fatalf("parseBotAPIID(%q, %q) = %s/%d, want %s/%d",
					test.raw, test.chatType, got.Kind, got.ID, test.wantKind, test.wantID)
			}
		})
	}
}

func TestFormatBotAPIID(t *testing.T) {
	if got := formatUserID(1761729952); got != "1761729952" {
		t.Fatalf("formatUserID = %q", got)
	}
	if got := formatGroupID(123456); got != "-123456" {
		t.Fatalf("formatGroupID = %q", got)
	}
	if got := formatChannelID(1234567890); got != "-1001234567890" {
		t.Fatalf("formatChannelID = %q", got)
	}

	// Round-trip: format lalu parse harus mengembalikan ID internal positif.
	for _, test := range []struct {
		kind peerKind
		id   int64
	}{
		{peerKindUser, 1761729952},
		{peerKindGroup, 123456},
		{peerKindChannel, 1234567890},
	} {
		var raw string
		switch test.kind {
		case peerKindUser:
			raw = formatUserID(test.id)
		case peerKindGroup:
			raw = formatGroupID(test.id)
		case peerKindChannel:
			raw = formatChannelID(test.id)
		}
		parsed, err := parseBotAPIID(raw, "")
		if err != nil || parsed.Kind != test.kind || parsed.ID != test.id {
			t.Fatalf("round-trip %s: raw=%q parsed=%s/%d err=%v", test.kind, raw, parsed.Kind, parsed.ID, err)
		}
	}
}

func TestLooksLikeBotAPIID(t *testing.T) {
	for _, value := range []string{"123", "-123", "-1001234567890", " 42 "} {
		if !looksLikeBotAPIID(value) {
			t.Errorf("looksLikeBotAPIID(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "-", "@name", "name", "+123", "12a", "t.me/name"} {
		if looksLikeBotAPIID(value) {
			t.Errorf("looksLikeBotAPIID(%q) = true, want false", value)
		}
	}
}

func TestNormalizeChatTarget(t *testing.T) {
	tests := map[string]string{
		"@publicchannel":                "publicchannel",
		"publicchannel":                 "publicchannel",
		"https://t.me/publicchannel":    "publicchannel",
		"t.me/publicchannel":            "publicchannel",
		"https://t.me/s/publicchannel":  "publicchannel",
		"https://t.me/publicchannel/42": "publicchannel",
		"www.t.me/publicchannel":        "publicchannel",
	}
	for input, want := range tests {
		if got := normalizeChatTarget(input); got != want {
			t.Errorf("normalizeChatTarget(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDialogIdentityUsesBotAPIIDs(t *testing.T) {
	chats := []tg.ChatClass{
		&tg.Chat{ID: 123456, Title: "Grup Biasa"},
		&tg.Channel{ID: 9876543210, Title: "Broadcast"},
		&tg.Channel{ID: 555000111, Title: "Supergroup", Megagroup: true},
	}
	users := []tg.UserClass{&tg.User{ID: 77, FirstName: "Rian"}}

	tests := []struct {
		peer     tg.PeerClass
		wantID   string
		wantKind string
	}{
		{&tg.PeerUser{UserID: 77}, "77", "user"},
		{&tg.PeerChat{ChatID: 123456}, "-123456", "group"},
		{&tg.PeerChannel{ChannelID: 9876543210}, "-1009876543210", "channel"},
		{&tg.PeerChannel{ChannelID: 555000111}, "-100555000111", "supergroup"},
	}
	for _, test := range tests {
		id, _, kind := dialogIdentity(test.peer, users, chats)
		if id != test.wantID || kind != test.wantKind {
			t.Errorf("dialogIdentity(%T) = %q/%q, want %q/%q", test.peer, id, kind, test.wantID, test.wantKind)
		}
	}
}

func TestPeerSummaryUsesBotAPIIDs(t *testing.T) {
	chats := []tg.ChatClass{
		&tg.Chat{ID: 123456, Title: "Grup Biasa"},
		&tg.Channel{ID: 9876543210, Title: "Broadcast"},
	}
	users := []tg.UserClass{&tg.User{ID: 77, FirstName: "Rian"}}

	if got := peerSummary(&tg.PeerUser{UserID: 77}, users, chats); got["id"] != "77" || got["type"] != "user" {
		t.Errorf("peerSummary user = %#v", got)
	}
	if got := peerSummary(&tg.PeerChat{ChatID: 123456}, users, chats); got["id"] != "-123456" || got["type"] != "group" {
		t.Errorf("peerSummary group = %#v", got)
	}
	if got := peerSummary(&tg.PeerChannel{ChannelID: 9876543210}, users, chats); got["id"] != "-1009876543210" || got["type"] != "channel" {
		t.Errorf("peerSummary channel = %#v", got)
	}
}
