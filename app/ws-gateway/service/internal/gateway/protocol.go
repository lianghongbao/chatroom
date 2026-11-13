package gateway

import (
	"chatroom/pkg/keys"
	"encoding/json"
	"time"
)

// FrameType 帧类型。
type FrameType string

const (
	FrameChat         FrameType = "chat"          // 客户端发消息
	FramePing         FrameType = "ping"          // 客户端心跳
	FrameStatus       FrameType = "status"        // 客户端查在线状态
	FrameResume       FrameType = "resume"        // 断线重连后补消息
	FramePong         FrameType = "pong"          // 服务端心跳响应
	FrameMessage      FrameType = "message"       // 服务端推消息
	FrameOnline       FrameType = "online"        // 在线状态变更
	FramePresence     FrameType = "presence"      // 会话级在线状态
	FrameError        FrameType = "error"         // 出错
	FrameAck          FrameType = "ack"           // 上行确认（消息已落库）
	FrameResumeResult FrameType = "resume_result" // 补消息的结果
)

// ClientFrame 客户端上行帧。
type ClientFrame struct {
	Type FrameType `json:"type"`
	// 会话 id。chat 帧必填；status 帧可选，表示查会话成员。
	ConversationID uint64 `json:"conversation_id,omitempty"`
	// 用户 uid。status 帧用，表示查某个人。
	UserID string `json:"user_id,omitempty"`
	// 客户端最后看到的消息 id。resume 帧用。
	LastMessageID uint64 `json:"last_message_id,omitempty"`
	// 消息类型 1=文本 2=图片。与 message 服务的枚举对齐。
	MsgType uint8 `json:"msg_type,omitempty"`
	// 消息内容。chat 帧用。
	Content string `json:"content,omitempty"`
}

// ServerFrame 服务端下行帧。
type ServerFrame struct {
	Type FrameType `json:"type"`
	Data any       `json:"data,omitempty"` // 不同 type 对应不同的 DTO
}

// MessageData 消息帧的载荷。
type MessageData struct {
	MessageID      uint64 `json:"message_id"`
	ConversationID uint64 `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	MsgType        uint8  `json:"msg_type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

// OnlineData 在线状态帧的载荷。
type OnlineData struct {
	UserID string `json:"user_id"`
	Online bool   `json:"online"`
}

// ErrorData 错误帧的载荷。
type ErrorData struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// AckData ack 帧的载荷。
type AckData struct {
	MessageID      uint64 `json:"message_id"`
	ConversationID uint64 `json:"conversation_id"`
	CreatedAt      int64  `json:"created_at"`
}

// ResumeData 断线重连补消息的结果载荷。
type ResumeData struct {
	ConversationID uint64 `json:"conversation_id"`
	Count          int    `json:"count"`
	// 补到的最后一条消息 id。客户端拿它当下次的 since_id。
	LastMessageID uint64 `json:"last_message_id"`
	// 还有更多没补完。为 true 时客户端再发一次 resume 帧继续拉。
	HasMore bool `json:"has_more"`
}

// EncodeServerFrame 把下行帧编成 JSON。
// t 帧类型
// data 载荷
func EncodeServerFrame(t FrameType, data any) ([]byte, error) {
	return json.Marshal(&ServerFrame{Type: t, Data: data})
}

// EncodeErrorFrame 编一个错误帧。
// msg 给客户端看的话
// code 错误码
func EncodeErrorFrame(msg string, code string) []byte {
	b, err := EncodeServerFrame(FrameError, &ErrorData{Message: msg, Code: code})
	if err != nil {
		// 兜底：手写一个最小帧，让客户端至少知道出错了
		return []byte(`{"type":"error","data":{"message":"internal error"}}`)
	}
	return b
}

// EncodePongFrame 编一个 pong 帧。
func EncodePongFrame() []byte {
	b, _ := EncodeServerFrame(FramePong, nil)
	return b
}

// EncodeOnlineFrame 编一个在线状态帧。
// userID 用户 ID
// online 是否在线
func EncodeOnlineFrame(userID string, online bool) []byte {
	b, _ := EncodeServerFrame(FrameOnline, &OnlineData{UserID: userID, Online: online})
	return b
}

// EncodeMessageFrame 编一个消息帧。
// d 消息内容
func EncodeMessageFrame(d *MessageData) []byte {
	b, _ := EncodeServerFrame(FrameMessage, d)
	return b
}

// EncodeAckFrame 编一个 ack 帧。
// d 落库后的消息 id 与时间
func EncodeAckFrame(d *AckData) []byte {
	b, _ := EncodeServerFrame(FrameAck, d)
	return b
}

// EncodeResumeResultFrame 编一个断线重连结果帧。
// d 补消息的结果
func EncodeResumeResultFrame(d *ResumeData) []byte {
	b, _ := EncodeServerFrame(FrameResumeResult, d)
	return b
}

// DecodeClientFrame 解析客户端上行帧。
// payload 原始帧内容
func DecodeClientFrame(payload []byte) (*ClientFrame, error) {
	var f ClientFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// messageChannel 会话对应的 Pub/Sub 频道名。
// conversationID 会话 ID
func messageChannel(conversationID uint64) string {
	return keys.MessageChannel(conversationID)
}

// nowUnix 当前 Unix 秒。
func nowUnix() int64 {
	return time.Now().Unix()
}
