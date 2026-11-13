package data

import (
	"context"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/data/ent"
	"chatroom/app/user/service/internal/data/ent/user"
)

// userRepo biz.UserRepo 的 ent 实现。
type userRepo struct {
	data *Data
}

// NewUserRepo 构造 biz.UserRepo 的 ent 实现。data 是共享的存储客户端。
func NewUserRepo(data *Data) biz.UserRepo {
	return &userRepo{data: data}
}

// toBizUser ent.User PO → biz.User DO。
// po ent 记录
// clearHash true 时清掉 PasswordHash
func toBizUser(po *ent.User, clearHash bool) *biz.User {
	if po == nil {
		return nil
	}
	hash := po.PasswordHash
	if clearHash {
		hash = ""
	}
	return &biz.User{
		ID:                  po.ID,
		Username:            po.Username,
		Nickname:            po.Nickname,
		Avatar:              po.Avatar,
		Email:               derefString(po.Email),
		Phone:               derefString(po.Phone),
		PasswordHash:        hash,
		IsVisibleToStranger: po.IsVisibleToStranger,
		Status:              biz.UserStatus(po.Status),
		CreatedAt:           po.CreatedAt,
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Save 新建用户并返回新用户 ID。
// 撞 username 唯一索引时折叠为 ErrUserAlreadyExists，兜底 Register 并发竞态。
func (r *userRepo) Save(ctx context.Context, u *biz.User) (string, error) {
	po, err := r.data.db.User.Create().
		SetUsername(u.Username).SetNickname(u.Nickname).
		SetAvatar(u.Avatar).SetPasswordHash(u.PasswordHash).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return "", biz.ErrUserAlreadyExists
		}
		return "", err
	}
	return po.ID, nil
}

// FindByID 按主键查用户。id 是用户 ID；找不到返 ErrUserNotFound；返回的 DO PasswordHash 已清空。
func (r *userRepo) FindByID(ctx context.Context, id string) (*biz.User, error) {
	po, err := r.data.db.User.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrUserNotFound
		}
		return nil, err
	}
	return toBizUser(po, true), nil
}

// FindByIDWithHash 按主键查用户，保留 PasswordHash。id 是用户 ID；找不到返 ErrUserNotFound。
func (r *userRepo) FindByIDWithHash(ctx context.Context, id string) (*biz.User, error) {
	po, err := r.data.db.User.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrUserNotFound
		}
		return nil, err
	}
	return toBizUser(po, false), nil
}

// FindByUsername 按用户名查用户。username 是用户名；找不到返 ErrUserNotFound；
// 返回的 DO 保留 PasswordHash。
func (r *userRepo) FindByUsername(ctx context.Context, username string) (*biz.User, error) {
	po, err := r.data.db.User.Query().Where(user.UsernameEQ(username)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrUserNotFound
		}
		return nil, err
	}
	// clearHash=false：Login 的 bcrypt 校验需要
	return toBizUser(po, false), nil
}

// FindByIDs 批量按 ID 查用户。ids 是用户 ID 列表；空 ids 返空切片；
// 找不到的 id 在结果中不出现，不返错。返回顺序与 ids 无关。
func (r *userRepo) FindByIDs(ctx context.Context, ids []string) ([]*biz.User, error) {
	if len(ids) == 0 {
		return []*biz.User{}, nil
	}
	pos, err := r.data.db.User.Query().Where(user.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.User, 0, len(pos))
	for _, po := range pos {
		out = append(out, toBizUser(po, true))
	}
	return out, nil
}

// Update 全量更新资料，内部走 UpdateFields。
// id 用户 ID
// nickname 昵称
// avatar 头像地址
func (r *userRepo) Update(ctx context.Context, id string, nickname, avatar string) (*biz.User, error) {
	return r.UpdateFields(ctx, id, biz.UserUpdateFields{Nickname: &nickname, Avatar: &avatar})
}

// UpdateFields 字段粒度更新。
// id 用户 ID
// fields 待改字段集合；只 Set 指针非 nil 的字段
func (r *userRepo) UpdateFields(ctx context.Context, id string, fields biz.UserUpdateFields) (*biz.User, error) {
	upd := r.data.db.User.UpdateOneID(id)
	if fields.Nickname != nil {
		upd.SetNickname(*fields.Nickname)
	}
	if fields.Avatar != nil {
		upd.SetAvatar(*fields.Avatar)
	}
	po, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrUserNotFound
		}
		return nil, err
	}
	return toBizUser(po, true), nil
}

// UpdatePassword 单独写 password_hash 列。
// id 用户 ID
// newHash 新的 bcrypt 哈希
func (r *userRepo) UpdatePassword(ctx context.Context, id string, newHash string) error {
	_, err := r.data.db.User.UpdateOneID(id).SetPasswordHash(newHash).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return biz.ErrUserNotFound
		}
		return err
	}
	return nil
}

// UpdateStatus 改账号状态。
// id 用户 ID
// status 目标状态
// 找不到返 ErrUserNotFound
func (r *userRepo) UpdateStatus(ctx context.Context, id string, status biz.UserStatus) error {
	_, err := r.data.db.User.UpdateOneID(id).SetStatus(uint8(status)).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return biz.ErrUserNotFound
		}
		return err
	}
	return nil
}
