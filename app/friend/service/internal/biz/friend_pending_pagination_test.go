package biz

import (
	"context"
	"testing"
)

// TestFriendUsecase_ListPendingRequests_CursorDoesNotDuplicate
// 收件箱翻页不该重复返回同一条记录。
//
// ListPendingRequests 用 limit+1 的哨兵多取一条判断还有没有下一页，
// 但 next_cursor 若取「多取的那条」的 id —— 下一页从 id > cursor 开始，
// 那条哨兵记录会被再返回一次。
func TestFriendUsecase_ListPendingRequests_CursorDoesNotDuplicate(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	insert := func(user, friend string, status FriendStatus) uint64 {
		id := repo.nextID
		repo.nextID = id + 1
		f := &Friend{ID: id, UserID: user, FriendID: friend, Status: status}
		repo.byPair[encodePair(user, friend)] = f
		repo.byID[id] = f
		return id
	}
	// 5 条 PENDING 收件，每页 2 条 —— 会翻 3 页。
	for _, u := range []string{"uid-2", "uid-3", "uid-4", "uid-5", "uid-6"} {
		insert(u, "uid-1", FriendStatusPending)
	}

	const limit = 2
	seen := map[uint64]int{} // 记录 id 到出现次数的映射。
	var cursor uint64
	pages := 0
	for {
		got, next, err := uc.ListPendingRequests(context.Background(), "uid-1", cursor, limit)
		if err != nil {
			t.Fatalf("ListPendingRequests: %v", err)
		}
		pages++
		for _, f := range got {
			seen[f.ID]++
		}
		if next == 0 {
			break
		}
		if pages > 10 {
			t.Fatal("翻页没终止")
		}
		cursor = next
	}

	// 每条记录恰好出现一次。
	for id, n := range seen {
		if n != 1 {
			t.Errorf("记录 id=%d 出现了 %d 次, want 1（next_cursor 指错了）", id, n)
		}
	}
	// 5 条记录都要拿到。
	if len(seen) != 5 {
		t.Errorf("拿到 %d 条不同记录, want 5", len(seen))
	}
}
