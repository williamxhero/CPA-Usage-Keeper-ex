package providermetadata

import (
	"context"

	"cpa-usage-keeper/internal/cpa/dto/response"
)

// geminiInteractionsSource 声明 CPA interactions-api-key endpoint 与独立 provider 类型的边界。
func geminiInteractionsSource() source {
	// Interactions 保持独立来源，配置缺省与失败沿用标准供应商的统一处理。
	return newProviderKeySource("gemini-interactions", "gemini-interactions", "Gemini Interactions", "interactions api keys", func(ctx context.Context, fetcher Fetcher) (*response.ProviderKeyConfigResult, error) {
		// 只调用 Interactions 专属 client 方法。
		return fetcher.FetchInteractionsAPIKeys(ctx)
	})
}
