package providermetadata

import "context"

// sourceFetch 把单个 CPA endpoint 转成统一来源结果。
type sourceFetch func(context.Context, Fetcher) sourceResult

// source 定义 registry 中一个固定 endpoint 的维护边界。
type source struct {
	// id 是 registry 内部稳定名称，也是 warning 的来源标签。
	id string
	// providerType 是写入 Snapshot 与 usage identity 的 CPA 原始类型。
	providerType string
	// defaultDisplayName 在 CPA entry 没有 name 时提供稳定展示名。
	defaultDisplayName string
	// warningName 保留现有来源错误文案中的 endpoint 业务名称。
	warningName string
	// fetch 绑定当前 endpoint 的实际读取与归一化函数。
	fetch sourceFetch
}

// sourceResult 保存一个 endpoint 的凭证、成功状态和独立 warning。
type sourceResult struct {
	// credentials 只包含通过必填字段校验的凭证。
	credentials []Credential
	// fetched 表示读取成功且凭证标识完整；空列表或仅含空 Key 时仍为 true。
	fetched bool
	// warning 保存该来源的读取、解码或身份校验错误。
	warning error
}
