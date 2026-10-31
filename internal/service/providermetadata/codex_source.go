package providermetadata

import (
	"context"

	"cpa-usage-keeper/internal/cpa/dto/response"
)

// codexSource 声明 CPA codex-api-key endpoint 与 Keeper codex provider 的一一边界。
func codexSource() source {
	// Codex 共用标准供应商的缺省判定，经 CPA 标识确认的缺省由客户端返回成功空来源。
	return newProviderKeySource("codex", "codex", "codex", "codex api keys", func(ctx context.Context, fetcher Fetcher) (*response.ProviderKeyConfigResult, error) {
		// 只调用 Codex 专属 client 方法。
		return fetcher.FetchCodexAPIKeys(ctx)
	})
}
