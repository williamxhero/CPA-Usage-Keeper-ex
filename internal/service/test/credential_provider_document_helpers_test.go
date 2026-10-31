package test

import (
	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
	"encoding/json"
)

// 测试 stub 的输入仍是内部投影；构造 v8 文档以验证服务实际写出的分组位置。
func testProviderDocument(entries []providerconfig.ProviderKeyConfig) *providerconfig.Document {
	document := &providerconfig.Document{}
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry)
		var fields map[string]json.RawMessage
		json.Unmarshal(encoded, &fields)
		group := providerconfig.Group{Fields: map[string]json.RawMessage{"base-url": fields["base-url"]}, Keys: []providerconfig.Key{{Fields: fields}}}
		delete(fields, "base-url")
		document.Groups = append(document.Groups, group)
	}
	return document
}

func testOpenAIDocument(providers []providerconfig.OpenAICompatibilityConfig) *providerconfig.Document {
	encoded, _ := json.Marshal(providers)
	var document providerconfig.Document
	json.Unmarshal(encoded, &document)
	return &document
}
