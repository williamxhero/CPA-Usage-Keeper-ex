package api

import (
	"errors"
	"net/http"
	"time"

	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
	"github.com/gin-gonic/gin"
)

func registerStoredUsageOverviewRoute(router gin.IRoutes, provider service.StoredUsageOverviewProvider) {
	registerStoredUsageOverviewRouteWithClock(router, provider, time.Now)
}

// A private clock seam keeps range/boundary integration tests deterministic.
func registerStoredUsageOverviewRouteWithClock(router gin.IRoutes, provider service.StoredUsageOverviewProvider, clock func() time.Time) {
	router.GET("/usage/stored/overview", func(c *gin.Context) {
		setNoStoreHeaders(c)
		if provider == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stored usage overview provider is not configured"})
			return
		}
		// Reject every unsupported/ambiguous field, even empty cursor/model fields.
		// The shared parser intentionally ignores event fields for other overview APIs.
		for field, values := range c.Request.URL.Query() {
			switch field {
			case "range", "unit", "start", "end", "api_key_id":
				if len(values) == 1 {
					continue
				}
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported or repeated stored overview query parameter"})
			return
		}
		query := c.Request.URL.Query()
		if query.Get("range") != "custom" && (query.Has("start") || query.Has("end") || query.Has("unit")) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start, end and unit require custom range"})
			return
		}
		now := timeutil.NormalizeStorageTime(clock())
		filter, err := parseUsageOverviewTimeFilterQuery(c.Request, now)
		if err != nil {
			writeUsageFilterParseError(c, err)
			return
		}
		result, err := provider.GetStoredUsageOverview(c.Request.Context(), servicedto.StoredUsageOverviewFilter{Range: filter.Range, CustomUnit: filter.CustomUnit, StartTime: filter.StartTime, EndTime: filter.EndTime, EndExclusive: filter.EndExclusive, APIKeyID: filter.APIKeyID, QueryNow: &now})
		if err != nil {
			var validation *service.StoredUsageOverviewValidationError
			if errors.As(err, &validation) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid stored overview filter"})
				return
			}
			if writeStoredEventsScopeError(c, err) {
				return
			}
			writeInternalError(c, "read stored usage overview failed", err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}
