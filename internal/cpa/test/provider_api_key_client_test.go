package cpa_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/cpa"
	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
)

type providerEndpointResult struct {
	statusCode    int
	body          []byte
	keys          []providerconfig.ProviderKeyConfig
	compatibility []providerconfig.OpenAICompatibilityConfig
}

func TestProviderAPIKeyClientsUseV8GroupsAndManagementAuth(t *testing.T) {
	for _, source := range []string{"codex", "xai", "gemini", "gemini-interactions", "claude", "vertex", "meta", "openai"} {
		t.Run(source, func(t *testing.T) {
			pathSource := source
			if source == "gemini-interactions" {
				pathSource = "interactions"
			}
			if source == "openai" {
				pathSource = "openai-compatibility"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config/api-keys/"+pathSource {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer management-secret" {
					t.Errorf("management auth missing")
				}
				_, _ = w.Write([]byte(`[{"name":"routing group","prefix":"team","base-url":"https://example.invalid/v1","priority":3,"keys":[{"api-key":"first-key","auth_index":"first-auth"},{"api-key":"second-key","auth_index":"second-auth"}]}]`))
			}))
			defer server.Close()
			result, err := fetchProviderEndpoint(context.Background(), cpa.NewClient(server.URL, "management-secret", 2*time.Second, false), source)
			if err != nil || result.statusCode != http.StatusOK {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
			if source == "openai" {
				if len(result.compatibility) != 1 || len(result.compatibility[0].APIKeyEntries) != 2 || result.compatibility[0].APIKeyEntries[1].AuthIndex != "second-auth" {
					t.Fatalf("groups = %#v", result.compatibility)
				}
			} else if len(result.keys) != 2 || result.keys[0].AuthIndex != "first-auth" || result.keys[1].AuthIndex != "second-auth" || result.keys[1].Prefix != "team" || *result.keys[1].Priority != 3 {
				t.Fatalf("keys = %#v", result.keys)
			}
		})
	}
}

func TestProviderConfigurationClassifiesMissingEmptyAndFailure(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		version   string
		wantError bool
	}{
		{"empty", http.StatusOK, `[]`, "", false},
		{"missing config dev", http.StatusNotFound, `{"error":"not_found"}`, "dev", false},
		{"missing config release", http.StatusNotFound, `{"error":"not_found"}`, "v9.0.0", false},
		{"gateway not_found", http.StatusNotFound, `{"error":"not_found"}`, "", true},
		{"blank CPA header", http.StatusNotFound, `{"error":"not_found"}`, " \t", true},
		{"proxy 404", http.StatusNotFound, `<html>not found</html>`, "", true},
		{"CPA HTML 404", http.StatusNotFound, `<html>not found</html>`, "dev", true},
		{"other JSON 404", http.StatusNotFound, `{"error":"no_route","secret":"do-not-expose"}`, "dev", true},
		{"unauthorized", http.StatusUnauthorized, `{"error":"denied"}`, "dev", true},
		{"malformed JSON", http.StatusOK, `[{`, "", true},
		{"invalid structure", http.StatusOK, `[{"keys":{}}]`, "", true},
		{"blank", http.StatusOK, " \n", "", true},
	}
	for _, source := range []string{"xai", "openai"} {
		for _, tc := range cases {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tc.version != "" {
						w.Header().Set("X-CPA-VERSION", tc.version)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				result, err := fetchProviderEndpoint(context.Background(), cpa.NewClient(server.URL, "management-secret", 2*time.Second, false), source)
				if (err != nil) != tc.wantError || result.statusCode != tc.status {
					t.Fatalf("result = %#v, err = %v", result, err)
				}
				if err != nil && strings.Contains(err.Error(), "do-not-expose") {
					t.Fatalf("error exposed response: %v", err)
				}
			})
		}
	}
}

func fetchProviderEndpoint(ctx context.Context, client *cpa.Client, source string) (providerEndpointResult, error) {
	switch source {
	case "codex":
		result, err := client.FetchCodexAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "xai":
		result, err := client.FetchXAIAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "gemini":
		result, err := client.FetchGeminiAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "gemini-interactions":
		result, err := client.FetchInteractionsAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "claude":
		result, err := client.FetchClaudeAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "vertex":
		result, err := client.FetchVertexAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "meta":
		result, err := client.FetchMetaAPIKeys(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, keys: result.Payload}, err
	case "openai":
		result, err := client.FetchOpenAICompatibility(ctx)
		return providerEndpointResult{statusCode: result.StatusCode, body: result.Body, compatibility: result.Payload}, err
	default:
		return providerEndpointResult{}, fmt.Errorf("unknown provider source: %s", source)
	}
}
