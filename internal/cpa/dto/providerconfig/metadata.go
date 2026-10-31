package providerconfig

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ProviderKeyConfig 是分组展开后的有效成员视图，不用于回写 CPA 原始配置。
type ProviderKeyConfig struct {
	APIKey         string   `json:"api-key"`
	Prefix         string   `json:"prefix"`
	Name           string   `json:"name"`
	BaseURL        string   `json:"base-url"`
	AuthIndex      string   `json:"auth_index"`
	Priority       *int     `json:"priority"`
	Disabled       *bool    `json:"disabled"`
	ExcludedModels []string `json:"excluded-models"`
	Note           *string  `json:"note"`
}

// OpenAICompatibilityConfig 保留组级配置，成员只提供当前 Key 与运行时标识。
type OpenAICompatibilityConfig struct {
	Name          string              `json:"name"`
	Prefix        string              `json:"prefix"`
	BaseURL       string              `json:"base-url"`
	Priority      *int                `json:"priority"`
	Disabled      *bool               `json:"disabled"`
	Note          *string             `json:"note"`
	APIKeyEntries []OpenAIApiKeyEntry `json:"keys"`
}

type OpenAIApiKeyEntry struct {
	APIKey    string `json:"api-key"`
	AuthIndex string `json:"auth_index"`
}

// ProviderKeys 按组、成员原序展开，供现有平铺列表和身份同步使用。
// 只复现 Keeper 消费字段的继承规则；配置写回始终使用 Document，不使用这些计算后的值。
func (d *Document) ProviderKeys(defaultBaseURL string) ([]ProviderKeyConfig, error) {
	keys := make([]ProviderKeyConfig, 0)
	for groupIndex, group := range d.Groups {
		var shared struct {
			Prefix         string   `json:"prefix"`
			BaseURL        string   `json:"base-url"`
			Priority       *int     `json:"priority"`
			ExcludedModels []string `json:"excluded-models"`
		}
		if err := decodeFields(group.Fields, &shared, "prefix", "base-url", "excluded-models"); err != nil {
			return nil, fmt.Errorf("group %d: %w", groupIndex, err)
		}
		// 地址由组统一提供；缺省地址仅用于有效值，不补写到原始配置。
		baseURL := strings.TrimSpace(shared.BaseURL)
		if baseURL == "" {
			baseURL = defaultBaseURL
		}
		for keyIndex, key := range group.Keys {
			var member ProviderKeyConfig
			if err := decodeFields(key.Fields, &member, "api-key", "prefix", "name", "base-url", "note", "excluded-models"); err != nil {
				return nil, fmt.Errorf("group %d key %d: %w", groupIndex, keyIndex, err)
			}
			// 缺省与 null 继承；显式 0、空字符串和空数组保持成员覆盖语义。
			if inherits(key.Fields["prefix"]) {
				member.Prefix = shared.Prefix
			}
			if inherits(key.Fields["priority"]) {
				member.Priority = shared.Priority
			}
			if inherits(key.Fields["excluded-models"]) {
				member.ExcludedModels = shared.ExcludedModels
			}
			if member.Priority == nil {
				priority := 0
				member.Priority = &priority
			}
			member.Prefix = normalizedPrefix(member.Prefix)
			member.ExcludedModels = normalizedExcludedModels(member.ExcludedModels)
			// 标准供应商的成员地址不生效；启停从有效排除规则中的精确 * 推导。
			member.BaseURL = baseURL
			disabled := slices.Contains(member.ExcludedModels, "*")
			member.Disabled = &disabled
			keys = append(keys, member)
		}
	}
	return keys, nil
}

// OpenAIProviders 保持组与成员位置一一对应，供组级优先级编辑定位及更新本地成员。
// 名称、地址、优先级和停用状态来自组；成员仅提供 Key 和运行时标识。
func (d *Document) OpenAIProviders() ([]OpenAICompatibilityConfig, error) {
	providers := make([]OpenAICompatibilityConfig, 0, len(d.Groups))
	for index, group := range d.Groups {
		var provider OpenAICompatibilityConfig
		// 成员另行投影，避免组结构中的 keys 先触发严格字符串解码。
		groupFields := cloneFields(group.Fields)
		delete(groupFields, "keys")
		if err := decodeFields(groupFields, &provider, "name", "prefix", "base-url", "note"); err != nil {
			return nil, fmt.Errorf("group %d: %w", index, err)
		}
		if provider.Priority == nil {
			priority := 0
			provider.Priority = &priority
		}
		if provider.Disabled == nil {
			disabled := false
			provider.Disabled = &disabled
		}
		provider.Prefix = normalizedPrefix(provider.Prefix)
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		provider.APIKeyEntries = make([]OpenAIApiKeyEntry, 0, len(group.Keys))
		for _, key := range group.Keys {
			var entry OpenAIApiKeyEntry
			if err := decodeFields(key.Fields, &entry, "api-key"); err != nil {
				return nil, fmt.Errorf("group %d key: %w", index, err)
			}
			provider.APIKeyEntries = append(provider.APIKeyEntries, entry)
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func decodeFields(fields map[string]json.RawMessage, target any, textFields ...string) error {
	// 只转换调用方列出的文字字段，避免把 priority、disabled、auth_index 等合同错误隐式纠正。
	// 转换发生在副本中，GET 原文和后续写回值不受影响。
	projection := cloneFields(fields)
	for _, field := range textFields {
		raw := projection[field]
		if inherits(raw) {
			continue
		}
		var converted json.RawMessage
		var err error
		if field == "excluded-models" {
			converted, err = projectTextList(raw)
		} else {
			converted, err = projectTextValue(raw)
		}
		if err != nil {
			return fmt.Errorf("field %s: %w", field, err)
		}
		projection[field] = converted
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// projectTextValue 保持字符串/null 的语义，数字直接取 json.Number 文本，避免大整数丢精度。
// 以 CPA 返回值为准，不猜测 YAML 原写法；布尔值、对象、数组不是合法文字标量。
func projectTextValue(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || strings.HasPrefix(trimmed, "\"") {
		return raw, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil, err
	}
	return json.Marshal(number.String())
}

// projectTextList 逐项处理文字数组，不放宽数组本身的结构；空数组仍保留显式覆盖语义。
func projectTextList(raw json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	for index, item := range items {
		converted, err := projectTextValue(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		items[index] = converted
	}
	return json.Marshal(items)
}

// normalizedPrefix 复现 CPA 的有效前缀：去首尾斜杠与空白，含内部斜杠时不使用前缀。
// 归一化仅影响读取视图，避免页面展示的路由前缀与 CPA 实际使用值不同。
func normalizedPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if strings.Contains(prefix, "/") {
		return ""
	}
	return prefix
}

// normalizedExcludedModels 保持首次出现顺序，去空白、小写化并去重，与 CPA 有效规则一致。
func normalizedExcludedModels(models []string) []string {
	if models == nil {
		return nil
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		model := strings.ToLower(strings.TrimSpace(raw))
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
