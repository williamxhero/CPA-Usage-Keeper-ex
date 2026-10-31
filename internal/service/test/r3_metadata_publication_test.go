package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/cpa"
	"cpa-usage-keeper/internal/cpa/dto/cpaapikeys"
	"cpa-usage-keeper/internal/cpa/dto/credentials"
	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
	"cpa-usage-keeper/internal/cpa/dto/response"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

type r3Notifier struct{ count int }

func (*r3Notifier) NotifyUsageEventsCommitted([]entities.UsageEvent) {}
func (n *r3Notifier) NotifyUsageIdentitiesChanged()                  { n.count++ }

// A runtime fatal cannot be recovered. Isolate each catalog path so it cannot
// swallow the independent atomicity/version tests in the package process.
func TestR3CatalogMutexSubprocess(t *testing.T) {
	if mode := os.Getenv("R3_CATALOG_CHILD"); mode != "" {
		db := openMetadataTestDatabase(t, "r3-mutex.db")
		f := newMetadataTestFetcher()
		catalog := pricing.NewCatalog(pricing.EmptySnapshot())
		f.standardHooks["codex"] = func(context.Context) (*response.ProviderKeyConfigResult, error) {
			if !catalog.MutationMutex().TryLock() {
				t.Fatal("pricing lock held during fetch")
			}
			catalog.MutationMutex().Unlock()
			return f.standardResults["codex"], nil
		}
		if err := db.Callback().Query().Before("gorm:query").Register("test:r3_writer_locked", func(tx *gorm.DB) {
			if catalog.MutationMutex().TryLock() {
				catalog.MutationMutex().Unlock()
				tx.AddError(errors.New("pricing writer is not locked"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		s := service.NewSyncServiceWithOptions(db, service.SyncServiceOptions{MetadataFetcher: f, PricingCatalog: catalog, UsageAggregationNotifier: &r3Notifier{}})
		for i := 0; i < 2; i++ {
			var err error
			if mode == "complete" {
				err = s.SyncCredentialDirectory(context.Background())
			} else {
				err = s.SyncMetadata(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			if !catalog.MutationMutex().TryLock() {
				t.Fatal("pricing writer was not released")
			}
			catalog.MutationMutex().Unlock()
		}
		return
	}
	for _, mode := range []string{"legacy", "complete"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestR3CatalogMutexSubprocess$", "-test.timeout=15s", "-test.v")
			cmd.Env = append(os.Environ(), "R3_CATALOG_CHILD="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("catalog child %s: %v\n%s", mode, err, output)
			}
		})
	}
}

type r3Directory struct {
	Identities   []entities.UsageIdentity
	Keys         []entities.CPAAPIKey
	Subjects     []entities.CredentialPricingSubject
	Associations []entities.CredentialPricingAssociation
}

func r3ReadDirectory(t *testing.T, db *gorm.DB) r3Directory {
	t.Helper()
	var d r3Directory
	for _, target := range []any{&d.Identities, &d.Keys, &d.Subjects, &d.Associations} {
		if err := db.Order("id asc").Find(target).Error; err != nil {
			t.Fatal(err)
		}
	}
	return d
}
func r3Populate(f *metadataTestFetcher, suffix string) {
	f.setCredentials([]credentials.Credential{{AuthIndex: "oauth-" + suffix, Name: suffix + ".json", Type: "codex", Provider: "codex", Priority: metadataIntPointer(0)}})
	f.managementAPIKeysResult = &response.ManagementAPIKeysResult{StatusCode: 200, Payload: cpaapikeys.ManagementAPIKeysResponse{APIKeys: []string{"management-" + suffix}}}
	f.standardResults["codex"] = &response.ProviderKeyConfigResult{StatusCode: 200, Payload: []providerconfig.ProviderKeyConfig{{AuthIndex: "provider-" + suffix, APIKey: "private-" + suffix, Name: suffix, Priority: metadataIntPointer(0)}}}
}

func TestR3CompletePublicationFailurePreservesEveryScope(t *testing.T) {
	cases := []string{"credentials-error", "credentials-nil", "management-error", "management-nil", "provider-error", "provider-nil", "credentials-write", "management-write", "provider-write", "subject-write", "catalog-load"}
	for _, fault := range cases {
		t.Run(fault, func(t *testing.T) {
			db := openMetadataTestDatabase(t, "r3-"+fault+".db")
			f := newMetadataTestFetcher()
			r3Populate(f, "before")
			catalog := pricing.NewCatalog(pricing.EmptySnapshot())
			notifier := &r3Notifier{}
			now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
			s := service.NewSyncServiceWithOptions(db, service.SyncServiceOptions{MetadataFetcher: f, PricingCatalog: catalog, UsageAggregationNotifier: notifier, Now: func() time.Time { return now }})
			if err := s.SyncCredentialDirectory(context.Background()); err != nil {
				t.Fatal(err)
			}
			beforeSeed := r3ReadDirectory(t, db)
			if len(beforeSeed.Subjects) != 2 {
				t.Fatalf("seed subjects = %d, want 2", len(beforeSeed.Subjects))
			}
			association := entities.CredentialPricingAssociation{ID: "r3-association", SubjectID: beforeSeed.Subjects[0].ID, AuthType: entities.UsageIdentityAuthTypeCredential, AuthTypeName: "oauth", Identity: "historical-alias", Enabled: true, CreatedAt: now}
			if err := db.Create(&association).Error; err != nil {
				t.Fatal(err)
			}
			before := r3ReadDirectory(t, db)
			snapshot := catalog.Snapshot()
			notifications := notifier.count
			if len(before.Subjects) != 2 {
				t.Fatalf("subjects=%d, want 2", len(before.Subjects))
			}
			r3Populate(f, "after")
			now = now.Add(time.Minute)
			switch fault {
			case "credentials-error":
				f.credentialsErr = errors.New("credentials offline")
			case "credentials-nil":
				f.credentialsResult = nil
			case "management-error":
				f.managementAPIKeysErr = errors.New("management offline")
			case "management-nil":
				f.managementAPIKeysResult = nil
			case "provider-error":
				f.standardErrors["xai"] = errors.New("xai offline")
			case "provider-nil":
				f.standardResults["xai"] = nil
			}
			writes := 0
			watchWrite := func(tx *gorm.DB) { writes++ }
			for _, register := range []func(string, func(*gorm.DB)) error{
				db.Callback().Create().Before("gorm:create").Register,
				db.Callback().Update().Before("gorm:update").Register,
				db.Callback().Delete().Before("gorm:delete").Register,
			} {
				if err := register("test:r3_write_observer", watchWrite); err != nil {
					t.Fatal(err)
				}
			}
			// Neither subjects nor newly compiled pricing may become visible in
			// the catalog while the database writer is still in its transaction.
			if err := db.Callback().Query().After("gorm:query").Register("test:r3_no_early_publication", func(tx *gorm.DB) {
				if catalog.Snapshot() != snapshot {
					tx.AddError(errors.New("catalog published before commit"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			injected := false
			if strings.HasSuffix(fault, "-write") {
				if err := db.Callback().Create().Before("gorm:create").Register("test:r3_injected_write", func(tx *gorm.DB) {
					block := false
					switch fault {
					case "credentials-write":
						block = tx.Statement.Table == "usage_identities" && strings.Contains(fmt.Sprint(tx.Statement.Dest), "oauth-after")
					case "provider-write":
						block = tx.Statement.Table == "usage_identities" && strings.Contains(fmt.Sprint(tx.Statement.Dest), "provider-after")
					case "management-write":
						block = tx.Statement.Table == "cpa_api_keys"
					case "subject-write":
						block = tx.Statement.Table == "credential_pricing_subjects"
					}
					if block {
						injected = true
						tx.AddError(errors.New("injected " + fault))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "catalog-load" {
				if err := db.Callback().Query().Before("gorm:query").Register("test:r3_catalog_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "model_price_settings" {
						injected = true
						tx.AddError(errors.New("injected catalog load"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SyncCredentialDirectory(context.Background()); err == nil {
				t.Fatal("expected failure")
			}
			// Remove injected callbacks before independent state readback.
			if err := db.Callback().Create().Remove("test:r3_injected_write"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().Remove("test:r3_catalog_failure"); err != nil {
				t.Fatal(err)
			}
			if (strings.HasSuffix(fault, "-write") || fault == "catalog-load") && !injected {
				t.Fatal("fault injection did not execute")
			}
			if after := r3ReadDirectory(t, db); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed %s published partial directory\nbefore=%+v\nafter=%+v", fault, before, after)
			}
			if !strings.HasSuffix(fault, "-write") && fault != "catalog-load" && writes != 0 {
				t.Fatalf("partial fetch attempted %d writes before preflight", writes)
			}
			if catalog.Snapshot() != snapshot {
				t.Fatal("failed refresh replaced catalog")
			}
			if notifier.count != notifications {
				t.Fatal("failed refresh notified aggregation")
			}
			if !catalog.MutationMutex().TryLock() {
				t.Fatal("failed refresh leaked mutex")
			}
			catalog.MutationMutex().Unlock()
			// Every error branch must allow a subsequent successful refresh.
			delete(f.standardErrors, "xai")
			f.standardResults["xai"] = &response.ProviderKeyConfigResult{StatusCode: 200}
			r3Populate(f, "recovered")
			f.managementAPIKeysErr = nil
			if err := s.SyncCredentialDirectory(context.Background()); err != nil {
				t.Fatalf("recovery: %v", err)
			}
			if catalog.Snapshot() == snapshot || notifier.count != notifications+1 {
				t.Fatal("successful recovery did not publish exactly once and notify")
			}
		})
	}
}

func TestR3CompleteSnapshotSkipsAllScopesAfterMutation(t *testing.T) {
	for _, kind := range []string{"oauth", "provider", "openai-group", "oauth-no-catalog", "provider-no-catalog", "openai-group-no-catalog"} {
		t.Run(kind, func(t *testing.T) {
			noCatalog := strings.HasSuffix(kind, "-no-catalog")
			kind := strings.TrimSuffix(kind, "-no-catalog")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db := openMetadataTestDatabase(t, "r3-stale.db")
			f := newMetadataTestFetcher()
			r3Populate(f, "before")
			if kind == "openai-group" {
				f.openAIResult.Payload = []providerconfig.OpenAICompatibilityConfig{{Name: "group", Priority: metadataIntPointer(0), APIKeyEntries: []providerconfig.OpenAIApiKeyEntry{{APIKey: "a", AuthIndex: "target"}, {APIKey: "b", AuthIndex: "sibling"}}}}
			}
			coordinator := &service.CredentialMetadataCoordinator{}
			catalog := pricing.NewCatalog(pricing.EmptySnapshot())
			if noCatalog {
				catalog = nil
			}
			n := &r3Notifier{}
			s := service.NewSyncServiceWithOptions(db, service.SyncServiceOptions{MetadataFetcher: f, CredentialMetadata: coordinator, PricingCatalog: catalog, UsageAggregationNotifier: n})
			if err := s.SyncCredentialDirectory(ctx); err != nil {
				t.Fatal(err)
			}
			r3Populate(f, "after")
			held, release := make(chan struct{}), make(chan struct{})
			old := f.standardResults["codex"]
			f.standardHooks["codex"] = func(ctx context.Context) (*response.ProviderKeyConfigResult, error) {
				close(held)
				select {
				case <-release:
					return old, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			done := make(chan error, 1)
			go func() { done <- s.SyncCredentialDirectory(ctx) }()
			select {
			case <-held:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `[{"name":"group","priority":0,"keys":[{"api-key":"private-before","auth_index":"provider-before"},{"api-key":"a","auth_index":"target"},{"api-key":"b","auth_index":"sibling"}]}]`)
					return
				}
				fmt.Fprint(w, `{"status":"ok"}`)
			}))
			defer server.Close()
			mutation := service.NewCredentialPriorityService(db, cpa.NewClient(server.URL, "local-test", time.Second, false), &credentialStatusRefresherStub{}, &service.CredentialMutationLocks{}, coordinator)
			var err error
			switch kind {
			case "oauth":
				_, err = mutation.SetCredentialPriority(ctx, "oauth-before", 17)
			case "provider":
				_, err = mutation.SetAIProviderPriority(ctx, "provider-before", 17)
			case "openai-group":
				_, err = mutation.SetAIProviderPriority(ctx, "target", 17)
			}
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			before := r3ReadDirectory(t, db)
			snapshot := catalog.Snapshot()
			notifications := n.count
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if after := r3ReadDirectory(t, db); !reflect.DeepEqual(before, after) {
				t.Fatalf("stale complete snapshot changed directory\nbefore=%+v\nafter=%+v", before, after)
			}
			if catalog.Snapshot() != snapshot || n.count != notifications {
				t.Fatal("stale complete refresh published/notified")
			}
		})
	}
}
