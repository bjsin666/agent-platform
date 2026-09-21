package tools

import (
	"encoding/json"
	"testing"
)

func TestRegistryRegisterGet(t *testing.T) {
	r := NewRegistry()
	tool := &Tool{Name: "echo", Description: "测试", Parameters: json.RawMessage(`{}`)}
	if err := r.Register(tool); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if err := r.Register(tool); err == nil {
		t.Fatal("重名注册应报错")
	}
	if err := r.Register(&Tool{Name: ""}); err == nil {
		t.Fatal("空名注册应报错")
	}
	got, ok := r.Get("echo")
	if !ok || got != tool {
		t.Fatalf("Get 失败: %v %v", got, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("不存在的工具不应命中")
	}
}

func TestRegistryListSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"b", "a", "c"} {
		r.Register(&Tool{Name: n})
	}
	list := r.List()
	if len(list) != 3 || list[0].Name != "a" || list[2].Name != "c" {
		t.Fatalf("List 应按名排序: %v", list)
	}
}

func TestRegistrySchemas(t *testing.T) {
	r := NewRegistry()
	r.Register(&Tool{
		Name:        "echo",
		Description: "回显",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	})
	schemas := r.Schemas()
	if len(schemas) != 1 {
		t.Fatalf("Schemas 数量错误: %d", len(schemas))
	}
	s := schemas[0]
	if s.Type != "function" || s.Function.Name != "echo" || s.Function.Description != "回显" {
		t.Fatalf("Schema 内容错误: %+v", s)
	}
	if string(s.Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("Parameters 未透传: %s", s.Function.Parameters)
	}
}
