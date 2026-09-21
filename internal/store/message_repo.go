package store

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/model"

	"gorm.io/gorm"
)

// MessageRepo 基于 GORM 的会话消息存取,实现 engine.MessageRepo 接口。
type MessageRepo struct {
	db *gorm.DB
}

// NewMessageRepo 构造消息仓储。
func NewMessageRepo(db *gorm.DB) *MessageRepo { return &MessageRepo{db: db} }

// LoadMessages 按会话加载全部历史消息(按 ID 升序)。
func (r *MessageRepo) LoadMessages(ctx context.Context, sessionID uint64) ([]llm.Message, error) {
	var rows []model.Message
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询会话消息: %w", err)
	}
	out := make([]llm.Message, 0, len(rows))
	for _, row := range rows {
		msg := llm.Message{Role: row.Role, Content: row.Content}
		if len(row.ToolCalls) > 0 {
			var calls []llm.ToolCall
			if err := json.Unmarshal(row.ToolCalls, &calls); err == nil {
				msg.ToolCalls = calls
			}
		}
		out = append(out, msg)
	}
	return out, nil
}

// AppendMessage 持久化一条消息。ToolCallID 不在表结构中(§6),故丢弃。
func (r *MessageRepo) AppendMessage(ctx context.Context, sessionID uint64, msg llm.Message) error {
	row := model.Message{
		SessionID: sessionID,
		Role:      msg.Role,
		Content:   msg.Content,
	}
	if len(msg.ToolCalls) > 0 {
		buf, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("序列化 tool_calls: %w", err)
		}
		row.ToolCalls = buf
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("写入会话消息: %w", err)
	}
	return nil
}
