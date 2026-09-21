package tools

import (
	"fmt"
	"sort"
	"sync"

	"agent-platform/internal/agent/llm"
)

// Registry 工具注册表。启动时注册全部工具,运行期只读。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: map[string]*Tool{}}
}

// Register 注册工具;重名或名称为空返回错误。
func (r *Registry) Register(t *Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t == nil || t.Name == "" {
		return fmt.Errorf("工具名不能为空")
	}
	if _, ok := r.tools[t.Name]; ok {
		return fmt.Errorf("工具已存在: %s", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// Get 按名获取工具。
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 返回全部工具,按名排序保证输出稳定。
func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Schemas 转成 LLM 可识别的工具定义(OpenAI tools 格式)。
func (r *Registry) Schemas() []llm.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, llm.ToolSchema{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}
