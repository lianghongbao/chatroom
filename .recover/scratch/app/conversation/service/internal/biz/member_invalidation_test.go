package biz

import (
	"context"
	"errors"
	"testing"
)

// fakeMemberPublisher 记录收到的成员变更通知。
type fakeMemberPublisher struct {
	published []uint64
	err       error
}

func (p *fakeMemberPublisher) Publish(_ context.Context, conversationID uint64) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, conversationID)
	return nil
}

// newGroupWithMembers 造一个已落库的群聊。
func newGroupWithMembers(t *testing.T, owner string, members []string) (*ConversationUsecase, *fakeConvRepo, uint64) {
	t.Helper()
	repo := newFakeConvRepo()
	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   owner,
		MemberIDs: members,
		Name:      "群",
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}
	return NewConversationUsecase(repo, nil, nil, nil, nil), repo, conv.ID
}

// TestAddMembers_NotifiesInvalidation 加成员后必须通知网关失效缓存 ——
// 否则新成员最长一个 TTL（默认 1 分钟）收不到消息。
func TestAddMembers_NotifiesInvalidation(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if _, _, err := uc.AddMembers(context.Background(), "uid-1", conv.ID, []string{"uid-3"}); err != nil {
		t.Fatalf("AddMembers: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("通知次数 = %d, want 1", len(pub.published))
	}
	if pub.published[0] != conv.ID {
		t.Errorf("通知的会话 id = %d, want %d", pub.published[0], conv.ID)
	}
}

// TestRemoveMember_NotifiesInvalidation 退群后必须通知 ——
// 退群的人不该再收到消息。
func TestRemoveMember_NotifiesInvalidation(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if _, err := uc.RemoveMember(context.Background(), "uid-1", conv.ID, "uid-2"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("通知次数 = %d, want 1", len(pub.published))
	}
	if pub.published[0] != conv.ID {
		t.Errorf("通知的会话 id = %d, want %d", pub.published[0], conv.ID)
	}
}

// TestLeaveConversation_NotifiesInvalidation 主动退群也要通知。
func TestLeaveConversation_NotifiesInvalidation(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if err := uc.LeaveConversation(context.Background(), "uid-2", conv.ID); err != nil {
		t.Fatalf("LeaveConversation: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("通知次数 = %d, want 1", len(pub.published))
	}
}

// TestDissolveConversation_NotifiesInvalidation 解散群聊清了全部成员 ——
// 缓存必须失效。
func TestDissolveConversation_NotifiesInvalidation(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if err := uc.DissolveConversation(context.Background(), "uid-1", conv.ID); err != nil {
		t.Fatalf("DissolveConversation: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("通知次数 = %d, want 1", len(pub.published))
	}
	if pub.published[0] != conv.ID {
		t.Errorf("通知的会话 id = %d, want %d", pub.published[0], conv.ID)
	}
}

// TestTransferOwner_DoesNotNotify 转让群主不改变成员集合 —— 网关只缓存成员
// 列表，不缓存群主信息，所以不用通知。
func TestTransferOwner_DoesNotNotify(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if _, err := uc.TransferOwner(context.Background(), "uid-1", conv.ID, "uid-2"); err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}

	if len(pub.published) != 0 {
		t.Errorf("通知次数 = %d, want 0", len(pub.published))
	}
}

// TestNotifyMemberChange_PublishFailureDoesNotFailOperation 通知失败不影响
// 成员变更本身的结果 —— 通知只是让网关少缓存一会儿。
func TestNotifyMemberChange_PublishFailureDoesNotFailOperation(t *testing.T) {
	repo := newFakeConvRepo()
	pub := &fakeMemberPublisher{err: errors.New("redis down")}
	uc := NewConversationUsecase(repo, nil, nil, pub, nil)

	conv, _, err := repo.Save(context.Background(), &Conversation{
		Type:      ConversationTypeGroup,
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("造群失败: %v", err)
	}

	if _, _, err := uc.AddMembers(context.Background(), "uid-1", conv.ID, []string{"uid-3"}); err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
}

// TestNilPublisher_NoPanic 没有 publisher 时不能 panic。
func TestNilPublisher_NoPanic(t *testing.T) {
	uc, _, convID := newGroupWithMembers(t, "uid-1", []string{"uid-1", "uid-2"})

	if _, _, err := uc.AddMembers(context.Background(), "uid-1", convID, []string{"uid-3"}); err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
}
