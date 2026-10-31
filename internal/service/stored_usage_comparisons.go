package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

type StoredUsageComparisonsProvider interface {
	GetStoredUsageComparisons(context.Context, servicedto.StoredUsageComparisonsFilter) (*servicedto.StoredUsageComparisons, error)
}
type storedUsageComparisonsService struct{ db *gorm.DB }

func NewStoredUsageComparisonsService(db *gorm.DB) StoredUsageComparisonsProvider {
	return &storedUsageComparisonsService{db}
}

type StoredUsageComparisonsValidationError struct{}

func (*StoredUsageComparisonsValidationError) Error() string {
	return "invalid stored comparison filter"
}
func (s *storedUsageComparisonsService) GetStoredUsageComparisons(ctx context.Context, f servicedto.StoredUsageComparisonsFilter) (*servicedto.StoredUsageComparisons, error) {
	ctx = usageServiceContext(ctx)
	valid := f.StartTime != nil && f.EndTime != nil && !f.StartTime.IsZero() && !f.EndTime.IsZero() && f.StartTime.Before(*f.EndTime)
	if f.Range == "custom" && valid {
		// Internal exact windows follow the stored overview's bounded contract.
		// Public HTTP parsing still requires an explicit hour/day custom unit.
		valid = (f.CustomUnit == "" || f.CustomUnit == "hour") && f.EndTime.Sub(*f.StartTime) <= 24*time.Hour || f.CustomUnit == "day" && timeutil.DateOf(*f.StartTime).DaysUntil(timeutil.DateOf(*f.EndTime)) <= timeutil.LongCustomDayRangeMaxDays
	} else if f.Range != "today" && f.Range != "yesterday" {
		_, ok := timeutil.ParseUsageRollingRange(f.Range)
		valid = valid && ok
	}
	if !valid {
		return nil, &StoredUsageComparisonsValidationError{}
	}
	if s.db == nil {
		return nil, fmt.Errorf("stored comparison database is nil")
	}
	key, err := (&usageService{db: s.db}).resolveAPIGroupKey(ctx, f.APIKeyID)
	if err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx).Clauses(dbresolver.Read)
	reader, err := repository.NewStoredUsageComparisonReader(db)
	if err != nil {
		return nil, err
	}
	record, err := reader.Read(repodto.UsageQueryFilter{Range: f.Range, CustomUnit: f.CustomUnit, StartTime: f.StartTime, EndTime: f.EndTime, EndExclusive: f.EndExclusive, QueryNow: f.QueryNow, APIGroupKey: key})
	if err != nil {
		return nil, err
	}
	// Directory IDs are the existing public numeric IDs, not hashes of credentials.
	// No pricing subject/config/catalog/channel queries and no implicit binding.
	keys := []entities.CPAAPIKey{}
	identities := []entities.UsageIdentity{}
	rawKeys := storedComparisonSortedKeys(record.APIKeys)
	indexes := storedComparisonSortedKeys(record.Credentials)
	for k := range record.AIProviders {
		indexes = append(indexes, k)
	}
	for i := 0; i < len(rawKeys); i += 200 {
		var batch []entities.CPAAPIKey
		if err := s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Clauses(dbresolver.Read).Select("id, api_key, display_key, key_alias").Where("api_key IN ? AND is_deleted = ?", rawKeys[i:min(i+200, len(rawKeys))], false).Find(&batch).Error; err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
	}
	for i := 0; i < len(indexes); i += 200 {
		var batch []entities.UsageIdentity
		if err := s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Clauses(dbresolver.Read).Select("id, identity, auth_type, name, alias, lookup_key, base_url, file_name, file_path").Where("identity IN ? AND auth_type IN ? AND is_deleted = ?", indexes[i:min(i+200, len(indexes))], []entities.UsageIdentityAuthType{entities.UsageIdentityAuthTypeCredential, entities.UsageIdentityAuthTypeAIProvider}, false).Find(&batch).Error; err != nil {
			return nil, err
		}
		identities = append(identities, batch...)
	}
	// Redact against observed secrets across dimensions: a model/name/alias can
	// contain another entity's key. Never trust the repository's label alone.
	safe := func(value string) string {
		value = pricing.SafeCredentialText(value, entities.UsageIdentity{})
		for _, k := range rawKeys {
			value = pricing.SafeCredentialText(value, entities.UsageIdentity{Identity: k})
		}
		for _, k := range indexes {
			value = pricing.SafeCredentialText(value, entities.UsageIdentity{Identity: k})
		}
		for _, identity := range identities {
			value = pricing.SafeCredentialText(value, identity)
		}
		return value
	}
	items := func(source map[string]*repodto.StoredUsageComparisonItemRecord, kind string) []servicedto.StoredUsageComparisonItem {
		result := make([]servicedto.StoredUsageComparisonItem, 0, len(source))
		for _, raw := range storedComparisonSortedKeys(source) {
			v := source[raw]
			id, label := "", ""
			switch kind {
			case "model":
				label = safe(raw)
				if label != "" && label == raw {
					id = raw
				}
			case "key":
				for _, k := range keys {
					if k.APIKey == raw && k.ID > 0 {
						id = strconv.FormatInt(k.ID, 10)
						label = safe(k.KeyAlias)
						if label == "" {
							label = safe(k.DisplayKey)
						}
						break
					}
				}
			default:
				authType := entities.UsageIdentityAuthTypeCredential
				if kind == "provider" {
					authType = entities.UsageIdentityAuthTypeAIProvider
				}
				for _, identity := range identities {
					if identity.Identity == raw && identity.AuthType == authType && identity.ID > 0 {
						id = strconv.FormatInt(identity.ID, 10)
						label = safe(helper.UsageIdentityDisplayName(identity))
						break
					}
				}
			}
			if label == "" {
				switch kind {
				case "model":
					label = "Model"
				case "key":
					label = "API key"
				case "provider":
					label = "AI provider"
				default:
					label = "Credential"
				}
			}
			var publicID *string
			if id != "" {
				v := id
				publicID = &v
			}
			result = append(result, servicedto.StoredUsageComparisonItem{ID: publicID, IDAvailable: publicID != nil, Label: label, TokenBuckets: v.TokenBuckets, Requests: v.Requests, Failures: v.Failures, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, CacheReadTokens: v.CacheReadTokens, CacheCreationTokens: v.CacheCreationTokens, ReasoningTokens: v.ReasoningTokens, TotalTokens: v.TotalTokens, StoredCostUSD: v.StoredCostUSD, StoredCostAvailable: v.StoredCostAvailable})
		}
		// Stable public-ID order, with a stable private identity tie-break (never
		// serialized) for unavailable IDs and duplicate labels. No label merging.
		sort.SliceStable(result, func(i, j int) bool {
			a, b := "", ""
			if result[i].ID != nil {
				a = *result[i].ID
			}
			if result[j].ID != nil {
				b = *result[j].ID
			}
			return a < b
		})
		return result
	}
	return &servicedto.StoredUsageComparisons{
		Range:    servicedto.StoredUsageOverviewRange{Start: record.RangeStart, End: record.RangeEnd, EndExclusive: true, Timezone: time.Local.String()},
		Coverage: servicedto.StoredUsageComparisonCoverage{StoredUsageOverviewCoverage: servicedto.StoredUsageOverviewCoverage{BoundarySource: record.BoundarySource, RollupSources: []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"}, ArchiveCoverageAsserted: false, CacheUsed: record.CacheUsed}, IdentityAttributionComplete: record.IdentityAttributionComplete, UnattributedRequests: record.UnattributedRequests},
		Buckets:  record.Buckets, Granularity: record.Granularity, Models: items(record.Models, "model"), APIKeys: items(record.APIKeys, "key"), AuthFiles: items(record.AuthFiles, "credential"), Credentials: items(record.Credentials, "credential"), AIProviders: items(record.AIProviders, "provider"),
	}, nil
}
func storedComparisonSortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
