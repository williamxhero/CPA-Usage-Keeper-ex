package service

import (
	repodto "cpa-usage-keeper/internal/repository/dto"
	"testing"
)

func TestCurrentReadMappingsDoNotAliasIndependentCredentialCollections(t *testing.T) {
	realtime := mapRealtimeCurrentUsage(repodto.RealtimeCurrentUsageRecord{
		AuthFiles:   []repodto.RealtimeUsageTopItemRecord{{Key: "current-auth", Tokens: 1}},
		Credentials: []repodto.RealtimeUsageTopItemRecord{{Key: "stored-credential", Tokens: 2}},
	})
	if len(realtime.AuthFiles) != 1 || realtime.AuthFiles[0].Key != "current-auth" || len(realtime.Credentials) != 1 || realtime.Credentials[0].Key != "stored-credential" {
		t.Fatalf("realtime collections aliased: %+v", realtime)
	}
	analysis := mapAnalysisRecord(&repodto.AnalysisRecord{
		AuthFilesComposition:   []repodto.AnalysisCompositionRecord{{Key: "current-auth", CostUSD: 33.44}},
		CredentialsComposition: []repodto.AnalysisCompositionRecord{{Key: "stored-credential", CostUSD: 100}},
	})
	if len(analysis.AuthFilesComposition) != 1 || analysis.AuthFilesComposition[0].Key != "current-auth" || analysis.AuthFilesComposition[0].CostUSD != 33.44 || len(analysis.CredentialsComposition) != 1 || analysis.CredentialsComposition[0].Key != "stored-credential" || analysis.CredentialsComposition[0].CostUSD != 100 {
		t.Fatalf("analysis collections aliased: %+v", analysis)
	}
}
