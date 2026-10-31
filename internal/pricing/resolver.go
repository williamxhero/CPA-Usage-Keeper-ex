package pricing

import (
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
)

// CostSubject 是所有 usage 来源进入计价领域的唯一固定输入。
type CostSubject struct {
	Dimensions UsageDimensions
	Tokens     helper.UsageTokenCostInput
	// Exact identity evidence is independent of normalized legacy rule fields.
	// ObservedIdentity distinguishes unknown raw type from a type-less rollup.
	AuthType          string
	IdentityAuthIndex string
	ObservedIdentity  bool
	// Timestamp is used only by the independently configured stored valuation.
	Timestamp time.Time
}

// NewCostSubject 规范化事件身份字段；时间由调用方从 CPA 已存 timestamp 填入。
func NewCostSubject(dimensions UsageDimensions, tokens helper.UsageTokenCostInput) CostSubject {
	return CostSubject{
		Dimensions:        canonicalizeUsageDimensions(dimensions),
		Tokens:            tokens,
		IdentityAuthIndex: dimensions.AuthIndex,
	}
}

type CostResult struct {
	Cost                       helper.UsageTokenCostBreakdown
	Available                  bool
	PricingStyle               string
	MatchedModel               string
	MatchedBy                  string
	RuleMultiplier             float64
	CredentialSubjectID        string
	Scope                      string
	Multiplier                 *float64
	UnavailableReason          string
	ChannelID                  string
	ChannelName                string
	AttributionWarning         string
	SelectedModel              string
	SelectedBy                 string
	Mode                       string
	Fixed                      *FixedTariff
	ReferenceCost              helper.UsageTokenCostBreakdown
	ReferenceAvailable         bool
	ConfiguredEstimate         PriceEstimate
	ReferenceEstimate          PriceEstimate
	ReferenceUnavailableReason string
	PricingSource              string
	PricingSnapshotID          string
	LegacyModelMultiplier      float64
	LegacyRuleMultiplier       float64
	FinalMultiplier            float64
	matchedRules               [ruleFieldCount]MatchedRule
	matchedRuleCount           int
}

// Resolver pins a single immutable snapshot for each response or stored event batch.
type Resolver struct {
	snapshot *Snapshot
}

func (r Resolver) ActiveFields() ActiveFields {
	if r.snapshot == nil {
		return 0
	}
	return r.snapshot.activeFields
}

func (r Resolver) Calculate(subject CostSubject) CostResult {
	if r.threeLayerPricing() {
		result := r.calculateThreeLayer(subject)
		return result
	}
	result := r.calculateLegacyConfigured(subject)
	return result
}

// The pre-catalog path remains only for old snapshots that have no new fact
// source. A published catalog containing official or matrix data never enters
// this function, so legacy defaults/rules/channels cannot affect its costs.
func (r Resolver) calculateLegacyConfigured(subject CostSubject) CostResult {
	model, matchedModel, matchedBy, found := r.matchModel(subject.Dimensions)
	var result CostResult
	if config, selectedBy, selected := r.credentialModel(subject); selected {
		result = calculateModelOverride(subject, config.SubjectID, config.Mode, config.Multiplier, config.Fixed, model, matchedModel, matchedBy, found)
		result.Scope, result.Mode = "credential_model", config.Mode
		result.SelectedModel, result.SelectedBy = config.Model, selectedBy
	} else if id, multiplier, selected := r.credentialDefault(subject); selected {
		result = calculateCredentialDefault(subject, id, multiplier, model, matchedModel, matchedBy, found)
	} else if config, selectedBy, selected := r.channelModel(subject); selected {
		result = calculateModelOverride(subject, r.credentialSubject(subject), config.Mode, config.Multiplier, config.Fixed, model, matchedModel, matchedBy, found)
		result.Scope, result.Mode = "channel_model", config.Mode
		result.SelectedModel, result.SelectedBy = config.Model, selectedBy
	} else if _, multiplier, selected := r.channelDefault(subject); selected {
		result = calculateCredentialDefault(subject, r.credentialSubject(subject), multiplier, model, matchedModel, matchedBy, found)
		result.Scope = "channel_default"
	} else {
		result = r.CalculateLegacy(subject)
	}
	result = withBaselineReference(result, subject, model, found)
	if result.CredentialSubjectID == "" {
		result.CredentialSubjectID = r.credentialSubject(subject)
	}
	result.LegacyModelMultiplier = 1
	if found && model.pricing.PriceMultiplier != nil {
		result.LegacyModelMultiplier = *model.pricing.PriceMultiplier
	}
	result.LegacyRuleMultiplier = result.RuleMultiplier
	result.FinalMultiplier = result.LegacyModelMultiplier * result.RuleMultiplier
	if result.Scope != "" {
		legacy := r.CalculateLegacy(subject)
		result.LegacyRuleMultiplier = legacy.RuleMultiplier
		result.matchedRules, result.matchedRuleCount = legacy.matchedRules, legacy.matchedRuleCount
		result.FinalMultiplier = 1
		if result.Multiplier != nil {
			result.FinalMultiplier = *result.Multiplier
		}
	}
	result.ChannelID, result.ChannelName, result.AttributionWarning = r.ChannelAttribution(subject)
	return result
}

func (r Resolver) threeLayerPricing() bool {
	return r.snapshot != nil && r.snapshot.threeLayerPricing
}

func (r Resolver) calculateThreeLayer(subject CostSubject) CostResult {
	result := CostResult{
		Available: !helper.UsageTokenInputRequiresPricing(subject.Tokens), RuleMultiplier: 1,
		LegacyModelMultiplier: 1, LegacyRuleMultiplier: 1, FinalMultiplier: 1,
		CredentialSubjectID: r.credentialSubject(subject), PricingSource: "unavailable", PricingSnapshotID: r.SnapshotID(),
		Scope: "unavailable", Mode: ModeFixed,
	}
	required := requiredOfficialPriceSegments(subject.Tokens)
	model, matchedBy := subject.Dimensions.Model, "model"
	matrix, matrixFound := r.matrixForSubjectModel(subject, model)
	if !matrixFound && strings.TrimSpace(subject.Dimensions.ModelAlias) != "" {
		model, matchedBy = subject.Dimensions.ModelAlias, "model_alias"
		matrix, matrixFound = r.matrixForSubjectModel(subject, model)
	}
	if matrixFound {
		result.MatchedModel, result.MatchedBy = model, matchedBy
		result.SelectedModel, result.SelectedBy = model, matchedBy
		result.Scope, result.Mode, result.PricingSource = "credential_matrix", ModeFixed, CredentialMatrixSource
		result.PricingStyle, result.Fixed = matrix.Fixed.PricingStyle, cloneFixed(&matrix.Fixed)
		// Matrix rows are validated as complete four-segment fixed tariffs. A
		// billable request is therefore priced whenever the exact subject/model
		// row is selected; only a zero-token request starts as known zero.
		result.Available = true
		result.Cost = helper.CalculateUsageTokenCostBreakdown(subject.Tokens, matrix.Fixed.pricing(matrix.Fixed.PricingStyle))
		result.ConfiguredEstimate = estimate(result.Cost, true, "")
		result.ConfiguredEstimate.Source, result.ConfiguredEstimate.MatchedModel, result.ConfiguredEstimate.MatchedBy = CredentialMatrixSource, model, matchedBy
		result = result.WithReference(r.referenceEstimate(subject, required))
		return result
	}

	match := MatchOfficialPrice(r.snapshot.officialPrices, model, required)
	if match.Status == OfficialMatchUnavailable && matchedBy == "model" && strings.TrimSpace(subject.Dimensions.ModelAlias) != "" {
		aliasMatch := MatchOfficialPrice(r.snapshot.officialPrices, subject.Dimensions.ModelAlias, required)
		if aliasMatch.Status != OfficialMatchUnavailable || aliasMatch.Reason != "no_match" {
			match, model, matchedBy = aliasMatch, subject.Dimensions.ModelAlias, "model_alias"
		}
	}
	result.MatchedModel, result.MatchedBy = model, matchedBy
	if match.Status == OfficialMatchAvailable && match.Price != nil {
		modelPricing := officialPriceSetting(*match.Price)
		result.Available, result.PricingStyle = true, modelPricing.PricingStyle
		result.Cost = helper.CalculateUsageTokenCostBreakdown(subject.Tokens, modelPricing)
		result.PricingSource = match.Price.Source
		if result.PricingSource == "" {
			result.PricingSource = "official"
		}
		result.Scope, result.Mode = result.PricingSource, "official"
		result.ConfiguredEstimate = estimate(result.Cost, true, "")
		result.ConfiguredEstimate.Source, result.ConfiguredEstimate.MatchedModel, result.ConfiguredEstimate.MatchedBy = result.PricingSource, model, matchedBy
		result = result.WithReference(result.ConfiguredEstimate)
		return result
	}
	if !result.Available {
		result.UnavailableReason = match.Reason
		if result.UnavailableReason == "" {
			result.UnavailableReason = "no_match"
		}
		result.ConfiguredEstimate = estimate(result.Cost, false, result.UnavailableReason)
		result.ConfiguredEstimate.Source, result.ConfiguredEstimate.MatchedModel, result.ConfiguredEstimate.MatchedBy = "unavailable", model, matchedBy
	} else {
		result.ConfiguredEstimate = estimate(result.Cost, true, "")
		result.ConfiguredEstimate.Source = "none"
		result = result.WithReference(result.ConfiguredEstimate)
	}
	if result.ReferenceEstimate.Status == "" {
		result.ReferenceEstimate = r.referenceEstimate(subject, required)
	}
	return result
}

func requiredOfficialPriceSegments(tokens helper.UsageTokenCostInput) []OfficialPriceSegment {
	input, output, read, write := tokens.InputTokens, tokens.OutputTokens, tokens.CacheReadTokens, tokens.CacheCreationTokens
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	if read < 0 {
		read = 0
	}
	if write < 0 {
		write = 0
	}
	input -= read + write
	if input < 0 {
		input = 0
	}
	segments := make([]OfficialPriceSegment, 0, 4)
	if input > 0 {
		segments = append(segments, OfficialPricePrompt)
	}
	if output > 0 {
		segments = append(segments, OfficialPriceCompletion)
	}
	if read > 0 {
		segments = append(segments, OfficialPriceCacheRead)
	}
	if write > 0 {
		segments = append(segments, OfficialPriceCacheWrite)
	}
	return segments
}

func officialPriceSetting(price entities.OfficialModelPrice) entities.ModelPriceSetting {
	value := func(rate *float64) float64 {
		if rate == nil {
			return 0
		}
		return *rate
	}
	one := 1.0
	return entities.ModelPriceSetting{Model: price.CanonicalModelID, PricingStyle: price.PricingStyle, PromptPricePer1M: value(price.PromptPricePer1M), CompletionPricePer1M: value(price.CompletionPricePer1M), CacheReadPricePer1M: value(price.CacheReadPricePer1M), CacheWritePricePer1M: value(price.CacheWritePricePer1M), PriceMultiplier: &one}
}

func (r Resolver) matrixForSubjectModel(subject CostSubject, model string) (CredentialMatrixConfig, bool) {
	if r.snapshot == nil {
		return CredentialMatrixConfig{}, false
	}
	id := r.credentialSubject(subject)
	if id == "" {
		return CredentialMatrixConfig{}, false
	}
	return r.snapshot.CredentialMatrixConfig(id, model)
}

func (r Resolver) referenceEstimate(subject CostSubject, required []OfficialPriceSegment) PriceEstimate {
	if !helper.UsageTokenInputRequiresPricing(subject.Tokens) {
		p := estimate(helper.UsageTokenCostBreakdown{}, true, "")
		p.Source = "none"
		return p
	}
	model := subject.Dimensions.Model
	match := MatchOfficialPrice(r.snapshot.officialPrices, model, required)
	if match.Status == OfficialMatchUnavailable && strings.TrimSpace(subject.Dimensions.ModelAlias) != "" {
		aliasMatch := MatchOfficialPrice(r.snapshot.officialPrices, subject.Dimensions.ModelAlias, required)
		if aliasMatch.Status != OfficialMatchUnavailable || aliasMatch.Reason != "no_match" {
			match, model = aliasMatch, subject.Dimensions.ModelAlias
		}
	}
	if match.Status != OfficialMatchAvailable || match.Price == nil {
		return estimate(helper.UsageTokenCostBreakdown{}, false, match.Reason)
	}
	cost := helper.CalculateUsageTokenCostBreakdown(subject.Tokens, officialPriceSetting(*match.Price))
	p := estimate(cost, true, "")
	p.Source, p.MatchedModel = match.Price.Source, model
	return p
}

// Scope selection precedes this shared calculator; missing baselines never
// cause a selected multiplier to fall through to a lower fixed configuration.
func calculateModelOverride(subject CostSubject, id, mode string, multiplier float64, fixed *FixedTariff, model compiledModel, matchedModel, matchedBy string, found bool) CostResult {
	if mode != ModeFixed {
		return calculateCredentialDefault(subject, id, multiplier, model, matchedModel, matchedBy, found)
	}
	style := fixed.PricingStyle
	if found {
		style = model.pricing.PricingStyle
	}
	result := CostResult{Available: true, PricingStyle: style, CredentialSubjectID: id, RuleMultiplier: 1, Fixed: cloneFixed(fixed), MatchedModel: matchedModel, MatchedBy: matchedBy}
	result.Cost = helper.CalculateUsageTokenCostBreakdown(subject.Tokens, fixed.pricing(style))
	return result
}

func withBaselineReference(result CostResult, subject CostSubject, model compiledModel, found bool) CostResult {
	result.ReferenceAvailable = found || !helper.UsageTokenInputRequiresPricing(subject.Tokens)
	if found {
		result.ReferenceCost = helper.CalculateUsageTokenCostBreakdown(subject.Tokens, unadjustedPricing(model))
		if !finiteBreakdown(result.ReferenceCost) {
			return result.IncompleteReference("reference_overflow")
		}
	}
	if !result.ReferenceAvailable {
		result.ReferenceUnavailableReason = "missing_baseline"
	}
	return result
}

func calculateCredentialDefault(subject CostSubject, id string, multiplier float64, model compiledModel, matchedModel, matchedBy string, found bool) CostResult {
	result := CostResult{Available: !helper.UsageTokenInputRequiresPricing(subject.Tokens), RuleMultiplier: 1, CredentialSubjectID: id, Scope: "credential_default", Mode: ModeMultiplier, Multiplier: &multiplier, MatchedModel: matchedModel, MatchedBy: matchedBy}
	if !found {
		if !result.Available {
			result.UnavailableReason = "missing_baseline"
		}
		return result
	}
	result.Available = true
	result.PricingStyle = model.pricing.PricingStyle
	result.Cost = helper.ScaleUsageTokenCostBreakdown(helper.CalculateUsageTokenCostBreakdown(subject.Tokens, unadjustedPricing(model)), multiplier)
	return result
}

// CalculateLegacy preserves legacy grouping for subjects already rejected by
// exact typed attribution. It never retries a type-less credential lookup.
func (r Resolver) CalculateLegacy(subject CostSubject) CostResult {
	if r.threeLayerPricing() {
		result := r.calculateThreeLayer(subject)
		return result
	}
	model, matchedModel, matchedBy, found := r.matchModel(subject.Dimensions)
	if !found {
		return withBaselineReference(CostResult{
			Available:      !helper.UsageTokenInputRequiresPricing(subject.Tokens),
			RuleMultiplier: 1, LegacyModelMultiplier: 1, LegacyRuleMultiplier: 1, FinalMultiplier: 1,
		}, subject, model, false)
	}
	breakdown := helper.CalculateUsageTokenCostBreakdown(subject.Tokens, model.pricing)
	ruleMultiplier := 1.0
	if model.pricing.PriceMultiplier == nil || *model.pricing.PriceMultiplier != 0 {
		ruleMultiplier = matchingRuleMultiplier(model.rules, subject.Dimensions)
		breakdown = helper.ScaleUsageTokenCostBreakdown(breakdown, ruleMultiplier)
	}
	modelMultiplier := 1.0
	if model.pricing.PriceMultiplier != nil {
		modelMultiplier = *model.pricing.PriceMultiplier
	}
	var matchedRules [ruleFieldCount]MatchedRule
	matchedRuleCount := 0
	if modelMultiplier != 0 {
		for _, rule := range model.rules {
			if subject.Dimensions.Value(rule.field) == rule.value {
				matchedRules[matchedRuleCount] = MatchedRule{Key: rule.field.String(), Multiplier: rule.multiplier}
				matchedRuleCount++
			}
		}
	}
	return withBaselineReference(CostResult{
		Cost: breakdown, Available: true, PricingStyle: model.pricing.PricingStyle,
		MatchedModel: matchedModel, MatchedBy: matchedBy, RuleMultiplier: ruleMultiplier,
		LegacyModelMultiplier: modelMultiplier, LegacyRuleMultiplier: ruleMultiplier, FinalMultiplier: modelMultiplier * ruleMultiplier, matchedRules: matchedRules, matchedRuleCount: matchedRuleCount,
	}, subject, model, true)
}

// CalculateFee is used only with the separately published stored-valuation
// snapshot; old usage responses continue to use Calculate and CalculateLegacy.
func (r Resolver) CalculateFee(subject CostSubject) FeeResult {
	model, _, _, found := r.matchModel(subject.Dimensions)
	if !found {
		return FeeResult{Available: !helper.UsageTokenInputRequiresPricing(subject.Tokens)}
	}
	selected := model.pricing
	for _, branch := range model.branches {
		if !branch.matches(subject, r.snapshot.location) {
			continue
		}
		selected.PromptPricePer1M = branch.prices.Input
		selected.CompletionPricePer1M = branch.prices.Output
		selected.CacheReadPricePer1M = branch.prices.CacheRead
		selected.CacheWritePricePer1M = branch.prices.CacheWrite
		break
	}
	total := helper.CalculateUsageTokenCost(subject.Tokens, selected)
	if model.pricing.PriceMultiplier == nil || *model.pricing.PriceMultiplier != 0 {
		total *= matchingRuleMultiplier(model.rules, subject.Dimensions)
	}
	return FeeResult{TotalCostUSD: total, Available: true}
}

// matchModel prefers the observed model and only falls back to its alias.
func (r Resolver) matchModel(dimensions UsageDimensions) (compiledModel, string, string, bool) {
	if r.snapshot == nil {
		return compiledModel{}, "", "", false
	}
	if model, ok := r.snapshot.modelsByName[dimensions.Model]; ok {
		return model, dimensions.Model, "model", true
	}
	if model, ok := r.snapshot.modelsByName[dimensions.ModelAlias]; ok {
		return model, dimensions.ModelAlias, "model_alias", true
	}
	return compiledModel{}, "", "", false
}

// matchingRuleMultiplier 连乘所有精确匹配的条件；任一零倍率使整条请求免费。
func matchingRuleMultiplier(rules []compiledRule, dimensions UsageDimensions) float64 {
	multiplier := 1.0
	for _, rule := range rules {
		if dimensions.Value(rule.field) != rule.value {
			continue
		}
		if rule.multiplier == 0 {
			return 0
		}
		multiplier *= rule.multiplier
	}
	return multiplier
}
