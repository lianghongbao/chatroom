package gateway

import (
	"encoding/json"
	"testing"
)

func TestEncodeServerFrame_Shape(t *testing.T) {
	b, err := EncodeServerFrame(FrameMessage, &MessageData{MessageID: 1})
	if err != nil {
		t.Fatalf("EncodeServerFrame: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["type"] != string(FrameMessage) {
		t.Errorf("type = %v, want %v", raw["type"], FrameMessage)
	}
	if _, ok := raw["data"]; !ok {
		t.Error("缺 data 字段")
	}
}

func TestEncodePongFrame(t *testing.T) {
	var f ServerFrame
	if err := json.Unmarshal(EncodePongFrame(), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != FramePong {
		t.Errorf("type = %v, want pong", f.Type)
	}
	// pong 不带 data。
	if f.Data != nil {
		t.Errorf("data = %v, want nil", f.Data)
	}
}

func TestEncodeOnlineFrame_UsesUserIDField(t *testing.T) {
	var f struct {
		Type FrameType  `json:"type"`
		Data OnlineData `json:"data"`
	}
	if err := json.Unmarshal(EncodeOnlineFrame("uid-42", true), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != FrameOnline {
		t.Errorf("type = %v, want online", f.Type)
	}
	if f.Data.UserID != "uid-42" {
		t.Errorf("user id = %q, want uid-42", f.Data.UserID)
	}
	if !f.Data.Online {
		t.Error("online 该是 true")
	}
}

// TestOnlineFrameWireFormatIsSnakeCase 下行帧的字段名统一用蛇形 ——
// 客户端按约定解析。驼峰（userId）会让客户端拿不到 user_id。
func TestOnlineFrameWireFormatIsSnakeCase(t *testing.T) {
	var raw struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(EncodeOnlineFrame("uid-42", true), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw.Data["user_id"]; !ok {
		t.Errorf("data 里该有 user_id 字段，实际: %v", raw.Data)
	}
	if _, ok := raw.Data["userId"]; ok {
		t.Errorf("data 里不该有驼峰的 userId: %v", raw.Data)
	}
}

func TestEncodeMessageFrame_MapsAllFields(t *testing.T) {
	var f struct {
		Type FrameType   `json:"type"`
		Data MessageData `json:"data"`
	}
	payload := EncodeMessageFrame(&MessageData{
		MessageID:      9,
		ConversationID: 7,
		SenderID:       "uid-42",
		MsgType:        1,
		Content:        "hello",
		CreatedAt:      1700000000,
	})
	if err := json.Unmarshal(payload, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != FrameMessage {
		t.Errorf("type = %v, want message", f.Type)
	}
	if f.Data.MessageID != 9 || f.Data.ConversationID != 7 || f.Data.SenderID != "uid-42" {
		t.Errorf("id 字段没对上: %+v", f.Data)
	}
	if f.Data.MsgType != 1 {
		t.Errorf("msg type = %d, want 1", f.Data.MsgType)
	}
	if f.Data.Content != "hello" {
		t.Errorf("content = %q, want %q", f.Data.Content, "hello")
	}
	if f.Data.CreatedAt != 1700000000 {
		t.Errorf("created at = %d, want 1700000000", f.Data.CreatedAt)
	}
}

func TestEncodeAckFrame(t *testing.T) {
	var f struct {
		Type FrameType `json:"type"`
		Data AckData   `json:"data"`
	}
	if err := json.Unmarshal(EncodeAckFrame(&AckData{MessageID: 5, ConversationID: 7, CreatedAt: 100}), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != FrameAck {
		t.Errorf("type = %v, want ack", f.Type)
	}
	if f.Data.MessageID != 5 {
		t.Errorf("message id = %d, want 5", f.Data.MessageID)
	}
}

func TestEncodeErrorFrame(t *testing.T) {
	var f struct {
		Type FrameType `json:"type"`
		Data ErrorData `json:"data"`
	}
	if err := json.Unmarshal(EncodeErrorFrame("boom", "SEND_FAILED"), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != FrameError {
		t.Errorf("type = %v, want error", f.Type)
	}
	if f.Data.Message != "boom" {
		t.Errorf("message = %q, want %q", f.Data.Message, "boom")
	}
	if f.Data.Code != "SEND_FAILED" {
		t.Errorf("code = %q, want %q", f.Data.Code, "SEND_FAILED")
	}
}
func TestDecodeClientFrame_Chat(t *testing.T) {
	f, err := DecodeClientFrame([]byte(`{"type":"chat","conversation_id":7,"msg_type":1,"content":"hello"}`))
	if err != nil {
		t.Fatalf("DecodeClientFrame: %v", err)
	}
	if f.Type != FrameChat {
		t.Errorf("type = %v, want chat", f.Type)
	}
	if f.ConversationID != 7 {
		t.Errorf("conversation id = %d, want 7", f.ConversationID)
	}
	if f.MsgType != 1 {
		t.Errorf("msg type = %d, want 1", f.MsgType)
	}
	if f.Content != "hello" {
		t.Errorf("content = %q, want %q", f.Content, "hello")
	}
}

func TestDecodeClientFrame_Ping(t *testing.T) {
	f, err := DecodeClientFrame([]byte(`{"type":"ping"}`))
	if err != nil {
		t.Fatalf("DecodeClientFrame: %v", err)
	}
	if f.Type != FramePing {
		t.Errorf("type = %v, want ping", f.Type)
	}
}

func TestDecodeClientFrame_Malformed(t *testing.T) {
	if _, err := DecodeClientFrame([]byte(`not json`)); err == nil {
		t.Fatal("坏 JSON 该返错误")
	}
}
func TestMessageChannel_Format(t *testing.T) {
	if got := messageChannel(7); got != "chatroom:msg:7" {
		t.Errorf("channel = %q, want %q", got, "chatroom:msg:7")
	}
}

func TestConversationIDFromChannel(t *testing.T) {
	cases := []struct {
		channel string
		want    uint64
	}{
		{"chatroom:msg:7", 7},
		{"chatroom:msg:123456", 123456},
		{"chatroom:msg:", 0},
		{"chatroom:msg", 0},
		{"chatroom:msg:abc", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := ConversationIDFromChannel(c.channel); got != c.want {
			t.Errorf("ConversationIDFromChannel(%q) = %d, want %d", c.channel, got, c.want)
		}
	}
}

func TestDecodePublishPayload(t *testing.T) {
	p, err := DecodePublishPayload([]byte(`{"message_id":9,"conversation_id":7,"sender_id":"uid-42","type":1,"content":"hi","created_at":1700000000}`))
	if err != nil {
		t.Fatalf("DecodePublishPayload: %v", err)
	}
	if p.MessageID != 9 || p.ConversationID != 7 || p.SenderID != "uid-42" {
		t.Errorf("id 字段没对上: %+v", p)
	}
	if p.Type != 1 {
		t.Errorf("type = %d, want 1", p.Type)
	}
	if p.Content != "hi" {
		t.Errorf("content = %q, want %q", p.Content, "hi")
	}
	if p.CreatedAt != 1700000000 {
		t.Errorf("created at = %d, want 1700000000", p.CreatedAt)
	}
}

func TestDecodePublishPayload_Malformed(t *testing.T) {
	if _, err := DecodePublishPayload([]byte(`{`)); err == nil {
		t.Fatal("坏 JSON 该返错误")
	}
}
