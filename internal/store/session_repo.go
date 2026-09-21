package store

import (
	"context"
	"errors"

	"agent-platform/internal/model"

	"gorm.io/gorm"
)

// SessionRepo 基于 GORM 的会话存取。
type SessionRepo struct {
	db *gorm.DB
}

// NewSessionRepo 构造会话仓储。
func NewSessionRepo(db *gorm.DB) *SessionRepo { return &SessionRepo{db: db} }

// Create 创建会话,返回含自增 ID 的会话记录。
func (r *SessionRepo) Create(ctx context.Context, title string) (*model.Session, error) {
	s := model.Session{TenantID: "default", Title: title, Status: "active"}
	if err := r.db.WithContext(ctx).Create(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// List 返回最近 limit 个会话(按 ID 倒序=新建优先)。
func (r *SessionRepo) List(ctx context.Context, limit int) ([]model.Session, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var list []model.Session
	if err := r.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Delete 删除会话及其全部消息(级联,事务保证一致)。
func (r *SessionRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", id).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Session{}, id).Error
	})
}

// Get 按 ID 取会话;不存在返回 (nil, nil)。
func (r *SessionRepo) Get(ctx context.Context, id uint64) (*model.Session, error) {
	var s model.Session
	if err := r.db.WithContext(ctx).First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}
