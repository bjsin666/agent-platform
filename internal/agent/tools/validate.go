package tools

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// validator 参数 JSON Schema 校验器,缓存编译结果避免重复编译。
type validator struct {
	mu    sync.Mutex
	cache map[string]*jsonschema.Schema // key: 原始 schema JSON
}

func newValidator() *validator {
	return &validator{cache: map[string]*jsonschema.Schema{}}
}

// validate 用 JSON Schema 校验 args。schema 为空表示无参,跳过校验。
func (v *validator) validate(schema, args json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	sch, err := v.compile(schema)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(args, &doc); err != nil {
		return fmt.Errorf("解析参数 JSON: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("参数校验失败: %w", err)
	}
	return nil
}

// compile 取缓存,未命中则编译。编译后的 Schema 不可变,可并发 Validate。
func (v *validator) compile(schema json.RawMessage) (*jsonschema.Schema, error) {
	key := string(schema)
	v.mu.Lock()
	defer v.mu.Unlock()
	if sch, ok := v.cache[key]; ok {
		return sch, nil
	}
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("解析工具 schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("tool-schema", doc); err != nil {
		return nil, fmt.Errorf("加载工具 schema: %w", err)
	}
	sch, err := compiler.Compile("tool-schema")
	if err != nil {
		return nil, fmt.Errorf("编译工具 schema: %w", err)
	}
	v.cache[key] = sch
	return sch, nil
}
