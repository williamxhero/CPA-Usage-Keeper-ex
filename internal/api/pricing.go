package api

import (
	"net/http"

	"cpa-usage-keeper/internal/service"
	"github.com/gin-gonic/gin"
)

type usedModelsResponse struct {
	Models []string `json:"models"`
}

type pricingEntryResponse struct {
	Model                string  `json:"model"`
	PricingStyle         string  `json:"pricing_style"`
	PromptPricePer1M     float64 `json:"prompt_price_per_1m"`
	CompletionPricePer1M float64 `json:"completion_price_per_1m"`
	CacheReadPricePer1M  float64 `json:"cache_read_price_per_1m"`
	CacheWritePricePer1M float64 `json:"cache_write_price_per_1m"`
	PriceMultiplier      float64 `json:"price_multiplier"`
}

type pricingListResponse struct {
	Pricing []pricingEntryResponse `json:"pricing"`
}

func registerPricingRoutes(router gin.IRoutes, pricingProvider service.PricingProvider, syncProviders ...service.PricingCredentialSyncProvider) {
	var credentialSyncProvider service.PricingCredentialSyncProvider
	if len(syncProviders) > 0 {
		credentialSyncProvider = syncProviders[0]
	}
	router.GET("/models/used", func(c *gin.Context) {
		if pricingProvider == nil {
			c.JSON(http.StatusOK, usedModelsResponse{Models: []string{}})
			return
		}

		models, err := pricingProvider.ListUsedModels(c.Request.Context())
		if err != nil {
			writeInternalError(c, "list used models failed", err)
			return
		}

		c.JSON(http.StatusOK, usedModelsResponse{Models: models})
	})

	router.GET("/pricing", func(c *gin.Context) {
		if pricingProvider == nil {
			c.JSON(http.StatusOK, pricingListResponse{Pricing: []pricingEntryResponse{}})
			return
		}

		settings, err := pricingProvider.ListPricing(c.Request.Context())
		if err != nil {
			writeInternalError(c, "list pricing failed", err)
			return
		}

		response := make([]pricingEntryResponse, 0, len(settings))
		for _, setting := range settings {
			response = append(response, pricingEntryResponse{
				Model:                setting.Model,
				PricingStyle:         setting.PricingStyle,
				PromptPricePer1M:     setting.PromptPricePer1M,
				CompletionPricePer1M: setting.CompletionPricePer1M,
				CacheReadPricePer1M:  setting.CacheReadPricePer1M,
				CacheWritePricePer1M: setting.CacheWritePricePer1M,
				PriceMultiplier:      modelPriceMultiplierValue(setting.PriceMultiplier),
			})
		}
		c.JSON(http.StatusOK, pricingListResponse{Pricing: response})
	})

	router.GET("/pricing/sync/preview", pricingFeatureRemoved)
	registerPricingRuleRoutes(router)
	registerPricingCredentialRoutes(router, pricingProvider, credentialSyncProvider)
	registerPricingCredentialDefaultRoutes(router)
	registerPricingChannelRoutes(router)
	registerPricingChannelModelRoutes(router)
	registerPricingCredentialModelRoutes(router)
	registerPricingCredentialMatrixRoutes(router, pricingProvider)
	registerPricingIdentityMigrationRoutes(router)
	registerOfficialPricingRoutes(router, pricingProvider)

	router.PUT("/pricing", pricingFeatureRemoved)
	router.PUT("/pricing/batch", pricingFeatureRemoved)
	router.PUT("/pricing/models", pricingFeatureRemoved)
	router.PUT("/pricing/:model", pricingFeatureRemoved)
	router.DELETE("/pricing", pricingFeatureRemoved)
}

func modelPriceMultiplierValue(multiplier *float64) float64 {
	if multiplier == nil {
		return 1
	}
	return *multiplier
}
