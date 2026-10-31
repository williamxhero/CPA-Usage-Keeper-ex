package service

import (
	"context"
	"fmt"

	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service/providermetadata"
	"cpa-usage-keeper/internal/timeutil"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// SyncMetadata 同步三类 CPA metadata，并只在三类数据库写入都成功后通知后台 identity 聚合。
func (s *SyncService) SyncMetadata(ctx context.Context) error {
	return s.syncMetadata(ctx, false)
}

// SyncCredentialDirectory performs the complete credential-directory refresh.
// Unlike the legacy metadata path, any source failure aborts before publication,
// preserving the last successful directory and its pricing subjects.
func (s *SyncService) SyncCredentialDirectory(ctx context.Context) error {
	return s.syncMetadata(ctx, true)
}

func (s *SyncService) syncMetadata(ctx context.Context, completeCredentialDirectory bool) error {
	// metadata 入口仍复用原 validate，并保留老构造器的 client 回填行为。
	if err := s.validate(syncMetadataRequired); err != nil {
		// 配置不完整时不发起任何远端读取。
		return err
	}
	// 开始日志保持原有 debug 级别和 message。
	logrus.Debug("metadata sync started")
	// 远端读取前固定版本，覆盖远端快照已返回但本地 diff 尚未读取的竞争窗口。
	credentialVersion := s.credentialMetadata.snapshotVersion()
	// 同一轮 metadata 写入与兼容同步补算共用该时间；后台 Runner 使用自己的事务时间。
	fetchedAt := timeutil.NormalizeStorageTime(s.now())
	// Credentials 先读取，但失败不跳过后续两类 metadata。
	credentialsResult, credentialsErr := s.metadataFetcher.FetchCredentials(ctx)
	// 管理 API Keys 保持第二个读取位置，失败同样不阻止 provider。
	apiKeysResult, apiKeysErr := s.metadataFetcher.FetchManagementAPIKeys(ctx)
	// 八个 provider endpoint 只在纯包内并发，返回按 registry 确定性归并的 snapshot。
	providerSnapshot, providerFetchErr := providermetadata.Fetch(ctx, s.metadataFetcher)
	if completeCredentialDirectory {
		if credentialsErr == nil && credentialsResult == nil {
			credentialsErr = fmt.Errorf("empty response")
		}
		if apiKeysErr == nil && apiKeysResult == nil {
			apiKeysErr = fmt.Errorf("empty response")
		}
		if err := joinErrors(
			wrapMetadataFetchError("fetch credentials", credentialsErr),
			wrapMetadataFetchError("fetch management api keys", apiKeysErr),
			wrapMetadataFetchError("fetch provider metadata", providerFetchErr),
		); err != nil {
			return err
		}
	}
	var upsertErr, providerWarningErr error
	completeApplied := true
	// Fetch without either lock. Serialize DB publication and catalog replacement
	// with pricing writers, then release before notifying or catching up usage.
	func() {
		if s.pricingCatalog != nil {
			s.pricingCatalog.MutationMutex().Lock()
			defer s.pricingCatalog.MutationMutex().Unlock()
		}
		if completeCredentialDirectory {
			// One version permit covers every scope, commit and publication. An
			// intervening completed mutation invalidates the whole remote directory,
			// including management keys, not just the credential/provider subsets.
			completeApplied, upsertErr = s.credentialMetadata.persistSnapshot(credentialVersion, func() error {
				var candidate *pricing.Snapshot
				err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					if err := syncCredentialsInTransaction(ctx, tx, credentialsResult, nil, fetchedAt); err != nil {
						return err
					}
					if err := syncManagementAPIKeysInTransaction(tx, apiKeysResult, nil, fetchedAt); err != nil {
						return err
					}
					if err, _ := persistProviderMetadataInTransaction(ctx, tx, providerSnapshot, nil, fetchedAt); err != nil {
						return err
					}
					if err := repository.EnsureCredentialPricingSubjects(tx, fetchedAt); err != nil {
						return err
					}
					if s.pricingCatalog != nil {
						var loadErr error
						// Compile using the same writer transaction. A load/compile
						// failure must not commit a directory the catalog cannot publish.
						candidate, loadErr = repository.LoadPricingSnapshot(ctx, tx)
						if loadErr != nil {
							return fmt.Errorf("refresh pricing identity evidence failed")
						}
					}
					return nil
				})
				if err == nil && s.pricingCatalog != nil {
					// Never expose candidate identity/subject evidence before COMMIT.
					s.pricingCatalog.Replace(candidate)
				}
				return err
			})
			return
		}
		// Legacy callers keep independent-source commits and management-key
		// progress even when a local credential mutation supersedes the snapshot.
		authApplied, authSyncErr := s.credentialMetadata.persistSnapshot(credentialVersion, func() error {
			return syncCredentials(ctx, s.db, credentialsResult, credentialsErr, fetchedAt)
		})
		if !authApplied && credentialsErr != nil {
			authSyncErr = fmt.Errorf("fetch credentials: %w", credentialsErr)
		}
		apiKeySyncErr := syncManagementAPIKeys(s.db, apiKeysResult, apiKeysErr, fetchedAt)
		providerApplied, providerSyncErr := s.credentialMetadata.persistSnapshot(credentialVersion, func() error {
			var persistErr error
			persistErr, providerWarningErr = persistProviderMetadata(ctx, s.db, providerSnapshot, providerFetchErr, fetchedAt)
			return persistErr
		})
		if !providerApplied && providerFetchErr != nil {
			providerWarningErr = fmt.Errorf("fetch provider metadata: %w", providerFetchErr)
		}
		upsertErr = joinErrors(authSyncErr, apiKeySyncErr, providerSyncErr)
		if s.pricingCatalog != nil {
			snapshot, pricingErr := repository.LoadPricingSnapshot(ctx, s.db)
			if pricingErr == nil {
				s.pricingCatalog.Replace(snapshot)
			} else {
				s.pricingCatalog.Replace(s.pricingCatalog.Snapshot().WithoutCredentialAttribution())
				upsertErr = joinErrors(upsertErr, fmt.Errorf("refresh pricing identity evidence failed"))
			}
		}
	}()
	// aggregateErr 只承接没有 notifier 的兼容同步补算错误。
	var aggregateErr error
	// 任一数据库写入失败都阻止基于半成品 identity 发送通知或执行兼容补算。
	if upsertErr == nil && completeApplied {
		// provider fetch warning 不属于持久化失败，因此仍允许已提交 identity 唤醒后台 runner。
		if s.usageAggregation != nil {
			// notifier 只写内存并发 wake，不在 metadata 热路径执行 SQLite 聚合事务。
			s.usageAggregation.NotifyUsageIdentitiesChanged()
		} else {
			// 没有 notifier 的既有构造路径继续在返回前补算历史 identity 事件。
			aggregateErr = repository.AggregateUsageIdentityStats(ctx, s.db, fetchedAt)
			// 错误包装保持 main 对兼容调用方的稳定文本。
			if aggregateErr != nil {
				aggregateErr = fmt.Errorf("aggregate usage identity stats: %w", aggregateErr)
			}
		}
	}
	// 最终顺序保持 persistence、兼容 aggregate、provider warning；生产后台聚合仍有独立生命周期。
	err := joinErrors(upsertErr, aggregateErr, providerWarningErr)
	// 完成日志默认沿用 completed 状态。
	fields := logrus.Fields{
		// status 字段保持现有 dashboard/runtime 观察语义。
		"status": "completed",
	}
	// 任一 warning 或错误都只改变完成状态，不回滚已提交的独立事务。
	if err != nil {
		// completed_with_warnings 保持原有非 fail-fast 表达。
		fields["status"] = "completed_with_warnings"
		// error 只写稳定合并后的错误；provider client 已保证不泄漏 response body。
		fields["error"] = err.Error()
	}
	// 完成日志保持原有 debug 级别和 message。
	logrus.WithFields(fields).Debug("metadata sync finished")
	// 返回全部可观察错误，同时保留本轮已提交数据。
	return err
}

func wrapMetadataFetchError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
