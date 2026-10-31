package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Implement every optional legacy provider, so a misrouted request cannot hide
// behind an unconfigured provider or bypass the no-service-call assertion.
type removedPricingSpy struct {
	service.PricingProvider
	calls []string
	err   error
}

var _ service.PricingProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) ListUsedModels(_ context.Context) ([]string, error) {
	s.calls = append(s.calls, "ListUsedModels")
	var result []string
	return result, s.err
}
func (s *removedPricingSpy) ListPricing(_ context.Context) ([]entities.ModelPriceSetting, error) {
	s.calls = append(s.calls, "ListPricing")
	var result []entities.ModelPriceSetting
	return result, s.err
}
func (s *removedPricingSpy) PreviewPricingSync(_ context.Context, _ string) (servicedto.PricingSyncPreview, error) {
	s.calls = append(s.calls, "PreviewPricingSync")
	var result servicedto.PricingSyncPreview
	return result, s.err
}
func (s *removedPricingSpy) UpdatePricing(_ context.Context, _ servicedto.UpdatePricingInput) (*entities.ModelPriceSetting, error) {
	s.calls = append(s.calls, "UpdatePricing")
	var result *entities.ModelPriceSetting
	return result, s.err
}
func (s *removedPricingSpy) UpdatePricingBatch(_ context.Context, _ []servicedto.UpdatePricingInput) ([]entities.ModelPriceSetting, error) {
	s.calls = append(s.calls, "UpdatePricingBatch")
	var result []entities.ModelPriceSetting
	return result, s.err
}
func (s *removedPricingSpy) DeletePricing(_ context.Context, _ string) error {
	s.calls = append(s.calls, "DeletePricing")
	return s.err
}
func (s *removedPricingSpy) ListPricingRules(_ context.Context, _ string) ([]servicedto.PricingRule, error) {
	s.calls = append(s.calls, "ListPricingRules")
	var result []servicedto.PricingRule
	return result, s.err
}
func (s *removedPricingSpy) ReplacePricingRules(_ context.Context, _ servicedto.ReplacePricingRulesInput) ([]servicedto.PricingRule, error) {
	s.calls = append(s.calls, "ReplacePricingRules")
	var result []servicedto.PricingRule
	return result, s.err
}

var _ service.PricingCredentialProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) ListPricingCredentials(_ context.Context) ([]servicedto.PricingCredential, error) {
	s.calls = append(s.calls, "ListPricingCredentials")
	var result []servicedto.PricingCredential
	return result, s.err
}
func (s *removedPricingSpy) ListCredentialPricingSubjects(_ context.Context) ([]servicedto.PricingCredential, error) {
	s.calls = append(s.calls, "ListCredentialPricingSubjects")
	var result []servicedto.PricingCredential
	return result, s.err
}
func (s *removedPricingSpy) BindPricingCredential(_ context.Context, _ int64) (servicedto.PricingCredential, error) {
	s.calls = append(s.calls, "BindPricingCredential")
	var result servicedto.PricingCredential
	return result, s.err
}
func (s *removedPricingSpy) ResolvePricingCredential(_ context.Context, _ string, _ string) (servicedto.PricingCredential, error) {
	s.calls = append(s.calls, "ResolvePricingCredential")
	var result servicedto.PricingCredential
	return result, s.err
}

var _ service.PricingCredentialDefaultsProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) GetCredentialDefault(_ context.Context, _ string) (service.CredentialDefault, error) {
	s.calls = append(s.calls, "GetCredentialDefault")
	var result service.CredentialDefault
	return result, s.err
}
func (s *removedPricingSpy) SetCredentialDefault(_ context.Context, _ string, _ string) (service.CredentialDefault, error) {
	s.calls = append(s.calls, "SetCredentialDefault")
	var result service.CredentialDefault
	return result, s.err
}
func (s *removedPricingSpy) ClearCredentialDefault(_ context.Context, _ string) (service.CredentialDefault, error) {
	s.calls = append(s.calls, "ClearCredentialDefault")
	var result service.CredentialDefault
	return result, s.err
}

var _ service.PricingChannelsProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) ListPricingChannels(_ context.Context) ([]service.PricingChannel, error) {
	s.calls = append(s.calls, "ListPricingChannels")
	var result []service.PricingChannel
	return result, s.err
}
func (s *removedPricingSpy) GetPricingChannel(_ context.Context, _ string) (service.PricingChannel, error) {
	s.calls = append(s.calls, "GetPricingChannel")
	var result service.PricingChannel
	return result, s.err
}
func (s *removedPricingSpy) CreatePricingChannel(_ context.Context, _ service.PricingChannelInput) (service.PricingChannel, error) {
	s.calls = append(s.calls, "CreatePricingChannel")
	var result service.PricingChannel
	return result, s.err
}
func (s *removedPricingSpy) UpdatePricingChannel(_ context.Context, _ string, _ service.PricingChannelInput) (service.PricingChannel, error) {
	s.calls = append(s.calls, "UpdatePricingChannel")
	var result service.PricingChannel
	return result, s.err
}
func (s *removedPricingSpy) DeletePricingChannel(_ context.Context, _ string, _ bool) error {
	s.calls = append(s.calls, "DeletePricingChannel")
	return s.err
}
func (s *removedPricingSpy) SetChannelDefault(_ context.Context, _ string, _ string) (service.PricingChannel, error) {
	s.calls = append(s.calls, "SetChannelDefault")
	var result service.PricingChannel
	return result, s.err
}
func (s *removedPricingSpy) ClearChannelDefault(_ context.Context, _ string) (service.PricingChannel, error) {
	s.calls = append(s.calls, "ClearChannelDefault")
	var result service.PricingChannel
	return result, s.err
}

var _ service.PricingCredentialModelsProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) ListCredentialModels(_ context.Context, _ string) (service.CredentialModelMultipliers, error) {
	s.calls = append(s.calls, "ListCredentialModels")
	var result service.CredentialModelMultipliers
	return result, s.err
}
func (s *removedPricingSpy) GetCredentialModel(_ context.Context, _ string, _ string) (service.CredentialModelMultiplier, error) {
	s.calls = append(s.calls, "GetCredentialModel")
	var result service.CredentialModelMultiplier
	return result, s.err
}
func (s *removedPricingSpy) SetCredentialModel(_ context.Context, _ string, _ string, _ string) (service.CredentialModelMultiplier, error) {
	s.calls = append(s.calls, "SetCredentialModel")
	var result service.CredentialModelMultiplier
	return result, s.err
}
func (s *removedPricingSpy) ClearCredentialModel(_ context.Context, _ string, _ string) (service.CredentialModelMultiplier, error) {
	s.calls = append(s.calls, "ClearCredentialModel")
	var result service.CredentialModelMultiplier
	return result, s.err
}

var _ service.PricingCredentialFixedProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) SetCredentialFixed(_ context.Context, _ string, _ string, _ pricing.FixedTariff) (service.CredentialModelMultiplier, error) {
	s.calls = append(s.calls, "SetCredentialFixed")
	var result service.CredentialModelMultiplier
	return result, s.err
}

var _ service.PricingChannelModelsProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) ListChannelModels(_ context.Context, _ string) (service.ChannelModelPrices, error) {
	s.calls = append(s.calls, "ListChannelModels")
	var result service.ChannelModelPrices
	return result, s.err
}
func (s *removedPricingSpy) GetChannelModel(_ context.Context, _ string, _ string) (service.ChannelModelPrice, error) {
	s.calls = append(s.calls, "GetChannelModel")
	var result service.ChannelModelPrice
	return result, s.err
}
func (s *removedPricingSpy) SetChannelModel(_ context.Context, _ string, _ string, _ string) (service.ChannelModelPrice, error) {
	s.calls = append(s.calls, "SetChannelModel")
	var result service.ChannelModelPrice
	return result, s.err
}
func (s *removedPricingSpy) SetChannelFixed(_ context.Context, _ string, _ string, _ pricing.FixedTariff) (service.ChannelModelPrice, error) {
	s.calls = append(s.calls, "SetChannelFixed")
	var result service.ChannelModelPrice
	return result, s.err
}
func (s *removedPricingSpy) ClearChannelModel(_ context.Context, _ string, _ string) (service.ChannelModelPrice, error) {
	s.calls = append(s.calls, "ClearChannelModel")
	var result service.ChannelModelPrice
	return result, s.err
}

var _ service.PricingIdentityMigrationProvider = (*removedPricingSpy)(nil)

func (s *removedPricingSpy) GetPricingIdentityState(_ context.Context) (service.PricingIdentityState, error) {
	s.calls = append(s.calls, "GetPricingIdentityState")
	var result service.PricingIdentityState
	return result, s.err
}
func (s *removedPricingSpy) MigratePricingIdentity(_ context.Context, _ service.PricingIdentityMigrationInput) (service.PricingIdentityMutationResult, error) {
	s.calls = append(s.calls, "MigratePricingIdentity")
	var result service.PricingIdentityMutationResult
	return result, s.err
}
func (s *removedPricingSpy) CorrectPricingIdentity(_ context.Context, _ service.PricingIdentityCorrectionInput) (service.PricingIdentityMutationResult, error) {
	s.calls = append(s.calls, "CorrectPricingIdentity")
	var result service.PricingIdentityMutationResult
	return result, s.err
}

type removedPricingRequestCase struct{ name, path, body string }

func removedPricingRequestCases(route removedPricingRoute) []removedPricingRequestCase {
	path := "/api/v1" + route.path
	base := strings.Split(path, "?")[0]
	secret := "sk-retired-request-secret"
	cases := []removedPricingRequestCase{
		{"valid", path, route.body},
		{"empty", base, ""},
		{"malformed", base + "?model=%20&source=invalid&confirm_dependencies=no", `{"multiplier":NaN`},
		{"invalid", base + "?model=missing&directory_id=-1", `{"multiplier":-1,"confirmed":false}`},
		{"trailing_json", path, `{} {"access_token":"` + secret + `"}`},
		{"oversized_body", path, `{"api_key":"` + secret + `","padding":"` + strings.Repeat("x", 4<<20) + `"}`},
		{"oversized_query", base + "?model=" + strings.Repeat("q", 65537), route.body},
		{"secret_fields", base + "?model=" + secret + "&source=" + secret + "&access_token=" + secret + "&file_path=" + url.QueryEscape("C:/auth/private-token.json"), `{"api_key":"` + secret + `","lookup_key":"` + secret + `","access_token":"` + secret + `","refresh_token":"` + secret + `","file_path":"C:/auth/private-token.json"}`},
	}
	for _, id := range []string{"binding_cred_retained", "cred_retained", "chan_retained"} {
		if strings.Contains(base, "/"+id+"/") || strings.HasSuffix(base, "/"+id) {
			cases = append(cases,
				removedPricingRequestCase{"missing_target", strings.Replace(base, id, "absent", 1), route.body},
				removedPricingRequestCase{"secret_path", strings.Replace(base, id, secret, 1), route.body},
				removedPricingRequestCase{"oversized_path", strings.Replace(base, id, strings.Repeat("p", 32769), 1), route.body})
			break
		}
	}
	if base == "/api/v1/pricing/base" {
		cases = append(cases, removedPricingRequestCase{"secret_path", "/api/v1/pricing/" + secret, route.body}, removedPricingRequestCase{"oversized_path", "/api/v1/pricing/" + strings.Repeat("p", 32769), route.body})
	}
	return cases
}

func captureRemovedPricingLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := logrus.StandardLogger().Out
	output := &bytes.Buffer{}
	logrus.SetOutput(output)
	t.Cleanup(func() { logrus.SetOutput(previous) })
	return output
}

func assertRemovedPricingLogsSafe(t *testing.T, output *bytes.Buffer) {
	t.Helper()
	for _, secret := range []string{"sk-retired-request-secret", "private-token.json", "synthetic-retained-secret", "synthetic-retained-index"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("retired route log leaked %q", secret)
		}
	}
}

func TestRemovedPricingRoutesIgnoreAllInputsAndNeverCallService(t *testing.T) {
	output := captureRemovedPricingLogs(t)
	spy := &removedPricingSpy{}
	harness := newOfficialAuthHarness(t, spy)
	for _, route := range removedPricingRoutes() {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, request := range removedPricingRequestCases(route) {
				t.Run(request.name, func(t *testing.T) {
					response := serveOfficialPricing(harness, route.method, request.path, request.body, harness.admin)
					assertPricingRemoved(t, response)
					if len(spy.calls) != 0 {
						t.Fatalf("retired route called service: %v", spy.calls)
					}
					assertRemovedPricingLogsSafe(t, output)
				})
			}
		})
	}
}

func TestRemovedPricingRoutesKeepAuthenticationForEveryMethod(t *testing.T) {
	spy := &removedPricingSpy{}
	harness := newOfficialAuthHarness(t, spy)
	for _, route := range removedPricingRoutes() {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				cookie *http.Cookie
				status int
			}{
				{"anonymous", nil, http.StatusUnauthorized},
				{"viewer", harness.viewer, http.StatusForbidden},
				{"administrator", harness.admin, http.StatusGone},
			} {
				t.Run(test.name, func(t *testing.T) {
					response := serveOfficialPricing(harness, route.method, "/api/v1"+route.path, route.body, test.cookie)
					if response.Code != test.status {
						t.Fatalf("auth status=%d want=%d", response.Code, test.status)
					}
					if test.status == http.StatusGone {
						assertPricingRemoved(t, response)
					}
					if len(spy.calls) != 0 {
						t.Fatalf("unauthorized or retired route called service: %v", spy.calls)
					}
				})
			}
		})
	}
}

// Retain every old layer, exact identity relation and migration control/audit
// rows. The fixed rejection must not alter any of these durable records.
func seedRetainedPricingRows(t *testing.T, db *gorm.DB) entities.UsageIdentity {
	t.Helper()
	identity := entities.UsageIdentity{Name: "Retained account", AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "synthetic-retained-index", LookupKey: "synthetic-retained-secret", Type: "openai", BindingIdentityStatus: "unique"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	rotated := entities.UsageIdentity{Name: "Rotated retained account", AuthType: identity.AuthType, AuthTypeName: identity.AuthTypeName, Identity: "synthetic-retained-index-rotated", LookupKey: "synthetic-retained-secret-rotated", Type: "openai", BindingIdentityStatus: "unique"}
	if err := db.Create(&rotated).Error; err != nil {
		t.Fatal(err)
	}
	model := entities.ModelPriceSetting{Model: "base", PromptPricePer1M: 99, CompletionPricePer1M: 99, CacheReadPricePer1M: 99, CacheWritePricePer1M: 99, PriceMultiplier: new(7.0)}
	if err := db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&entities.ModelPriceRule{ModelPriceSettingID: model.ID, Key: "service_tier", Value: "priority", Multiplier: 9},
		&entities.CredentialPricingSubject{ID: "cred_retained", UsageIdentityID: identity.ID, AuthType: identity.AuthType, AuthTypeName: identity.AuthTypeName, Identity: identity.Identity},
		&entities.CredentialPricingAssociation{ID: "binding_cred_retained", SubjectID: "cred_retained", UsageIdentityID: identity.ID, AuthType: identity.AuthType, AuthTypeName: identity.AuthTypeName, Identity: identity.Identity, Enabled: true},
		&entities.CredentialPriceDefault{SubjectID: "cred_retained", Multiplier: 8},
		&entities.CredentialModelMultiplier{SubjectID: "cred_retained", Model: "base", Mode: "multiplier", Multiplier: 6},
		&entities.CredentialModelMultiplier{SubjectID: "cred_retained", Model: "legacy-fixed", Mode: "fixed", PromptPricePer1M: new(99.0), CompletionPricePer1M: new(99.0), CacheReadPricePer1M: new(99.0), CacheWritePricePer1M: new(99.0), PricingStyle: "openai"},
		&entities.PricingChannel{ID: "chan_retained", Name: "Retained channel"},
		&entities.PricingChannelMember{ChannelID: "chan_retained", SubjectID: "cred_retained"},
		&entities.ChannelPriceDefault{ChannelID: "chan_retained", Multiplier: 5},
		&entities.ChannelModelPrice{ChannelID: "chan_retained", Model: "base", Multiplier: 4, Mode: "multiplier"},
		&entities.ChannelModelPrice{ChannelID: "chan_retained", Model: "legacy-fixed", Mode: "fixed", PromptPricePer1M: new(99.0), CompletionPricePer1M: new(99.0), CacheReadPricePer1M: new(99.0), CacheWritePricePer1M: new(99.0), PricingStyle: "openai"},
		&entities.PricingMigrationBatch{ID: "retained_batch", Kind: entities.PricingMigrationKindLegacyPricing, Status: entities.PricingMigrationStatusSucceeded, InputFrozen: true, ImportedCount: 1, StartedAt: time.Now().UTC(), PreviousSnapshotJSON: "{}"},
		&entities.PricingMigrationBatchCount{BatchID: "retained_batch", Category: entities.PricingMigrationCategoryChannels, Migrated: 1},
		&entities.PricingMigrationItem{BatchID: "retained_batch", Category: entities.PricingMigrationCategoryChannels, SourceRef: "legacy_channel", TargetRef: "chan_retained", Outcome: entities.PricingMigrationOutcomeMigrated, Staged: true, InputJSON: "{}"},
		&entities.PricingMigrationLegacyState{BatchID: "retained_batch", Category: entities.PricingMigrationCategoryChannels, SourceRef: "legacy_channel", Inactive: true, Reason: "retained"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return identity
}

// Snapshot every table, not just counts or selected pricing entities: detect
// partial writes, timestamps, deletes, sequences, frozen snapshots and settings.
func allAPIDatabaseRows(t *testing.T, db *gorm.DB) map[string][]string {
	t.Helper()
	var tables []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]string, len(tables))
	for _, table := range tables {
		var rows []map[string]any
		if err := db.Table(table).Find(&rows).Error; err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		values := make([]string, 0, len(rows))
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, string(encoded))
		}
		sort.Strings(values)
		result[table] = values
	}
	return result
}

func TestRemovedPricingRoutesLeaveAllDatabaseRowsSnapshotAndMigrationUnchanged(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "unadopted_legacy"
		if strict {
			name = "strict_three_layer"
		}
		t.Run(name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			seedRetainedPricingRows(t, db)
			snapshot, err := repository.LoadPricingSnapshot(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			catalog := pricing.NewCatalog(snapshot)
			provider := service.NewPricingService(db, catalog)
			harness := newOfficialAuthHarness(t, provider)
			// Current APIs remain operational and establish a real active snapshot before
			// the no-write baseline is frozen.
			if strict {
				if res := serveOfficialPricing(harness, "PUT", "/api/v1/pricing/official", completeOfficialPricingJSON("base"), harness.admin); res.Code != 200 {
					t.Fatal(res.Body.String())
				}
				body := `{"cells":[{"subject_id":"cred_retained","model":"base","mode":"fixed","fixed":{"pricing_style":"openai","prompt_price_per_1m":2,"completion_price_per_1m":3,"cache_read_price_per_1m":4,"cache_write_price_per_1m":5}}]}`
				if res := serveOfficialPricing(harness, "PUT", "/api/v1/pricing/credential-model-matrix", body, harness.admin); res.Code != 200 {
					t.Fatal(res.Body.String())
				}
			}
			before := allAPIDatabaseRows(t, db)
			snapshot = catalog.Snapshot()
			output := captureRemovedPricingLogs(t)
			routes := removedPricingRoutes()
			state, err := provider.(service.PricingIdentityMigrationProvider).GetPricingIdentityState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for index := range routes {
				switch routes[index].path {
				case "/pricing/identity-bindings/migrate":
					encoded, err := json.Marshal(service.PricingIdentityMigrationInput{SubjectID: "cred_retained", DirectoryRef: state.Directory[len(state.Directory)-1].Ref, SnapshotID: snapshot.ID(), Confirmed: true})
					if err != nil {
						t.Fatal(err)
					}
					routes[index].body = string(encoded)
				case "/pricing/identity-bindings/binding_cred_retained/correction":
					encoded, err := json.Marshal(service.PricingIdentityCorrectionInput{ExpectedSubjectID: "cred_retained", Action: "unbind", SnapshotID: snapshot.ID(), Confirmed: true})
					if err != nil {
						t.Fatal(err)
					}
					routes[index].body = string(encoded)
				}
			}
			for _, route := range routes {
				t.Run(route.method+" "+route.path, func(t *testing.T) {
					for _, request := range removedPricingRequestCases(route) {
						t.Run(request.name, func(t *testing.T) {
							res := serveOfficialPricing(harness, route.method, request.path, request.body, harness.admin)
							assertPricingRemoved(t, res)
							if !reflect.DeepEqual(before, allAPIDatabaseRows(t, db)) {
								t.Fatal("retired route changed persisted rows, active settings or migration batch")
							}
							if catalog.Snapshot() != snapshot || catalog.Snapshot().ID() != snapshot.ID() {
								t.Fatal("retired route published a snapshot")
							}
							assertRemovedPricingLogsSafe(t, output)
						})
					}
				})
			}
			reports := provider.(service.PricingMigrationProvider)
			if report, err := reports.GetPricingMigrationReport(context.Background(), "retained_batch"); err != nil || report.Batch.ID != "retained_batch" {
				t.Fatalf("read-only migration report lost: %+v %v", report, err)
			}
			if !reflect.DeepEqual(before, allAPIDatabaseRows(t, db)) || catalog.Snapshot() != snapshot {
				t.Fatal("read-only migration report mutated pricing")
			}
			for _, path := range []string{"/pricing", "/models/used", "/pricing/credentials", "/pricing/credential-subjects", "/pricing/official", "/pricing/credential-model-matrix", "/pricing/credentials/matrix"} {
				if res := serveOfficialPricing(harness, "GET", "/api/v1"+path, "", harness.admin); res.Code != 200 {
					t.Fatalf("preserved GET %s: %d %s", path, res.Code, res.Body.String())
				}
			}
		})
	}
}

// Legacy HTTP successes and parser/service-error tests are rewritten at the
// same route seam: all their original requests now have one fixed rejection.
func assertRetiredPricingRequest(t *testing.T, method, path, body string, providerErrors ...error) {
	t.Helper()
	spy := &removedPricingSpy{}
	if len(providerErrors) > 0 {
		spy.err = providerErrors[0]
	}
	harness := newOfficialAuthHarness(t, spy)
	res := serveOfficialPricing(harness, method, path, body, harness.admin)
	assertPricingRemoved(t, res)
	if len(spy.calls) != 0 {
		t.Fatalf("retired request called service: %v", spy.calls)
	}
}

func bindPricingFixture(t *testing.T, db *gorm.DB, directoryID int64, catalogs ...*pricing.Catalog) safeCredential {
	t.Helper()
	catalog := emptyPricingCatalogForTest()
	if len(catalogs) > 0 {
		catalog = catalogs[0]
	}
	provider := service.NewPricingService(db, catalog).(service.PricingCredentialProvider)
	credential, err := provider.BindPricingCredential(context.Background(), directoryID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	var saved safeCredential
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

// Service-only setup for unadopted/rollback compatibility cost fixtures. This
// does not issue HTTP requests or simulate responses from retired handlers.
func applyLegacyPricingFixture(t *testing.T, provider service.PricingProvider, method, path, body string) {
	t.Helper()
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/api/v1/pricing/"), "/")
	if len(parts) != 3 {
		t.Fatalf("unsupported legacy fixture path %s", path)
	}
	var input struct {
		Multiplier json.RawMessage `json:"multiplier"`
		Mode       string          `json:"mode"`
		Fixed      json.RawMessage `json:"fixed"`
	}
	if body != "" {
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
	}
	multiplier := strings.Trim(string(input.Multiplier), `"`)
	model := parsed.Query().Get("model")
	id := parts[1]
	var fixed pricing.FixedTariff
	if input.Mode == "fixed" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(input.Fixed, &fields); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fields["pricing_style"], &fixed.PricingStyle); err != nil {
			t.Fatal(err)
		}
		for field, target := range map[string]**float64{"prompt_price_per_1m": &fixed.PromptPricePer1M, "completion_price_per_1m": &fixed.CompletionPricePer1M, "cache_read_price_per_1m": &fixed.CacheReadPricePer1M, "cache_write_price_per_1m": &fixed.CacheWritePricePer1M} {
			value, err := pricing.ParseFixedRate(strings.Trim(string(fields[field]), `"`))
			if err != nil {
				t.Fatal(err)
			}
			*target = &value
		}
	}
	switch parts[0] + "/" + parts[2] {
	case "credentials/default":
		defaults := provider.(service.PricingCredentialDefaultsProvider)
		if method == http.MethodDelete {
			_, err = defaults.ClearCredentialDefault(context.Background(), id)
		} else {
			_, err = defaults.SetCredentialDefault(context.Background(), id, multiplier)
		}
	case "credentials/model":
		models := provider.(service.PricingCredentialModelsProvider)
		if method == http.MethodDelete {
			_, err = models.ClearCredentialModel(context.Background(), id, model)
		} else if input.Mode == "fixed" {
			_, err = provider.(service.PricingCredentialFixedProvider).SetCredentialFixed(context.Background(), id, model, fixed)
		} else {
			_, err = models.SetCredentialModel(context.Background(), id, model, multiplier)
		}
	case "channels/default":
		channels := provider.(service.PricingChannelsProvider)
		if method == http.MethodDelete {
			_, err = channels.ClearChannelDefault(context.Background(), id)
		} else {
			_, err = channels.SetChannelDefault(context.Background(), id, multiplier)
		}
	case "channels/model":
		models := provider.(service.PricingChannelModelsProvider)
		if method == http.MethodDelete {
			_, err = models.ClearChannelModel(context.Background(), id, model)
		} else if input.Mode == "fixed" {
			_, err = models.SetChannelFixed(context.Background(), id, model, fixed)
		} else {
			_, err = models.SetChannelModel(context.Background(), id, model, multiplier)
		}
	default:
		t.Fatalf("unsupported legacy fixture path %s", path)
	}
	if err != nil {
		t.Fatalf("legacy fixture %s %s: %v", method, path, err)
	}
}
