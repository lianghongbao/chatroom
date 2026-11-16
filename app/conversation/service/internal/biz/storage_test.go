package biz

import (
	"errors"
	"testing"

	"chatroom/pkg/oss"
)

// testGroupOSSClient 构造一个测试用的 OSS 客户端。
func testGroupOSSClient(t *testing.T) *oss.Client {
	t.Helper()
	client, err := oss.NewClient(oss.Config{
		Endpoint:        "oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "test-bucket",
		Region:          "cn-hangzhou",
		RoleArn:         "acs:ram::123456789:role/oss-upload",
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		UploadHost:      "https://test-bucket.oss-cn-hangzhou.aliyuncs.com",
		URLPrefix:       "https://cdn.example.com",
	})
	if err != nil {
		t.Fatalf("构造 OSS 客户端失败：%v", err)
	}
	return client
}

// newGroupWithOwner 建一个群会话，返回 repo 和会话。
// owner 是群主 uid，成员是 uid-2。
func newGroupWithOwner(t *testing.T, owner string) (*fakeConvRepo, *Conversation) {
	t.Helper()
	repo := newFakeConvRepo()
	conv, _, err := repo.Save(t.Context(), &Conversation{
		Type:      ConversationTypeGroup,
		Name:      "测试群",
		OwnerID:   owner,
		MemberIDs: []string{owner, "uid-2"},
	})
	if err != nil {
		t.Fatalf("建群失败：%v", err)
	}
	return repo, conv
}

// TestGroupAvatarDir key 前缀按会话 ID 拼。
func TestGroupAvatarDir(t *testing.T) {
	if got := groupAvatarDir(62); got != "group_avatar/62/" {
		t.Fatalf("期望 group_avatar/62/，实际 %q", got)
	}
	// 不同群的目录前缀必须不同 —— 前缀是唯一的越权防线。
	if groupAvatarDir(1) == groupAvatarDir(2) {
		t.Fatal("不同群的目录前缀不该相同")
	}
}

// TestGroupAvatarTokenNotConfigured 没配对象存储时签发凭证要报错。
func TestGroupAvatarTokenNotConfigured(t *testing.T) {
	repo, conv := newGroupWithOwner(t, "uid-1")
	uc := NewConversationUsecase(repo, nil, nil, nil, StorageOptions{}, nil)
	_, err := uc.GetGroupAvatarUploadToken(t.Context(), "uid-1", conv.ID, "png")
	if !errors.Is(err, ErrStorageNotConfigured) {
		t.Fatalf("期望 ErrStorageNotConfigured，实际 %v", err)
	}
}

// TestGroupAvatarTokenOwnerOnly 只有群主能签凭证。
func TestGroupAvatarTokenOwnerOnly(t *testing.T) {
	repo, conv := newGroupWithOwner(t, "uid-1")
	uc := NewConversationUsecase(repo, nil, nil, nil, StorageOptions{
		Client:      testGroupOSSClient(t),
		AllowedExts: []string{"png"},
	}, nil)

	// 非群主 —— 报 NOT_OWNER，而不是被参数校验先拦下。
	_, err := uc.GetGroupAvatarUploadToken(t.Context(), "uid-2", conv.ID, "png")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("期望 ErrConversationNotOwner，实际 %v", err)
	}
}

// TestGroupAvatarTokenExtNotAllowed 后缀不在白名单时要报错。
func TestGroupAvatarTokenExtNotAllowed(t *testing.T) {
	repo, conv := newGroupWithOwner(t, "uid-1")
	uc := NewConversationUsecase(repo, nil, nil, nil, StorageOptions{
		Client:      testGroupOSSClient(t),
		AllowedExts: []string{"png", "jpg"},
	}, nil)

	for _, ext := range []string{"exe", "php", "", "svg"} {
		_, err := uc.GetGroupAvatarUploadToken(t.Context(), "uid-1", conv.ID, ext)
		if !errors.Is(err, ErrUploadExtNotAllowed) {
			t.Fatalf("后缀 %q 期望 ErrUploadExtNotAllowed，实际 %v", ext, err)
		}
	}
}

// TestGroupAvatarTokenSingleChat 单聊不支持群头像。
func TestGroupAvatarTokenSingleChat(t *testing.T) {
	repo, conv := newGroupWithOwner(t, "uid-1")
	// 把会话改成单聊 —— repo 里存的是同一个指针。
	repo.byID[conv.ID].Type = ConversationTypeSingle

	uc := NewConversationUsecase(repo, nil, nil, nil, StorageOptions{
		Client:      testGroupOSSClient(t),
		AllowedExts: []string{"png"},
	}, nil)
	_, err := uc.GetGroupAvatarUploadToken(t.Context(), "uid-1", conv.ID, "png")
	if !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("期望 ErrConversationNotGroup，实际 %v", err)
	}
}

// TestGroupAvatarURLHostAndPrefix 群头像地址必须在本群目录、且域名在白名单内。
func TestGroupAvatarURLHostAndPrefix(t *testing.T) {
	uc := NewConversationUsecase(newFakeConvRepo(), nil, nil, nil, StorageOptions{
		Client:      testGroupOSSClient(t),
		AllowedExts: []string{"png"},
	}, nil)

	bad := []struct {
		name string
		raw  string
	}{
		{"别人的目录", "https://cdn.example.com/group_avatar/999/a.png"},
		{"个人头像目录", "https://cdn.example.com/avatar/u1/a.png"},
		{"host 不对", "https://evil.example.com/group_avatar/62/a.png"},
		{"后缀不对", "https://cdn.example.com/group_avatar/62/a.exe"},
		{"不是 http", "ftp://cdn.example.com/group_avatar/62/a.png"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := uc.ValidateGroupAvatarURL(t.Context(), 62, tc.raw); err == nil {
				t.Error("应该被拒")
			}
		})
	}
}
