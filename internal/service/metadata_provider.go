package service

import (
	"context"
	"fmt"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service/providermetadata"
	"gorm.io/gorm"
)

// persistProviderMetadata 把确定性 snapshot 映射为 AI Provider identities，并区分持久化错误与 fetch warning。
func persistProviderMetadata(ctx context.Context, db *gorm.DB, snapshot providermetadata.Snapshot, fetchErr error, now time.Time) (error, error) {
	return persistProviderMetadataWithReplacement(ctx, db, snapshot, fetchErr, now, false)
}

func persistProviderMetadataInTransaction(ctx context.Context, tx *gorm.DB, snapshot providermetadata.Snapshot, fetchErr error, now time.Time) (error, error) {
	return persistProviderMetadataWithReplacement(ctx, tx, snapshot, fetchErr, now, true)
}

func persistProviderMetadataWithReplacement(ctx context.Context, db *gorm.DB, snapshot providermetadata.Snapshot, fetchErr error, now time.Time, inTransaction bool) (error, error) {
	// nil 数据库属于 persistence error，并沿用旧逻辑抑制本轮 provider fetch warning。
	if db == nil {
		// 第一个返回值参与 upsert error，第二个 warning 为空。
		return fmt.Errorf("database is nil"), nil
	}
	// identities 按 snapshot 的 registry/source 顺序预分配，SQLite 新行自然沿用该顺序写入。
	identities := make([]entities.UsageIdentity, 0, len(snapshot.Credentials))
	// A transient failed source is not new negative identity evidence for an
	// already saved exact binding. Preserve its last proved status; genuinely
	// new/ambiguous claims remain unknown/ambiguous until a complete refresh.
	previous := make(map[string]string)
	if fetchErr != nil {
		oldIdentities, subjects, err := repository.LoadCredentialPricingDirectory(db.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("load previous provider binding evidence"), nil
		}
		for _, old := range oldIdentities {
			for _, subject := range subjects {
				if !subject.BindingDisabled && old.AuthType == entities.UsageIdentityAuthTypeAIProvider && subject.AuthType == old.AuthType && subject.Identity == old.Identity && subject.AuthTypeName == old.AuthTypeName {
					previous[old.Identity] = old.BindingIdentityStatus
				}
			}
		}
	}
	// 每条 Credential 已在纯包完成必填校验和精确 auth-index 去重。
	for _, credential := range snapshot.Credentials {
		// 单条数据库 identity 严格按字段来源单向映射。
		identity := entities.UsageIdentity{
			// Name 只接收 source 归一化后的 CPA name 或固定默认名。
			Name: credential.DisplayName,
			// AuthType 固定为 AI Provider，与 OAuth Credential 隔离。
			AuthType: entities.UsageIdentityAuthTypeAIProvider,
			// AuthTypeName 与 usage_events 的 apikey 关联规则保持一致。
			AuthTypeName: "apikey",
			// Identity 只接收 CPA auth-index，不接收 API Key、prefix 或展示名。
			Identity: credential.AuthIndex,
			// Type 保留 source 的原始 provider type，包括 gemini-interactions、xai 与 meta。
			Type: credential.ProviderType,
			// Provider 沿用既有行为，与最终展示名一致。
			Provider: credential.DisplayName,
			// LookupKey 是 CPA api-key 的唯一目标字段，不进入对外 API DTO。
			LookupKey: credential.LookupKey,
			// Prefix 只接收 CPA 独立 prefix 字段。
			Prefix: credential.Prefix,
			// BaseURL 只接收 CPA 独立 base-url 字段。
			BaseURL: credential.BaseURL,
			// Priority 原样保留 CPA nullable 语义。
			Priority: credential.Priority,
			// Disabled 原样保留 CPA nullable 语义。
			Disabled: credential.Disabled,
			// Note 原样保留 CPA nullable 语义。
			Note: credential.Note,
		}
		identity.BindingIdentityStatus = "unique"
		if fetchErr != nil {
			// A failed source may contain another claim; partial success cannot prove uniqueness.
			identity.BindingIdentityStatus = "unknown"
			if status := previous[identity.Identity]; status == "unique" || status == "ambiguous" {
				identity.BindingIdentityStatus = status
			}
		}
		if credential.Ambiguous {
			identity.BindingIdentityStatus = "ambiguous"
		}
		// 按稳定 snapshot 顺序加入本轮单事务输入。
		identities = append(identities, identity)
	}
	// 一次 scoped replace 接收全部成功 provider types，保留旧单事务 stale/restore 语义。
	var err error
	if inTransaction {
		err = repository.ReplaceUsageIdentitiesForProviderTypesInTransaction(ctx, db, identities, snapshot.FetchedProviderTypes, now)
	} else {
		err = repository.ReplaceUsageIdentitiesForProviderTypes(ctx, db, identities, snapshot.FetchedProviderTypes, now)
	}
	if err != nil {
		// persistence error 回滚 provider 单事务，并抑制同轮 fetch warning。
		return fmt.Errorf("sync provider usage identities: %w", err), nil
	}
	// provider 写入成功后才把并发 fetch warning 返回给外层编排。
	if fetchErr != nil {
		// 保持旧错误分类前缀，内部 warning 已按 registry 稳定归并。
		return nil, fmt.Errorf("fetch provider metadata: %w", fetchErr)
	}
	// 全部 provider 成功且持久化完成时不返回错误。
	return nil, nil
}
