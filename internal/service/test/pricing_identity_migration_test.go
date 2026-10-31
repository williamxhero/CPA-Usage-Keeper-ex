package test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/cpa/dto/credentials"
	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
	"cpa-usage-keeper/internal/cpa/dto/response"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func identityState(t *testing.T, provider service.PricingProvider) service.PricingIdentityState {
	t.Helper()
	state, err := provider.(service.PricingIdentityMigrationProvider).GetPricingIdentityState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func migrationSelection(t *testing.T, state service.PricingIdentityState, directoryID int64) string {
	t.Helper()
	for _, item := range state.Directory {
		if item.Credential.DirectoryID == directoryID {
			return item.Ref
		}
	}
	t.Fatal("missing safe directory selection")
	return ""
}
func migrateFixtureIdentity(t *testing.T, f credentialDefaultFixture, directoryID int64) service.PricingIdentityMutationResult {
	t.Helper()
	state := identityState(t, f.prices)
	result, err := f.prices.(service.PricingIdentityMigrationProvider).MigratePricingIdentity(context.Background(), service.PricingIdentityMigrationInput{SubjectID: f.subjectID, DirectoryRef: migrationSelection(t, state, directoryID), SnapshotID: state.SnapshotID, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func fixtureDirectory(t *testing.T, f credentialDefaultFixture, index string) entities.UsageIdentity {
	t.Helper()
	var identity entities.UsageIdentity
	if err := f.db.Where("identity = ? AND auth_type = ?", index, entities.UsageIdentityAuthTypeAuthFile).First(&identity).Error; err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestIdentityMigrationRealServiceAllCostsStablePricesChannelsAndReopen(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	channels := f.prices.(service.PricingChannelsProvider)
	channel, err := channels.CreatePricingChannel(ctx, service.PricingChannelInput{Name: "Saved agreement", MemberSubjectIDs: []string{f.subjectID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channels.SetChannelDefault(ctx, channel.ID, ".3"); err != nil {
		t.Fatal(err)
	}
	next := fixtureDirectory(t, f, "synthetic-b")
	// Matching mutable labels/endpoints never creates an association.
	if err := f.db.Model(&next).Updates(map[string]any{"name": "Synthetic A", "base_url": "https://same.example.invalid"}).Error; err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 33.44, true)
	old := f.catalog.NewResolver()
	result := migrateFixtureIdentity(t, f, next.ID)
	if result.SubjectID != f.subjectID || !result.Enabled || result.BindingRef == "" {
		t.Fatalf("migration %+v", result)
	}
	f.assertCostFamilies(t, 6, true)
	closeCost(t, old.Calculate(repository.UsageEventCostSubject(f.events[1])).Cost.TotalCostUSD, 29.4)
	for _, event := range f.events {
		cost := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(event))
		if cost.CredentialSubjectID != f.subjectID || cost.ChannelID != channel.ID {
			t.Fatalf("lost stable selection %+v", cost)
		}
	}
	readback, err := channels.GetPricingChannel(ctx, channel.ID)
	if err != nil || len(readback.MemberSubjectIDs) != 1 || readback.MemberSubjectIDs[0] != f.subjectID || readback.Multiplier == nil || *readback.Multiplier != .3 {
		t.Fatalf("channel preservation %+v %v", readback, err)
	}
	var original entities.CredentialPricingSubject
	if err := f.db.First(&original, "id = ?", f.subjectID).Error; err != nil {
		t.Fatal(err)
	}
	if original.Identity != "synthetic-a" || original.AuthTypeName != "oauth" {
		t.Fatal("migration overwrote original relation")
	}
	subjects, err := f.prices.(service.PricingCredentialProvider).ListCredentialPricingSubjects(ctx)
	if err != nil || len(subjects) != 1 {
		t.Fatalf("duplicate subjects %+v %v", subjects, err)
	}
	// Channel inheritance and already delivered model exceptions follow the same
	// retained subject automatically; migration never copies price records.
	if _, err := f.defaults.ClearCredentialDefault(ctx, f.subjectID); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 9, true)
	if _, err := f.prices.(service.PricingCredentialModelsProvider).SetCredentialModel(ctx, f.subjectID, "base", ".4"); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 12, true)
	var files []struct {
		Name string
		File string
	}
	if err := f.db.Raw("PRAGMA database_list").Scan(&files).Error; err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, file := range files {
		if file.Name == "main" {
			path = file.File
		}
	}
	if path == "" {
		t.Fatal("missing isolated database file")
	}
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := repository.OpenDatabase(config.Config{SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	reopenedSQL, err := reopened.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedSQL.Close() })
	f.db = reopened
	f.prices, f.catalog = newCatalogPricingService(t, reopened)
	f.defaults = f.prices.(service.PricingCredentialDefaultsProvider)
	f.assertCostFamilies(t, 12, true)
	state := identityState(t, f.prices)
	if len(state.Bindings) != 2 || state.Bindings[1].Ref != result.BindingRef {
		t.Fatalf("restart associations %+v", state.Bindings)
	}
}

func TestIdentityCorrectionSeparateExplicitUnbindRebindKeepsOriginalAndConfiguration(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	next := fixtureDirectory(t, f, "synthetic-b")
	migrated := migrateFixtureIdentity(t, f, next.ID)
	third := entities.UsageIdentity{Name: "Correction account", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: "synthetic-c", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	other, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, other.SubjectID, ".5"); err != nil {
		t.Fatal(err)
	}
	provider := f.prices.(service.PricingIdentityMigrationProvider)
	correction := func(ref, owner, target, action string, confirmed bool) error {
		state := identityState(t, f.prices)
		_, err := provider.CorrectPricingIdentity(ctx, service.PricingIdentityCorrectionInput{BindingRef: ref, ExpectedSubjectID: owner, TargetSubjectID: target, Action: action, SnapshotID: state.SnapshotID, Confirmed: confirmed})
		return err
	}
	before := f.catalog.Snapshot()
	if !errors.Is(correction(migrated.BindingRef, f.subjectID, other.SubjectID, "rebind", false), service.ErrPricingBindingConfirmation) || f.catalog.Snapshot() != before {
		t.Fatal("unconfirmed correction changed binding")
	}
	if err := correction(migrated.BindingRef, f.subjectID, other.SubjectID, "rebind", true); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 8.94, true)
	originalRef := "binding_" + f.subjectID
	pinned := f.catalog.NewResolver()
	if err := correction(originalRef, f.subjectID, "", "unbind", true); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 62.5, false)
	closeCost(t, pinned.Calculate(repository.UsageEventCostSubject(f.events[0])).Cost.TotalCostUSD, 1.96)
	if got := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(f.events[0])); got.CredentialSubjectID != "" {
		t.Fatalf("disabled original still prices %+v", got)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".3"); err != nil {
		t.Fatal("saved unbound subject remains configurable", err)
	}
	if got := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(f.events[0])); got.CredentialSubjectID != "" {
		t.Fatal("saving default resurrected original")
	}
	if _, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, fixtureDirectory(t, f, "synthetic-a").ID); !errors.Is(err, service.ErrCredentialBindingConflict) {
		t.Fatal("normal registration bypassed correction", err)
	}
	if err := correction(originalRef, f.subjectID, f.subjectID, "rebind", true); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 10.96, true) // A 6.06 at .3; B 4.9 at its corrected owner .5.
	var original entities.CredentialPricingSubject
	if err := f.db.First(&original, "id = ?", f.subjectID).Error; err != nil || original.Identity != "synthetic-a" {
		t.Fatalf("original lost %+v %v", original, err)
	}
}

func TestIdentityMigrationRefusesOccupiedSharedStaleAndCommitFailuresAtomically(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	next := fixtureDirectory(t, f, "synthetic-b")
	provider := f.prices.(service.PricingIdentityMigrationProvider)
	attempt := func(state service.PricingIdentityState, confirmed bool) error {
		_, err := provider.MigratePricingIdentity(ctx, service.PricingIdentityMigrationInput{SubjectID: f.subjectID, DirectoryRef: migrationSelection(t, state, next.ID), SnapshotID: state.SnapshotID, Confirmed: confirmed})
		return err
	}
	oldState := identityState(t, f.prices)
	if !errors.Is(attempt(oldState, false), service.ErrPricingBindingConfirmation) {
		t.Fatal("confirmation required")
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(attempt(oldState, true), service.ErrPricingBindingStale) {
		t.Fatal("stale selection accepted")
	}
	before := f.catalog.Snapshot()
	if err := f.db.Model(&next).Update("binding_identity_status", "ambiguous").Error; err != nil {
		t.Fatal(err)
	}
	if err := attempt(identityState(t, f.prices), true); err == nil {
		t.Fatal("shared identity accepted")
	}
	if f.catalog.Snapshot() != before {
		t.Fatal("rejection published candidate")
	}
	if err := f.db.Model(&next).Update("binding_identity_status", "unique").Error; err != nil {
		t.Fatal(err)
	}
	// A real deferred foreign-key failure occurs after candidate compilation.
	if err := f.db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TABLE migration_commit_probe (subject_id TEXT, FOREIGN KEY(subject_id) REFERENCES credential_pricing_subjects(id) DEFERRABLE INITIALLY DEFERRED)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_migration_commit AFTER INSERT ON credential_pricing_associations BEGIN INSERT INTO migration_commit_probe(subject_id) VALUES ('absent-subject'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := attempt(identityState(t, f.prices), true); err == nil {
		t.Fatal("expected commit failure")
	}
	if f.catalog.Snapshot() != before || len(identityState(t, f.prices).Bindings) != 1 {
		t.Fatal("failed commit changed effective associations")
	}
	if err := f.db.Exec("DROP TRIGGER fail_migration_commit").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	before = f.catalog.Snapshot()
	if err := attempt(identityState(t, f.prices), true); err == nil || f.catalog.Snapshot() != before {
		t.Fatal("occupied identity was stolen")
	}
}

func TestIdentityMigrationScopedStaleRestoreTimeoutAndExactHistoricalEvidence(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	fetcher := newMetadataTestFetcher()
	oldFile := credentials.Credential{AuthIndex: "synthetic-a", Email: "safe@example.invalid", Name: "synthetic.json", Type: "codex", Provider: "codex"}
	newFile := oldFile
	newFile.AuthIndex = "rotated-index"
	fetcher.setCredentials([]credentials.Credential{newFile})
	syncer := service.NewSyncServiceWithOptions(f.db, service.SyncServiceOptions{BaseURL: "https://cpa.example.invalid", MetadataFetcher: fetcher, Now: func() time.Time { return f.now }, PricingCatalog: f.catalog})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	state := identityState(t, f.prices)
	if state.Bindings[0].Credential.Status != "stale" {
		t.Fatalf("not reliably stale %+v", state.Bindings)
	}
	var next entities.UsageIdentity
	if err := f.db.Where("identity = ?", newFile.AuthIndex).First(&next).Error; err != nil {
		t.Fatal(err)
	}
	read := func(index, kind string) float64 {
		return f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(entities.UsageEvent{Model: "base", AuthIndex: index, AuthType: kind, InputTokens: 1_000_000})).Cost.TotalCostUSD
	}
	closeCost(t, read("synthetic-a", "oauth"), 2)
	closeCost(t, read(newFile.AuthIndex, "oauth"), 5)
	migrated := migrateFixtureIdentity(t, f, next.ID)
	closeCost(t, read(newFile.AuthIndex, "oauth"), 2)
	closeCost(t, read(" rotated-index ", "oauth"), 5)
	closeCost(t, read(newFile.AuthIndex, "apikey"), 5)
	fetcher.credentialsErr = context.DeadlineExceeded
	if err := syncer.SyncMetadata(ctx); err == nil {
		t.Fatal("expected controlled timeout warning")
	}
	if state := identityState(t, f.prices); !state.Bindings[1].Enabled || state.Bindings[1].Credential.Status == "stale" {
		t.Fatalf("timeout deleted association %+v", state.Bindings)
	}
	closeCost(t, read(newFile.AuthIndex, "oauth"), 2)
	fetcher.credentialsErr = nil
	fetcher.setCredentials([]credentials.Credential{oldFile, newFile})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	state = identityState(t, f.prices)
	if state.Bindings[0].Credential.Status != "active" || state.Bindings[1].Ref != migrated.BindingRef {
		t.Fatalf("restore %+v", state.Bindings)
	}
	closeCost(t, read("synthetic-a", "oauth"), 2)
}

func TestIdentityMigrationExactWhitespaceTypedAndRetainedClampingCompleteness(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	spaced := entities.UsageIdentity{Name: "Rotated account", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: " synthetic-b ", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&spaced).Error; err != nil {
		t.Fatal(err)
	}
	migrateFixtureIdentity(t, f, spaced.ID)
	for _, row := range []struct {
		index, kind string
		cost        float64
	}{{spaced.Identity, "oauth", 1.96}, {"synthetic-b", "oauth", 29.4}, {spaced.Identity, "apikey", 29.4}, {spaced.Identity, "unknown", 29.4}} {
		event := f.events[1]
		event.AuthIndex, event.AuthType = row.index, row.kind
		closeCost(t, f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(event)).Cost.TotalCostUSD, row.cost)
	}
	migrateFixtureIdentity(t, f, fixtureDirectory(t, f, "synthetic-b").ID)
	f.assertCostFamilies(t, 6, true)
	// Retained archive facts reproduce nonlinear per-event clamping, not a
	// normalized rollup approximation. Hot/archive overlap remains deduplicated.
	var first entities.UsageEvent
	if err := f.db.First(&first, "event_key = ?", "default-abnormal-a").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("INSERT INTO usage_events_archive ("+entities.UsageEventStorageColumns+") SELECT "+entities.UsageEventStorageColumns+" FROM usage_events WHERE id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 6, false)
	if err := f.db.Delete(&entities.UsageEvent{}, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 6, false)
	if err := f.db.Delete(&entities.UsageEventArchive{}, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Summary.CostAvailable || overview.Summary.UnavailableReason != "retained_pricing_evidence_incomplete" {
		t.Fatalf("missing historical evidence was hidden %+v", overview)
	}
}

func TestIdentityMigrationCorrectionPreservesPersistedFixedModesDefaultsChannels(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	third := entities.UsageIdentity{Name: "Separate owner", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: "synthetic-c", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	other, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	channels := f.prices.(service.PricingChannelsProvider)
	channelIDs := []string{}
	for _, subject := range []string{f.subjectID, other.SubjectID} {
		channel, err := channels.CreatePricingChannel(ctx, service.PricingChannelInput{Name: "Saved contract", MemberSubjectIDs: []string{subject}})
		if err != nil {
			t.Fatal(err)
		}
		channelIDs = append(channelIDs, channel.ID)
		if _, err := channels.SetChannelDefault(ctx, channel.ID, ".3"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.defaults.SetCredentialDefault(ctx, subject, ".2"); err != nil {
			t.Fatal(err)
		}
	}
	fixed := f.prices.(service.PricingCredentialFixedProvider)
	if _, err := fixed.SetCredentialFixed(ctx, f.subjectID, "observed-model", syntheticFixed(1, "openai")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixed.SetCredentialFixed(ctx, other.SubjectID, "observed-model", syntheticFixed(2, "claude")); err != nil {
		t.Fatal(err)
	}
	var before []entities.CredentialModelMultiplier
	if err := f.db.Order("subject_id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 32.9, true)
	migrated := migrateFixtureIdentity(t, f, fixtureDirectory(t, f, "synthetic-b").ID)
	f.assertCostFamilies(t, 5.4, true)
	channelFixture{credentialDefaultFixture: f}.assertChannels(t, map[string]float64{channelIDs[0]: 5.4})
	state := identityState(t, f.prices)
	if _, err := f.prices.(service.PricingIdentityMigrationProvider).CorrectPricingIdentity(ctx, service.PricingIdentityCorrectionInput{BindingRef: migrated.BindingRef, ExpectedSubjectID: f.subjectID, TargetSubjectID: other.SubjectID, Action: "rebind", SnapshotID: state.SnapshotID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 7.3, true)
	channelFixture{credentialDefaultFixture: f}.assertChannels(t, map[string]float64{channelIDs[0]: 3.5, channelIDs[1]: 3.8})
	var after []entities.CredentialModelMultiplier
	if err := f.db.Order("subject_id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("identity mutation changed persisted fixed configuration")
	}
	f.prices, f.catalog = newCatalogPricingService(t, f.db)
	f.assertCostFamilies(t, 7.3, true)
	for _, subject := range []string{f.subjectID, other.SubjectID} {
		config, err := f.prices.(service.PricingCredentialModelsProvider).GetCredentialModel(ctx, subject, "observed-model")
		if err != nil || config.Mode != "fixed" || config.Fixed == nil || config.Multiplier != nil {
			t.Fatalf("lost fixed mode %+v %v", config, err)
		}
		defaultConfig, err := f.prices.(service.PricingCredentialDefaultsProvider).GetCredentialDefault(ctx, subject)
		if err != nil || defaultConfig.Multiplier == nil || *defaultConfig.Multiplier != .2 {
			t.Fatalf("lost default %+v %v", defaultConfig, err)
		}
	}
}

func TestIdentityMigrationCandidateAndCorrectionCommitFailureKeepPublishedOwnership(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	provider := f.prices.(service.PricingIdentityMigrationProvider)
	before := f.catalog.Snapshot()
	bad := entities.CredentialModelMultiplier{SubjectID: f.subjectID, Model: "invalid-candidate", Mode: "unsupported", Multiplier: .2}
	if err := f.db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	state := identityState(t, f.prices)
	if _, err := provider.MigratePricingIdentity(ctx, service.PricingIdentityMigrationInput{SubjectID: f.subjectID, DirectoryRef: migrationSelection(t, state, fixtureDirectory(t, f, "synthetic-b").ID), SnapshotID: state.SnapshotID, Confirmed: true}); !errors.Is(err, repository.ErrInvalidPricingSnapshot) {
		t.Fatalf("candidate failure %v", err)
	}
	if f.catalog.Snapshot() != before || len(identityState(t, f.prices).Bindings) != 1 {
		t.Fatal("candidate failure published or committed association")
	}
	if err := f.db.Where("subject_id = ? AND model = ?", f.subjectID, bad.Model).Delete(&entities.CredentialModelMultiplier{}).Error; err != nil {
		t.Fatal(err)
	}
	migrated := migrateFixtureIdentity(t, f, fixtureDirectory(t, f, "synthetic-b").ID)
	before = f.catalog.Snapshot()
	if err := f.db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TABLE correction_commit_probe (subject_id TEXT, FOREIGN KEY(subject_id) REFERENCES credential_pricing_subjects(id) DEFERRABLE INITIALLY DEFERRED)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_correction_commit AFTER UPDATE ON credential_pricing_associations BEGIN INSERT INTO correction_commit_probe(subject_id) VALUES ('absent-subject'); END`).Error; err != nil {
		t.Fatal(err)
	}
	state = identityState(t, f.prices)
	if _, err := provider.CorrectPricingIdentity(ctx, service.PricingIdentityCorrectionInput{BindingRef: migrated.BindingRef, ExpectedSubjectID: f.subjectID, Action: "unbind", SnapshotID: state.SnapshotID, Confirmed: true}); err == nil {
		t.Fatal("expected deferred correction commit failure")
	}
	if f.catalog.Snapshot() != before || !identityState(t, f.prices).Bindings[1].Enabled {
		t.Fatal("failed correction changed ownership")
	}
	f.assertCostFamilies(t, 6, true)
	if _, err := provider.MigratePricingIdentity(ctx, service.PricingIdentityMigrationInput{SubjectID: f.subjectID, DirectoryRef: migrationSelection(t, state, fixtureDirectory(t, f, "synthetic-b").ID), SnapshotID: state.SnapshotID, Confirmed: true}); err == nil {
		t.Fatal("duplicate migration accepted")
	}
}

func TestIdentityMigrationProviderPartialFailurePreservesMultipleExactAssociations(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	fetcher := newMetadataTestFetcher()
	file := credentials.Credential{AuthIndex: "synthetic-a", Name: "safe.json", Email: "safe@example.invalid", Type: "codex", Provider: "codex"}
	fetcher.setCredentials([]credentials.Credential{file})
	fetcher.standardResults["codex"] = &response.ProviderKeyConfigResult{StatusCode: 200, Payload: []providerconfig.ProviderKeyConfig{{AuthIndex: "rotated-api", APIKey: "synthetic-private-provider-key", Name: "Safe provider"}}}
	syncer := service.NewSyncServiceWithOptions(f.db, service.SyncServiceOptions{BaseURL: "https://cpa.example.invalid", MetadataFetcher: fetcher, Now: func() time.Time { return f.now }, PricingCatalog: f.catalog})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	var identity entities.UsageIdentity
	if err := f.db.Where("identity = ? AND auth_type = ?", "rotated-api", entities.UsageIdentityAuthTypeAIProvider).First(&identity).Error; err != nil {
		t.Fatal(err)
	}
	migrateFixtureIdentity(t, f, identity.ID)
	check := func() {
		t.Helper()
		for _, event := range []entities.UsageEvent{{Model: "base", AuthType: "apikey", AuthIndex: "rotated-api", InputTokens: 1_000_000}, {Model: "base", AuthType: "oauth", AuthIndex: "synthetic-a", InputTokens: 1_000_000}} {
			cost := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(event))
			closeCost(t, cost.Cost.TotalCostUSD, 2)
			if cost.CredentialSubjectID != f.subjectID {
				t.Fatal("lost migrated exact owner")
			}
		}
	}
	check()
	fetcher.standardErrors["claude"] = context.DeadlineExceeded
	if err := syncer.SyncMetadata(ctx); err == nil {
		t.Fatal("expected controlled partial warning")
	}
	check()
	fetcher.standardErrors["codex"] = context.DeadlineExceeded
	if err := syncer.SyncMetadata(ctx); err == nil {
		t.Fatal("expected scoped timeout")
	}
	check()
	state := identityState(t, f.prices)
	for _, binding := range state.Bindings {
		if !binding.Enabled || binding.Credential.Status != "active" {
			t.Fatalf("timeout marked saved association deleted %+v", binding)
		}
	}
}

func TestIdentityMigrationConcurrentPinnedReaders(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	migrated := migrateFixtureIdentity(t, f, fixtureDirectory(t, f, "synthetic-b").ID)
	provider := f.prices.(service.PricingIdentityMigrationProvider)
	var wg sync.WaitGroup
	failures := make(chan error, 100)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				pinned := f.catalog.NewResolver()
				a := pinned.Calculate(repository.UsageEventCostSubject(f.events[0]))
				b := pinned.Calculate(repository.UsageEventCostSubject(f.events[1]))
				if math.Abs(a.Cost.TotalCostUSD-1.96) > 1e-9 || (math.Abs(b.Cost.TotalCostUSD-1.96) > 1e-9 && math.Abs(b.Cost.TotalCostUSD-29.4) > 1e-9) {
					failures <- fmt.Errorf("mixed pinned result %+v %+v", a, b)
					return
				}
				if (b.CredentialSubjectID == f.subjectID) != (math.Abs(b.Cost.TotalCostUSD-1.96) < 1e-9) {
					failures <- fmt.Errorf("inconsistent ownership %+v", b)
					return
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		action, target := "unbind", ""
		if i%2 == 1 {
			action, target = "rebind", f.subjectID
		}
		state := identityState(t, f.prices)
		if _, err := provider.CorrectPricingIdentity(ctx, service.PricingIdentityCorrectionInput{BindingRef: migrated.BindingRef, ExpectedSubjectID: f.subjectID, TargetSubjectID: target, Action: action, SnapshotID: state.SnapshotID, Confirmed: true}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
