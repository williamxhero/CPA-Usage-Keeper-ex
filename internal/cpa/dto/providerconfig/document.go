package providerconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Document 保留供应商完整配置，供一次读取、修改和整列表写回使用。
// 展示字段另由有效值投影生成；这里保留未知字段和原始数值，避免局部编辑丢掉其他配置。
type Document struct{ Groups []Group }

// Group 同时保留组字段和可编辑成员；序列化时以 Keys 重建 fields 中的 keys。
// 因此成员修改只操作 Keys，无需同步修改原始嵌套数组。
type Group struct {
	Fields map[string]json.RawMessage
	Keys   []Key
}

type Key struct{ Fields map[string]json.RawMessage }

// Location 只在一次 GET/PUT 内定位首个匹配成员，不作为长期身份保存。
type Location struct {
	Group int
	Key   int
}

// UnmarshalJSON 要求组列表和成员列表都是数组、每项都是对象。
// 结构错误直接返回失败，不能当成空配置交给同步流程替换本地身份。
func (d *Document) UnmarshalJSON(data []byte) error {
	var groups []json.RawMessage
	if err := decodeArray(data, &groups); err != nil {
		return err
	}
	d.Groups = make([]Group, 0, len(groups))
	for groupIndex, raw := range groups {
		fields, err := decodeObject(raw)
		if err != nil {
			return fmt.Errorf("group %d: %w", groupIndex, err)
		}
		var rawKeys []json.RawMessage
		if err := decodeArray(fields["keys"], &rawKeys); err != nil {
			return fmt.Errorf("group %d keys: %w", groupIndex, err)
		}
		group := Group{Fields: fields, Keys: make([]Key, 0, len(rawKeys))}
		for keyIndex, rawKey := range rawKeys {
			keyFields, err := decodeObject(rawKey)
			if err != nil {
				return fmt.Errorf("group %d key %d: %w", groupIndex, keyIndex, err)
			}
			group.Keys = append(group.Keys, Key{Fields: keyFields})
		}
		d.Groups = append(d.Groups, group)
	}
	return nil
}

// MarshalJSON 从当前组和成员重建请求，保留未修改字段；空列表显式输出 []。
func (d Document) MarshalJSON() ([]byte, error) {
	groups := make([]map[string]json.RawMessage, 0, len(d.Groups))
	for _, group := range d.Groups {
		fields := cloneFields(group.Fields)
		keys := make([]map[string]json.RawMessage, 0, len(group.Keys))
		for _, key := range group.Keys {
			keys = append(keys, key.Fields)
		}
		encoded, err := json.Marshal(keys)
		if err != nil {
			return nil, err
		}
		fields["keys"] = encoded
		groups = append(groups, fields)
	}
	return json.Marshal(groups)
}

// FindFirst 按上游的组顺序、成员顺序定位首个匹配标识。
// 同一标识可能出现在多处；只编辑首项，不把数组位置保存为长期身份。
func (d *Document) FindFirst(authIndex string) (Location, bool) {
	for groupIndex, group := range d.Groups {
		for keyIndex, key := range group.Keys {
			var index string
			if json.Unmarshal(key.Fields["auth_index"], &index) == nil && strings.TrimSpace(index) == authIndex {
				return Location{Group: groupIndex, Key: keyIndex}, true
			}
		}
	}
	return Location{}, false
}

func decodeArray(data []byte, target any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '[' {
		return fmt.Errorf("expected an array")
	}
	return json.Unmarshal(data, target)
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil, fmt.Errorf("expected an object")
	}
	var fields map[string]json.RawMessage
	err := json.Unmarshal(data, &fields)
	return fields, err
}

// inherits 只将缺省或 null 视为继承；0、空字符串和空数组都属于显式覆盖。
func inherits(data json.RawMessage) bool {
	return len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

// cloneFields 只复制映射；调用方替换字段而不修改 RawMessage 底层字节，保持原文独立。
func cloneFields(fields map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

// SetKeyPriority 写入成员覆盖值，不修改组默认值或其他成员。
func (d *Document) SetKeyPriority(location Location, priority int) {
	d.Groups[location.Group].Keys[location.Key].Fields["priority"] = json.RawMessage(fmt.Sprintf("%d", priority))
}

// SetGroupPriority 用于 OpenAI 兼容供应商，其优先级由整组共享。
func (d *Document) SetGroupPriority(location Location, priority int) {
	d.Groups[location.Group].Fields["priority"] = json.RawMessage(fmt.Sprintf("%d", priority))
}

// SetKeyExcludedModels 写入成员的完整有效规则；空数组表示清空，不能写 null 恢复继承。
func (d *Document) SetKeyExcludedModels(location Location, models []string) error {
	if models == nil {
		models = []string{}
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return err
	}
	d.Groups[location.Group].Keys[location.Key].Fields["excluded-models"] = encoded
	return nil
}

// Writable 在副本中剔除组和成员的运行时只读标识，避免将它们持久化进 CPA 配置。
// 不递归删除：请求头、模型和插件中的同名字段属于用户配置，必须原样保留。
func (d *Document) Writable() Document {
	writable := Document{Groups: make([]Group, 0, len(d.Groups))}
	for _, group := range d.Groups {
		copy := Group{Fields: cloneFields(group.Fields), Keys: make([]Key, 0, len(group.Keys))}
		delete(copy.Fields, "auth_index")
		delete(copy.Fields, "auth-index")
		for _, key := range group.Keys {
			fields := cloneFields(key.Fields)
			delete(fields, "auth_index")
			delete(fields, "auth-index")
			copy.Keys = append(copy.Keys, Key{Fields: fields})
		}
		writable.Groups = append(writable.Groups, copy)
	}
	return writable
}
