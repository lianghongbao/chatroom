package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIsEmojiContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"单码位表情", "😀", true},
		{"带变体选择符", "❤️", true},
		{"ZWJ 组合序列", "👨‍👩‍👧", true},
		{"手势", "👍", true},
		{"带国旗", "🇨🇳", true},
		{"空串", "", false},
		{"普通文字", "hello", false},
		{"单字中文", "好", false},
		{"带空格", "😀 😀", false},
		{"换行", "😀\n😀", false},
		{"多个表情拼一起", "😀😀", false},
		{"超长文本", "这是一段很长很长的文字内容用来测试长度上限", false},
		{"只有变体选择符", "️", false},
		{"只有 ZWJ", "‍", false},

		// 修饰符序列
		{"肤色修饰", "👍🏻", true},
		{"深色肤色", "👋🏽", true},
		{"键帽序列", "1️⃣", true},
		{"井号键帽", "#️⃣", true},
		{"星号键帽", "*️⃣", true},
		{"裸数字不是表情", "1", false},
		{"裸井号不是表情", "#", false},

		// 非 1F000 区段但有 emoji 呈现的符号
		{"键盘", "⌨️", true},
		{"手表", "⌚", true},
		{"闹钟", "⏰", true},
		{"沙漏", "⌛", true},
		{"播放", "▶️", true},
		{"双感叹号", "‼️", true},
		{"商标", "™️", true},
		{"版权", "©️", true},
		{"带方框字母", "🆎", true},
		{"日本字旗", "㊗️", true},

		// 长 ZWJ 序列
		{"五人家庭", "👨‍👩‍👧‍👦‍👦", true},
		{"情侣接吻", "👩‍❤️‍💋‍👨", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmojiContent(tc.in); got != tc.want {
				t.Errorf("isEmojiContent(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestEmojiPickerItemsAllAccepted 验表情面板里的每一个表情后端都认。
//
// 面板里的表情是后端校验的白名单 —— 面板有、后端拒的话，
// 用户点下去会看到一条发不出去的消息。
func TestEmojiPickerItemsAllAccepted(t *testing.T) {
	// 与 chatroom-web/src/components/EmojiPicker.tsx 的 EMOJI_GROUPS 对应。
	items := []string{
		"😀", "😃", "😄", "😁", "😆", "😅", "🤣", "😂",
		"🙂", "🙃", "😉", "😊", "😇", "🥰", "😍", "🤩",
		"😘", "😗", "😚", "😙", "😋", "😛", "😜", "🤪",
		"🤨", "🧐", "🤓", "😎", "🥳", "😏", "😒", "😞",
		"😔", "😟", "😕", "🙁", "☹️", "😣", "😖", "😫",
		"😩", "🥺", "😢", "😭", "😤", "😠", "😡", "🤬",
		"👍", "👎", "👌", "✌️", "🤞", "🤟", "🤘", "🤙",
		"👈", "👉", "👆", "👇", "☝️", "✋", "🤚", "🖐️",
		"🖖", "👋", "🤝", "🙏", "💪", "🦾", "✍️", "👏",
		"🙌", "🤲", "🤜", "🤛", "👀", "🧠", "👣", "🫶",
		"❤️", "🧡", "💛", "💚", "💙", "💜", "🖤", "🤍",
		"🤎", "💔", "❣️", "💕", "💞", "💓", "💗", "💖",
		"💘", "💝", "💟", "🔥", "✨", "⭐", "🌟", "💫",
		"🎉", "🎊", "🎈", "🎁", "🏆", "🥇", "🥈", "🥉",
		"⚽", "🏀", "🏈", "⚾", "🎾", "🏐", "🎱", "🏓",
		"🎮", "🎲", "🎯", "🎳", "🎤", "🎧", "🎸", "🥁",
		"📱", "💻", "⌨️", "🖥️", "🖨️", "📷", "📹", "🔍",
		"☕", "🍺", "🍻", "🥂", "🍰", "🎂", "🍎", "🍉",
		"✅", "❌", "❗", "❓", "⚠️", "🚫", "💯", "🔴",
		"🟠", "🟡", "🟢", "🔵", "🟣", "⚫", "⚪", "🔺",
		"🔻", "🔸", "🔹", "🔶", "🔷", "💤", "💨", "💢",
		"♻️", "🔱", "⚜️", "🔰", "⭕", "➕", "➖",
	}
	for _, e := range items {
		if !isEmojiContent(e) {
			t.Errorf("表情 %q 后端不认 —— 面板里有它但发不出去", e)
		}
	}
}

func TestMessage_SendEmojiAccepted(t *testing.T) {
	uc, repo, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	msg, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeEmoji, "😀")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.Type != MessageTypeEmoji {
		t.Errorf("type = %v, want MessageTypeEmoji", msg.Type)
	}
	if msg.Content != "😀" {
		t.Errorf("content = %q, want 😀", msg.Content)
	}
	if len(repo.messages[7]) != 1 {
		t.Errorf("落库消息数 = %d, want 1", len(repo.messages[7]))
	}
}

func TestMessage_SendEmojiRejectsPlainText(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	// 拿表情类型夹带普通文本 —— 消息列表里没法渲染。
	_, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeEmoji, "这是一段普通文字")
	if !errors.Is(err, ErrMessageInvalidArgument) {
		t.Fatalf("error = %v, want %v", err, ErrMessageInvalidArgument)
	}
}

func TestMessage_SendEmojiRejectsEmpty(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeEmoji, ""); !errors.Is(err, ErrMessageInvalidArgument) {
		t.Fatalf("error = %v, want %v", err, ErrMessageInvalidArgument)
	}
}

func TestMessage_SendImageRequiresStorage(t *testing.T) {
	uc, repo, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	// 没配对象存储 —— 图片消息直接拒，不落库。
	_, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageTypeImage, "https://cdn.example.com/chat_image/uid-42/a.png")
	if !errors.Is(err, ErrStorageNotConfigured) {
		t.Fatalf("error = %v, want %v", err, ErrStorageNotConfigured)
	}
	if repo.createCalls != 0 {
		t.Errorf("repo.Create 调用了 %d 次，图片校验没过就不该落库", repo.createCalls)
	}
}

func TestMessage_SendUnknownTypeRejected(t *testing.T) {
	uc, _, membership, _, _ := newTestMessageUsecase()
	membership.add(7, "uid-42")

	if _, err := uc.SendMessage(context.Background(), "uid-42", 7, MessageType(99), "x"); !errors.Is(err, ErrMessageInvalidArgument) {
		t.Fatalf("error = %v, want %v", err, ErrMessageInvalidArgument)
	}
}

func TestChatImageDirScopedToUser(t *testing.T) {
	// 前缀按 uid 拼 —— 客户端拿着临时凭证也越不出自己的目录。
	if got := chatImageDir("uid-42"); got != "chat_image/uid-42/" {
		t.Errorf("chatImageDir = %q, want chat_image/uid-42/", got)
	}
}

func TestGetImageUploadTokenRequiresStorage(t *testing.T) {
	uc, _, _, _, _ := newTestMessageUsecase()

	if _, err := uc.GetImageUploadToken(context.Background(), "uid-42", "png"); !errors.Is(err, ErrStorageNotConfigured) {
		t.Fatalf("error = %v, want %v", err, ErrStorageNotConfigured)
	}
}

func TestGetImageUploadTokenRequiresCaller(t *testing.T) {
	uc, _, _, _, _ := newTestMessageUsecase()

	if _, err := uc.GetImageUploadToken(context.Background(), "", "png"); !errors.Is(err, ErrMessageInvalidArgument) {
		t.Fatalf("error = %v, want %v", err, ErrMessageInvalidArgument)
	}
}

func TestValidateImageURLScopedToOwner(t *testing.T) {
	// 上传器的目录前缀锁在调用方 uid 下 —— 别人名下的图片地址发不进来。
	//
	// 这里直接验前缀本身：不同 uid 的目录必须不同，
	// 否则一个人就能引用另一个人的图片。
	if chatImageDir("uid-1") == chatImageDir("uid-2") {
		t.Fatal("不同 uid 的图片目录前缀必须不同")
	}
	// 前缀不能是别人的目录的前缀 —— 否则 uid-2 的目录能匹配 uid-1 的。
	if strings.HasPrefix(chatImageDir("uid-2"), strings.TrimSuffix(chatImageDir("uid-1"), "/")) {
		t.Errorf("uid-2 的目录 %q 不该以 uid-1 的目录为前缀", chatImageDir("uid-2"))
	}
	// 前缀必须带尾斜杠 —— 否则 uid-1 的前缀能匹配 uid-10 的目录。
	if !strings.HasSuffix(chatImageDir("uid-1"), "/") {
		t.Errorf("目录前缀 %q 必须以斜杠结尾", chatImageDir("uid-1"))
	}
}

func TestSendMessageChecksMembershipBeforeImageValidation(t *testing.T) {
	uc, repo, membership, _, _ := newTestMessageUsecase()
	// 调用方不是成员。
	membership.add(7, "uid-42")

	// 图片消息的 content 是个非法地址。校验顺序要是「成员校验在前」，
	// 报出来的应该是 NOT_MEMBER 而不是上传相关的错 ——
	// 否则非会员能靠错误码差异探测会话存不存在。
	_, err := uc.SendMessage(context.Background(), "uid-99", 7, MessageTypeImage, "https://evil.com/x.png")
	if !errors.Is(err, ErrMessageNotMember) {
		t.Fatalf("error = %v, want %v", err, ErrMessageNotMember)
	}
	if repo.createCalls != 0 {
		t.Errorf("repo.Create 调用了 %d 次，不该落库", repo.createCalls)
	}
}
