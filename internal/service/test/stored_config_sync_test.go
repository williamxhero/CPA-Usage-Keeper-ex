package test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"cpa-usage-keeper/internal/cpa/dto/models"
	"cpa-usage-keeper/internal/cpa/dto/response"
	"cpa-usage-keeper/internal/entities"
)

func TestStoredConfigSyncFetchUsesExplicitProviderAndNeverMutatesRevision(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = pricingReviewTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"gpt-4.1":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000002,"output_cost_per_token":0.000008}}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	db := openUsageServiceTestDatabase(t)
	if err := db.Create(&entities.UsageEvent{EventKey: "stored-sync-local", Model: "gpt-4.1"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, fetcher := range []stubModelsFetcher{
		{result: &response.ModelsResult{Payload: models.ModelsResponse{Data: []models.ModelInfo{{ID: "gpt-4.1"}}}}},
		{err: errors.New("CPA unavailable")},
	} {
		provider, _ := newStoredConfigService(t, db, fetcher)
		got, err := provider.FetchPricingSync(context.Background(), "litellm")
		if err != nil || got.Source != "litellm" || len(got.Matches) != 1 || got.Matches[0].Model != "gpt-4.1" || got.Matches[0].BasePrices.Input != 2 || got.Matches[0].BasePrices.Output != 8 {
			t.Fatalf("stored source fetch: %+v %v", got, err)
		}
		listed, err := provider.ListPricingModels(context.Background())
		if err != nil || listed.ConfigRevision != 0 || len(listed.Models) != 0 {
			t.Fatalf("fetch mutated stored config: %+v %v", listed, err)
		}
	}
}
