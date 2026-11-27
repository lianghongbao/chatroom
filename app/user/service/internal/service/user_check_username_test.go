package service

import (
	"context"
	"testing"

	pb "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/biz"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// TestServiceCheckUsernameAvailable_NilRequest nil 请求返参数错误。
func TestServiceCheckUsernameAvailable_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.CheckUsernameAvailable(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceCheckUsernameAvailable_EmptyUsername 空用户名返参数错误。
func TestServiceCheckUsernameAvailable_EmptyUsername(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	_, err := s.CheckUsernameAvailable(context.Background(), &pb.CheckUsernameAvailableRequest{Username: ""})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceCheckUsernameAvailable_Available 没人占用时 available=true。
func TestServiceCheckUsernameAvailable_Available(t *testing.T) {
	repo := &fakeRepo{}
	// fake repo 查不到人时返回 nil user + nil error —— biz 把它当成「没人占用」。
	repo.findByUsernameRet = nil
	s := newServiceWithRepo(repo)

	reply, err := s.CheckUsernameAvailable(context.Background(), &pb.CheckUsernameAvailableRequest{Username: "bob"})
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if !reply.GetAvailable() {
		t.Errorf("available = false, want true")
	}
}

// TestServiceCheckUsernameAvailable_Taken 已被占用时 available=false。
func TestServiceCheckUsernameAvailable_Taken(t *testing.T) {
	repo := &fakeRepo{}
	repo.findByUsernameRet = &biz.User{ID: "uid-1", Username: "alice"}
	s := newServiceWithRepo(repo)

	reply, err := s.CheckUsernameAvailable(context.Background(), &pb.CheckUsernameAvailableRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if reply.GetAvailable() {
		t.Errorf("available = true, want false")
	}
}

// TestServiceCheckUsernameAvailable_AlwaysSetsAvailable available 必须总是被赋值。
//
// 契约里它是 optional bool（指针）而不是裸 bool：HTTP 响应走标准
// encoding/json codec，裸 bool 的 tag 带 omitempty —— false 会被整个省略，
// 前端拿到 undefined 而不是 false，于是「已被占用」会被当成「可用」。
// optional 让字段变成指针，非 nil 就一定会出现在 JSON 里。
func TestServiceCheckUsernameAvailable_AlwaysSetsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo *fakeRepo
		want bool
	}{
		{"可用", &fakeRepo{}, true},
		{"被占用", &fakeRepo{findByUsernameRet: &biz.User{ID: "uid-1", Username: "alice"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServiceWithRepo(tc.repo)
			reply, err := s.CheckUsernameAvailable(context.Background(), &pb.CheckUsernameAvailableRequest{Username: "x"})
			if err != nil {
				t.Fatalf("CheckUsernameAvailable: %v", err)
			}
			if reply.Available == nil {
				t.Fatalf("Available 是 nil —— JSON 里会被省略，前端拿不到结论")
			}
			if reply.GetAvailable() != tc.want {
				t.Errorf("available = %v, want %v", reply.GetAvailable(), tc.want)
			}
		})
	}
}
