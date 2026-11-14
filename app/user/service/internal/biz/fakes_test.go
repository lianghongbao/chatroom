package biz

import (
	"context"
	"testing"
	"time"
)

// fakeUserRepo 是 UserRepo 的内存替身。uid 由计数器分配，形如 "uid-1"。
type fakeUserRepo struct {
	// users 是 uid → 用户记录。
	users map[string]*User
	// seq 是 uid 计数器。
	seq int
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: map[string]*User{}}
}

// nextUID 分配下一个 uid，返回形如 "uid-2" 的字符串。
func (r *fakeUserRepo) nextUID() string {
	r.seq++
	return "uid-" + itoa(r.seq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func (r *fakeUserRepo) Save(ctx context.Context, u *User) (string, error) {
	if u.ID == "" {
		u.ID = r.nextUID()
	}
	u.Status = UserStatusNormal
	r.users[u.ID] = u
	return u.ID, nil
}

func (r *fakeUserRepo) FindByID(ctx context.Context, id string) (*User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) FindByIDWithHash(ctx context.Context, id string) (*User, error) {
	return r.FindByID(ctx, id)
}

func (r *fakeUserRepo) FindByUsername(ctx context.Context, username string) (*User, error) {
	for _, u := range r.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, ErrUserNotFound
}

func (r *fakeUserRepo) FindByPhone(ctx context.Context, phone string) (*User, error) {
	for _, u := range r.users {
		if u.Phone == phone {
			return u, nil
		}
	}
	return nil, ErrUserNotFound
}

func (r *fakeUserRepo) FindByIDs(ctx context.Context, ids []string) ([]*User, error) {
	out := make([]*User, 0, len(ids))
	for _, id := range ids {
		if u, ok := r.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (r *fakeUserRepo) Update(ctx context.Context, id string, nickname, avatar string) (*User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	u.Nickname = nickname
	u.Avatar = avatar
	return u, nil
}

func (r *fakeUserRepo) UpdateFields(ctx context.Context, id string, fields UserUpdateFields) (*User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	if fields.Nickname != nil {
		u.Nickname = *fields.Nickname
	}
	if fields.Avatar != nil {
		u.Avatar = *fields.Avatar
	}
	if fields.Phone != nil {
		u.Phone = *fields.Phone
	}
	if fields.Email != nil {
		u.Email = *fields.Email
	}
	return u, nil
}

func (r *fakeUserRepo) UpdatePassword(ctx context.Context, id string, newHash string) error {
	u, ok := r.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.PasswordHash = newHash
	return nil
}

func (r *fakeUserRepo) UpdateStatus(ctx context.Context, id string, status UserStatus) error {
	u, ok := r.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.Status = status
	return nil
}

// seedUser 造一个已知用户。username 是用户名，password 是明文密码；
// 密码按 bcrypt 存，昵称与用户名相同。
func seedUser(t *testing.T, repo *fakeUserRepo, username, password string) *User {
	t.Helper()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	u := &User{
		ID:           repo.nextUID(),
		Username:     username,
		Nickname:     username,
		PasswordHash: hash,
		Status:       UserStatusNormal,
		CreatedAt:    time.Now(),
	}
	repo.users[u.ID] = u
	return u
}

// fakeTokenStore 是 TokenStore 的内存替身。
type fakeTokenStore struct {
	// blacklist 是被拉黑的 access token jti。
	blacklist map[string]bool
	// refreshByJTI 是存活的 refresh token jti → uid。
	refreshByJTI map[string]string
	// revokeCt 统计 RevokeRefresh 被调用的次数。
	revokeCt int
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{
		blacklist:    map[string]bool{},
		refreshByJTI: map[string]string{},
	}
}

func (s *fakeTokenStore) Blacklist(ctx context.Context, jti string, ttl time.Duration) error {
	s.blacklist[jti] = true
	return nil
}

func (s *fakeTokenStore) IsBlacklisted(ctx context.Context, jti string) (bool, error) {
	return s.blacklist[jti], nil
}

func (s *fakeTokenStore) SaveRefresh(ctx context.Context, jti string, uid string, userAgent string, ttl time.Duration) error {
	s.refreshByJTI[jti] = uid
	return nil
}

func (s *fakeTokenStore) ConsumeRefresh(ctx context.Context, jti string) (string, error) {
	uid, ok := s.refreshByJTI[jti]
	if !ok {
		return "", ErrRefreshInvalid
	}
	delete(s.refreshByJTI, jti)
	return uid, nil
}

func (s *fakeTokenStore) RevokeRefresh(ctx context.Context, jti string) error {
	s.revokeCt++
	delete(s.refreshByJTI, jti)
	return nil
}
