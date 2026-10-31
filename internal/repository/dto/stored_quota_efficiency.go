package dto

import "time"

// Stored history is committed fee evidence, not a current pricing snapshot.
type StoredCodexQuotaEfficiencyHistory struct {
	// GeneratedAt 固定本次当前周期的查询截点和响应时间。
	GeneratedAt time.Time
	// RangeStart 是响应实际采用的历史下界，供调用层明确“最近 30 天”口径。
	RangeStart time.Time
	// Windows 列出近 30 天范围内各自有记录的角色，每个角色最多一项并使用其最新真实周期。
	Windows []CodexQuotaEfficiencyWindow
	// SelectedWindow 是当前响应实际展开的单个窗口；没有历史时为 nil。
	SelectedWindow *CodexQuotaEfficiencyWindow
	// Cycles 由后端统一标记 current/completed；当前周期优先，其余按角色有效结束时间倒序。
	Cycles []StoredCodexQuotaEfficiencyCycle
}

type StoredCodexQuotaEfficiencyCycle struct {
	// ID 是历史父行 ID，只用于稳定标识周期，不是 UsageEvent 外键。
	ID int64
	// Status 只允许 current/completed，前端不得自行用浏览器时间重算。
	Status string
	// WindowSeconds 是这个历史周期自身的上游真实秒数，不跟随角色最新周期变化。
	WindowSeconds int64
	// WindowStartedAt 是由 reset_at 减真实窗口秒数得到的周期开始边界。
	WindowStartedAt time.Time
	// ResetAt 是周期结束的半开右边界。
	ResetAt time.Time
	// EffectiveStartedAt 是角色在本周期真正生效的查询起点；重叠周期统一使用新周期理论起点切分。
	EffectiveStartedAt time.Time
	// EffectiveEndedAt 是角色在本周期真正结束的查询终点；窗口切换或角色消失时可早于 ResetAt。
	EffectiveEndedAt time.Time
	// FirstObservedAt 是 Keeper 首次看到该周期的时间，不能冒充周期开始。
	FirstObservedAt time.Time
	// LastObservedAt 是 Keeper 最近看到该周期的时间。
	LastObservedAt time.Time
	// FirstRemainingPercent 与 LastRemainingPercent 让单基线周期也能在列表中明确展示。
	FirstRemainingPercent *int
	LastRemainingPercent  *int
	// ObservationCount 是本周期所有整数百分比状态段的真实观察次数总和。
	ObservationCount int64
	// Usage 聚合整个周期边界内的 OAuth UsageEvent，包括百分比稳定期间的事件。
	Usage StoredCodexQuotaEfficiencyUsage
	// Transitions 只包含真实相邻剩余百分比状态形成的效率样本。
	Transitions []StoredCodexQuotaEfficiencyTransition
}

type StoredCodexQuotaEfficiencyTransition struct {
	// FromRemainingPercent 是变化前 Keeper 统一的整数剩余百分比。
	FromRemainingPercent int
	// ToRemainingPercent 是变化后 Keeper 统一的整数剩余百分比。
	ToRemainingPercent int
	// PercentagePoints 是真实下降百分点；跨档不会补造中间样本。
	PercentagePoints int
	// IsDirect 仅在恰好下降一个百分点时为 true。
	IsDirect bool
	// IntervalStartedAt 取前一状态段 first_observed_at，不属于区间。
	IntervalStartedAt time.Time
	// IntervalEndedAt 取后一状态段 first_observed_at，属于区间。
	IntervalEndedAt time.Time
	// Usage 是这个观察间隔内动态回溯出的 OAuth 用量。
	Usage StoredCodexQuotaEfficiencyUsage
	// TokensPerPoint 是区间 TotalTokens 除以真实下降百分点后的平均值。
	TokensPerPoint float64
	// CostPerPoint 是区间可计价部分除以真实下降百分点后的平均值。
	CostPerPoint float64
	// CostPerPointAvailable 为 false 时调用层必须显示缺失，不能把 CostPerPoint 当作零成本。
	CostPerPointAvailable bool
}

type StoredCodexQuotaEfficiencyUsage struct {
	// Requests 是范围内所有匹配请求数量。
	Requests int64
	// SuccessfulRequests 是 Failed=false 的请求数量。
	SuccessfulRequests int64
	// FailedRequests 是 Failed=true 的请求数量。
	FailedRequests int64
	// InputTokens 保留输入 Token 原值汇总，供周期用量展示。
	InputTokens int64
	// OutputTokens 保留输出 Token 汇总。
	OutputTokens int64
	// ReasoningTokens 保留推理 Token 汇总，供用户理解 TotalTokens 构成。
	ReasoningTokens int64
	// CacheReadTokens 保留缓存读取 Token 汇总。
	CacheReadTokens int64
	// CacheCreationTokens 保留缓存写入 Token 汇总。
	CacheCreationTokens int64
	// TotalTokens 是页面展示和每百分点效率计算使用的总 Token。
	TotalTokens int64
	// TotalCostUSD 是事件已存美元费用在该周期或区间的总和。
	TotalCostUSD float64
	// CostAvailable 只有范围内每条事件的已存费用都标记可用时才为 true。
	CostAvailable bool
}
