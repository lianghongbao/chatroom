package data

import (
	"context"

	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/data/ent"
	"chatroom/app/user/service/internal/data/ent/user"
)

// userRepo biz.UserRepo 的 ent 实现
type userRepo struct {
	data *Data
}

// NewUserRepo 构造 biz.UserRepo 的 ent 实现；wire 注入
func NewUserRepo(data *Data) biz.UserRepo {
	return &userRepo{data: data}
}

// toBizUser ent.User PO → biz.User DO
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

// Save 新建用户；撞 username 唯一索引时折叠为 ErrUserAlreadyExists，兜底 Register 并发竞态
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

// FindByID 按主键查用户；找不到返 ErrUserNotFound；返回的 DO PasswordHash 已清空
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

// FindByUsername 按用户名查用户；找不到返 ErrUserNotFound；返回的 DO 保留 PasswordHash（供 Login bcrypt 比对）
func (r *userRepo) FindByUsername(ctx context.Context, username string) (*biz.User, error) {
	po, err := r.data.db.User.Query().Where(user.UsernameEQ(username)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrUserNotFound
		}
		return nil, err
	}
	// clearHash=false：Login bcrypt 校验需要
	return toBizUser(po, false), nil
}

// FindByIDs 批量按 ID 查用户。空 ids 返空切片；找不到 / id=0 在结果中不出现，不返错。返回顺序与 ids 无关
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

// Update 旧全量更新接口，内部走 UpdateFields（兼容老调用方）
func (r *userRepo) Update(ctx context.Context, id string, nickname, avatar string) (*biz.User, error) {
	return r.UpdateFields(ctx, id, biz.UserUpdateFields{Nickname: &nickname, Avatar: &avatar})
}

// UpdateFields 字段粒度更新；只 Set 指针非 nil 的字段，ent 的 SetXxx 未调用即不改该列
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

// UpdatePassword 单独写 password_hash 列；改密不与 UpdateFields 共路径，语义隔离
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

// UpdateStatus 改账号状态；找不到返 ErrUserNotFound
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
