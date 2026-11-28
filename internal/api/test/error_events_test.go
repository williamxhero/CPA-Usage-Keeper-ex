package test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

func TestErrorEventsRoutePreservesOnlyAllowlistedUpstreamCodes(t *testing.T) {
	db := openAPITestDatabase(t)
	identity := entities.UsageIdentity{AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "test-index", LookupKey: "sk-test-secret", Name: "test"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	provider := service.NewErrorEventService(db)
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{ErrorEvents: provider})
	for i, tc := range []struct{ name, field, want string }{
		{"invalid_prompt", ",\"upstream_code\":\"invalid_prompt\"", "invalid_prompt"},
		{"invalid_value", ",\"upstream_code\":\"invalid_value\"", "invalid_value"},
		{"unsupported_value", ",\"upstream_code\":\"unsupported_value\"", "unsupported_value"},
		{"context_length_exceeded", ",\"upstream_code\":\"context_length_exceeded\"", "context_length_exceeded"},
		{"context_too_large", ",\"upstream_code\":\"context_too_large\"", "context_too_large"},
		{"message_too_big", ",\"upstream_code\":\"message_too_big\"", "message_too_big"},
		{"string_above_max_length", ",\"upstream_code\":\"string_above_max_length\"", "string_above_max_length"},
		{"previous_response_not_found", ",\"upstream_code\":\"previous_response_not_found\"", "previous_response_not_found"},
		{"invalid_encrypted_content", ",\"upstream_code\":\"invalid_encrypted_content\"", "invalid_encrypted_content"},
		{"thinking_signature_invalid", ",\"upstream_code\":\"thinking_signature_invalid\"", "thinking_signature_invalid"},
		{"unknown", ",\"upstream_code\":\"private-user-input\"", ""},
		{"nonexact", ",\"upstream_code\":\"INVALID_PROMPT \"", ""},
		{"missing", "", ""},
		{"malformed", ",\"upstream_code\":42", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 11, 27, 12, 0, i, 0, time.UTC)
			raw := fmt.Sprintf(`{"timestamp":%q,"auth_index":"test-index","code":"normalized","body":"fixed sk-test-secret"%s}`, at.Format(time.RFC3339Nano), tc.field)
			if err := provider.StoreErrorEvent(context.Background(), raw, at); err != nil {
				t.Fatal(err)
			}
			resp := serveAPIGet(router, fmt.Sprintf("/api/v1/usage/identities/%d/errors?page_size=1", identity.ID))
			var wire struct {
				Events []map[string]json.RawMessage `json:"events"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &wire); err != nil || resp.Code != http.StatusOK || len(wire.Events) != 1 {
				t.Fatalf("unexpected response: %d %s (%v)", resp.Code, resp.Body, err)
			}
			event := wire.Events[0]
			got, present := event["upstream_code"]
			if (tc.want == "" && present) || (tc.want != "" && string(got) != fmt.Sprintf("%q", tc.want)) {
				t.Errorf("upstream_code = %s (present=%v), want %q or omitted when empty", got, present, tc.want)
			}
			if string(event["code"]) != `"normalized"` || string(event["body_summary"]) != `"fixed [REDACTED]"` {
				t.Errorf("existing code/body changed: %s", resp.Body)
			}
		})
	}
}

type errorEventsStub struct {
	request  service.ErrorEventListRequest
	response service.ErrorEventListResponse
	err      error
}

func (s *errorEventsStub) StoreErrorEvent(context.Context, string, time.Time) error { return nil }

func (s *errorEventsStub) ListErrorEvents(_ context.Context, request service.ErrorEventListRequest) (service.ErrorEventListResponse, error) {
	s.request = request
	return s.response, s.err
}

func TestErrorEventsRouteReturnsSafeDisplayPayloadAndCursor(t *testing.T) {
	timestamp := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	credentialRetry := timestamp.Add(5 * time.Minute)
	modelRetry := timestamp.Add(3 * time.Minute)
	provider := &errorEventsStub{response: service.ErrorEventListResponse{
		Events: []entities.ErrorEvent{{
			ID: 9, Timestamp: timestamp, Provider: "codex", Model: "gpt-5.6", AuthID: "runtime-auth-id", AuthIndex: "identity-auth-index",
			StatusCode: 429, Body: "upstream rejected api_key=sk-secret-value at base_url=https://private.example/v1", Code: "rate_limit", Retryable: true,
			AuthStatus: "error", AuthStatusMessage: "hidden auth snapshot", AuthUnavailable: true, AuthNextRetryAfter: &credentialRetry,
			AuthQuotaExceeded: new(true), AuthQuotaReason: new("limit for sk-secret-value"), AuthQuotaBackoffLevel: new(2),
			AuthModelName: new("gpt-5.6"), AuthModelStatus: new("error"), AuthModelStatusMessage: new("Bearer abc.def.secret"), AuthModelUnavailable: new(true),
			AuthModelNextRetryAfter: &modelRetry,
		}},
		HasMore: true,
		APIKey:  "sk-secret-value",
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{ErrorEvents: provider})
	resp := serveAPIGet(router, "/api/v1/usage/identities/42/errors?page_size=50")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	assertNoStoreHeaders(t, resp)
	body := resp.Body.String()
	for _, secret := range []string{"sk-secret-value", "hidden auth snapshot", `"auth_id"`, `"auth_index"`, `"auth_status"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "base_url=https://private.example/v1") {
		t.Fatalf("response removed non-API-key error context: %s", body)
	}
	var payload struct {
		Events []struct {
			ID                   string  `json:"id"`
			StatusCode           int     `json:"status_code"`
			BodySummary          string  `json:"body_summary"`
			CredentialRetryAfter *string `json:"credential_retry_after"`
			ModelRetryAfter      *string `json:"model_retry_after"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Events) != 1 || payload.Events[0].ID != "9" || payload.Events[0].StatusCode != 429 || payload.Events[0].CredentialRetryAfter == nil || payload.Events[0].ModelRetryAfter == nil {
		t.Fatalf("unexpected response: %+v", payload)
	}
	if !payload.HasMore || payload.NextCursor == "" || provider.request.IdentityID != 42 || provider.request.PageSize != 50 {
		t.Fatalf("unexpected pagination/request: payload=%+v request=%+v", payload, provider.request)
	}
}

func TestErrorEventsRouteOnlyRedactsKnownAPIKey(t *testing.T) {
	timestamp := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	provider := &errorEventsStub{response: service.ErrorEventListResponse{
		Events: []entities.ErrorEvent{{
			ID: 10, Timestamp: timestamp, Provider: "codex", Model: "gpt-5.6", StatusCode: 401,
			Body: `{
	  "model": "gpt-5.6",
	  "status": "error",
	  "code": "unauthorized",
	  "message": "API key generic-api-key was rejected",
	  "token": "provider-error-token-name",
	  "claims": {"aud": "provider-error-audience"},
	  "config": {"region": "provider-error-region"}
}`,
			Code: "unauthorized", AuthStatus: "error",
		}},
		APIKey: "generic-api-key",
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{ErrorEvents: provider})
	resp := serveAPIGet(router, "/api/v1/usage/identities/42/errors")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if strings.Contains(body, "generic-api-key") {
		t.Fatalf("response leaked the known API key: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("response did not contain redaction marker: %s", body)
	}
	for _, ordinaryValue := range []string{
		"gpt-5.6",
		"error",
		"unauthorized",
		"provider-error-token-name",
		"provider-error-audience",
		"provider-error-region",
	} {
		if !strings.Contains(body, ordinaryValue) {
			t.Fatalf("response removed non-API-key error value %q: %s", ordinaryValue, body)
		}
	}
}

func TestErrorEventsRouteParsesCursorAndErrors(t *testing.T) {
	timestamp := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	cursor := base64.RawURLEncoding.EncodeToString([]byte(timestamp.Format(time.RFC3339Nano) + "|17"))
	provider := &errorEventsStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{ErrorEvents: provider})

	resp := serveAPIGet(router, "/api/v1/usage/identities/42/errors?cursor="+cursor)
	if resp.Code != http.StatusOK || provider.request.Cursor == nil || provider.request.Cursor.ID != 17 || !provider.request.Cursor.Timestamp.Equal(timestamp) {
		t.Fatalf("cursor was not parsed: status=%d request=%+v body=%s", resp.Code, provider.request, resp.Body.String())
	}

	for _, tc := range []struct {
		path   string
		err    error
		status int
	}{
		{path: "/api/v1/usage/identities/not-an-id/errors", status: http.StatusBadRequest},
		{path: "/api/v1/usage/identities/42/errors?cursor=invalid", status: http.StatusBadRequest},
		{path: "/api/v1/usage/identities/404/errors", err: gorm.ErrRecordNotFound, status: http.StatusNotFound},
		{path: "/api/v1/usage/identities/42/errors", err: errors.New("database unavailable"), status: http.StatusInternalServerError},
	} {
		provider.err = tc.err
		resp := serveAPIGet(router, tc.path)
		if resp.Code != tc.status {
			t.Fatalf("%s status = %d, want %d; body=%s", tc.path, resp.Code, tc.status, resp.Body.String())
		}
	}
}
