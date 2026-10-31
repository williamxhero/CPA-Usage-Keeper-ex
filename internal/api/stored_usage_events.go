package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Explicit allowlist: never marshal repository metadata (raw key/identity/source)
// or a current-price DTO into the persisted-valuation namespace.
type storedUsageEventPayload struct {
	ID                   string                 `json:"id"`
	Timestamp            string                 `json:"timestamp"`
	APIKey               string                 `json:"api_key,omitempty"`
	Source               string                 `json:"source"`
	Model                string                 `json:"model"`
	ModelAlias           string                 `json:"model_alias,omitempty"`
	ResponseModel        string                 `json:"response_model,omitempty"`
	ReasoningEffort      string                 `json:"reasoning_effort,omitempty"`
	ServiceTier          string                 `json:"service_tier,omitempty"`
	EffectiveServiceTier string                 `json:"effective_service_tier,omitempty"`
	ResponseServiceTier  string                 `json:"response_service_tier,omitempty"`
	Failed               bool                   `json:"failed"`
	StatusCode           *int                   `json:"status_code,omitempty"`
	Stream               *bool                  `json:"stream,omitempty"`
	LatencyMS            int64                  `json:"latency_ms"`
	TTFTMS               *int64                 `json:"ttft_ms,omitempty"`
	Tokens               usageEventTokenPayload `json:"tokens"`
	StoredCostUSD        float64                `json:"stored_cost_usd"`
	StoredCostAvailable  bool                   `json:"stored_cost_available"`
}

func buildStoredUsageEvent(row servicedto.StoredUsageEventRecord, identities []entities.UsageIdentity, infos map[string]analysisAPIKeyInfo) storedUsageEventPayload {
	m := row.Metadata
	source := "Credential"
	matches := 0
	for _, identity := range identities {
		kind := ""
		switch identity.AuthType {
		case entities.UsageIdentityAuthTypeAIProvider:
			kind = "apikey"
		case entities.UsageIdentityAuthTypeAuthFile:
			kind = "oauth"
		}
		// Do not borrow a same-index identity of another/unknown observed type.
		if !identity.IsDeleted && kind != "" && kind == m.ObservedAuthType && identity.Identity == m.ObservedAuthIndex {
			matches++
			name := identity.Name
			if identity.Alias != nil && strings.TrimSpace(*identity.Alias) != "" {
				name = *identity.Alias
			}
			source = pricing.SafeCredentialText(name, identity)
		}
	}
	if matches != 1 || source == "" {
		source = "Credential"
	}
	return storedUsageEventPayload{ID: strconv.FormatInt(m.ID, 10), Timestamp: timeutil.FormatStorageTime(m.Timestamp), APIKey: usageEventAPIKeyLabel(m.APIGroupKey, infos), Source: source, Model: m.Model, ModelAlias: m.ModelAlias, ResponseModel: m.ResponseModel, ReasoningEffort: m.ReasoningEffort, ServiceTier: m.ServiceTier, EffectiveServiceTier: m.EffectiveServiceTier, ResponseServiceTier: m.ResponseServiceTier, Failed: m.Failed, StatusCode: m.StatusCode, Stream: m.Stream, LatencyMS: m.LatencyMS, TTFTMS: m.TTFTMS, Tokens: usageEventTokenPayload{InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, ReasoningTokens: m.ReasoningTokens, CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens, TotalTokens: m.TotalTokens}, StoredCostUSD: row.StoredCostUSD, StoredCostAvailable: row.StoredCostAvailable}
}

const storedEventsExportMaxBytes int64 = 256 << 20
const storedEventsExportTimeout = 2 * time.Minute

func registerStoredUsageEventsRoutes(router gin.IRoutes, provider service.StoredUsageEventsProvider, identityProvider service.UsageIdentityProvider, keyProvider service.CPAAPIKeyProvider) {
	slots := make(chan struct{}, usageEventsExportMaxConcurrency)
	router.GET("/usage/stored/events", func(c *gin.Context) {
		setNoStoreHeaders(c)
		if provider == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stored usage events provider is not configured"})
			return
		}
		filter, err := parseUsageFilterQuery(c.Request, timeutil.NormalizeStorageTime(time.Now()))
		if err != nil {
			writeUsageFilterParseError(c, err)
			return
		}
		_ = applyUsageEventsSourceFilter(&filter)
		page, err := provider.ListStoredUsageEvents(c.Request.Context(), filter)
		if err != nil {
			if writeStoredEventsScopeError(c, err) {
				return
			}
			writeInternalError(c, "list stored usage events failed", err)
			return
		}
		identities, err := loadUsageResolutionData(c, identityProvider)
		if err != nil {
			writeInternalError(c, "load stored usage identities failed", err)
			return
		}
		infos, err := loadCPAAPIKeyInfos(c, keyProvider)
		if err != nil {
			return
		}
		events := make([]storedUsageEventPayload, 0, len(page.Events))
		for _, row := range page.Events {
			events = append(events, buildStoredUsageEvent(row, identities, infos))
		}
		cursor := ""
		if filter.CursorMode && page.HasMore && len(page.Events) > 0 {
			last := page.Events[len(page.Events)-1].Metadata
			cursor = encodeUsageEventsCursor(last.Timestamp, last.ID)
		}
		c.JSON(http.StatusOK, gin.H{"events": events, "total_count": page.TotalCount, "page": page.Page, "page_size": page.PageSize, "total_pages": page.TotalPages, "has_more": page.HasMore, "next_cursor": cursor, "data_source": page.DataSource})
	})
	router.GET("/usage/stored/events/export", func(c *gin.Context) {
		setNoStoreHeaders(c)
		if provider == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stored usage events provider is not configured"})
			return
		}
		format := strings.ToLower(strings.TrimSpace(c.Query("format")))
		if format == "" {
			format = "csv"
		}
		if format != "csv" && format != "json" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid export format"})
			return
		}
		filter, err := parseUsageExportFilterQuery(c.Request, timeutil.NormalizeStorageTime(time.Now()))
		if err != nil {
			writeUsageFilterParseError(c, err)
			return
		}
		_ = applyUsageEventsSourceFilter(&filter)
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "stored usage events export capacity is full"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), storedEventsExportTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		identities, err := loadUsageResolutionData(c, identityProvider)
		if err != nil {
			writeInternalError(c, "load stored usage identities failed", err)
			return
		}
		infos, err := loadCPAAPIKeyInfos(c, keyProvider)
		if err != nil {
			return
		}
		// Disk spool bounds memory and validates ALL selected fees using the single
		// reader SELECT snapshot before HTTP success. No precheck/requery TOCTOU.
		// Both spooling and client transfer are cancellation-aware; partial transport
		// cannot be rolled back and Content-Length makes truncation detectable.
		file, err := os.CreateTemp("", "stored-usage-events-*.export")
		if err != nil {
			writeInternalError(c, "prepare stored usage export failed", err)
			return
		}
		defer os.Remove(file.Name())
		defer file.Close()
		out := &storedExportWriter{ctx: ctx, out: file, remaining: storedEventsExportMaxBytes}
		err = writeStoredEventsExport(out, format, func(emit func(storedUsageEventPayload) error) error {
			return provider.StreamStoredUsageEvents(ctx, filter, func(row servicedto.StoredUsageEventRecord) error {
				return emit(buildStoredUsageEvent(row, identities, infos))
			})
		})
		if err != nil {
			if writeStoredEventsScopeError(c, err) {
				return
			}
			writeInternalError(c, "export stored usage events failed", err)
			return
		}
		size, err := file.Seek(0, io.SeekCurrent)
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
		}
		if err != nil {
			writeInternalError(c, "rewind stored usage export failed", err)
			return
		}
		if err = ctx.Err(); err != nil {
			writeInternalError(c, "export stored usage events canceled", err)
			return
		}
		contentType := "text/csv; charset=utf-8"
		if format == "json" {
			contentType = "application/json; charset=utf-8"
		}
		c.Header("Content-Type", contentType)
		c.Header("Content-Disposition", `attachment; filename="stored-usage-events.`+format+`"`)
		c.Header("Content-Length", strconv.FormatInt(size, 10))
		c.Status(http.StatusOK)
		_, err = io.Copy(&storedExportWriter{ctx: ctx, out: c.Writer, remaining: storedEventsExportMaxBytes}, file)
		if err != nil {
			_ = c.Error(err)
		}
	})
}

func writeStoredEventsScopeError(c *gin.Context, err error) bool {
	if errors.Is(err, service.ErrInvalidID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid api_key_id"})
		return true
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "api key not found"})
		return true
	}
	return false
}

type storedExportWriter struct {
	ctx       context.Context
	out       io.Writer
	remaining int64
}

func (w *storedExportWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("stored usage export exceeds byte limit")
	}
	n, err := w.out.Write(p)
	w.remaining -= int64(n)
	return n, err
}

var storedUsageCSVHeader = []string{"id", "timestamp", "api_key", "source", "model", "model_alias", "response_model", "reasoning_effort", "service_tier", "effective_service_tier", "response_service_tier", "failed", "status_code", "stream", "latency_ms", "ttft_ms", "input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_creation_tokens", "total_tokens", "stored_cost_usd", "stored_cost_available", "data_source"}

func writeStoredEventsExport(out io.Writer, format string, stream func(func(storedUsageEventPayload) error) error) error {
	var count int64
	if format == "json" {
		if _, err := io.WriteString(out, `{"data_source":"usage_events","events":[`); err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		if err := stream(func(row storedUsageEventPayload) error {
			if count > 0 {
				if _, err := io.WriteString(out, ","); err != nil {
					return err
				}
			}
			if err := encoder.Encode(row); err != nil {
				return err
			}
			count++
			return nil
		}); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, `],"total_count":%d}`, count)
		return err
	}
	writer := csv.NewWriter(out)
	if err := writer.Write(storedUsageCSVHeader); err != nil {
		return err
	}
	if err := stream(func(e storedUsageEventPayload) error {
		record := []string{e.ID, e.Timestamp, e.APIKey, e.Source, e.Model, e.ModelAlias, e.ResponseModel, e.ReasoningEffort, e.ServiceTier, e.EffectiveServiceTier, e.ResponseServiceTier, strconv.FormatBool(e.Failed), formatOptionalInt(e.StatusCode), formatOptionalBool(e.Stream), strconv.FormatInt(e.LatencyMS, 10), formatOptionalInt64(e.TTFTMS), strconv.FormatInt(e.Tokens.InputTokens, 10), strconv.FormatInt(e.Tokens.OutputTokens, 10), strconv.FormatInt(e.Tokens.ReasoningTokens, 10), strconv.FormatInt(e.Tokens.CacheReadTokens, 10), strconv.FormatInt(e.Tokens.CacheCreationTokens, 10), strconv.FormatInt(e.Tokens.TotalTokens, 10), strconv.FormatFloat(e.StoredCostUSD, 'f', -1, 64), strconv.FormatBool(e.StoredCostAvailable), "usage_events"}
		for i := 0; i <= 10; i++ {
			record[i] = formatOptionalCSVText(&record[i])
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		count++
		if count%100 == 0 {
			writer.Flush()
			return writer.Error()
		}
		return nil
	}); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}
