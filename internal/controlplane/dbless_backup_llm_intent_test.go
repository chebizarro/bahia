package controlplane

import (
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDBLessBackupAndLLMIntentsRejectMissingRepositories(t *testing.T) {
	actor := nostr.Generate()
	backupRegistry := service.NewBackupRegistryService(repository.NewPgBackupControlPlaneRepository(nil), nil, zap.NewNop())
	for _, tc := range []struct {
		name    string
		domain  string
		op      string
		handler DomainHandler
		want    error
	}{
		{"backup recipe", "backup", "recipe-apply", NewBackupIntentHandler(BackupIntentHandlerConfig{
			Registry: backupRegistry, SQLRepositoryUnavailable: true,
		}), ErrBackupRepositoryUnavailable},
		{"backup repository", "backup", "repository-register", NewBackupIntentHandler(BackupIntentHandlerConfig{
			Registry: backupRegistry, SQLRepositoryUnavailable: true,
		}), ErrBackupRepositoryUnavailable},
		{"LLM route", "llm", "update", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{}), ErrLLMRepositoryUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := uuid.NewV7()
			require.NoError(t, err)
			event := signedLLMRequest(t, actor.Hex(), 30900, `{"id":"`+id.String()+`"}`, nostr.Tags{
				{"d", id.String()}, {"domain", tc.domain}, {"schema", "bahia.intent." + tc.domain + ".v1"},
				{"t", "bahia-intent"}, {"op", tc.op}, {"org", testOrgID().String()}, {"intent_id", id.String()},
			})
			intent, err := ParseIntent(event)
			require.NoError(t, err)
			intent.Actor = actor.Public().Hex()
			statuses := &statusCollector{}
			processor := NewIntentProcessor(NewTrustSet([]string{intent.Actor}, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): intent.Actor})), openTestStore(t),
				NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
				IntentProcessorConfig{EnabledDomains: map[string]bool{tc.domain: true}}, zap.NewNop())
			processor.RegisterHandler(tc.domain, tc.handler)
			err = processor.ProcessRelayIntent(t.Context(), event)
			if tc.domain == "backup" {
				require.ErrorContains(t, err, "canonical backup publisher is unavailable")
			} else {
				require.True(t, errors.Is(err, tc.want), "error = %v", err)
			}
			require.Len(t, statuses.events, 1)
			require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
		})
	}
}

func TestDBLessBackupRunKeepsCanonicalAdmissionPath(t *testing.T) {
	actor := nostr.Generate()
	event := signedBackupRunFixture(t, actor, nil)
	intent, err := ParseIntent(&event)
	require.NoError(t, err)
	intent.Actor = actor.Public().Hex()
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{SQLRepositoryUnavailable: true})
	err = handler.HandleIntent(t.Context(), intent)
	require.ErrorContains(t, err, "canonical acceptance is unavailable")
	require.NotErrorIs(t, err, ErrBackupRepositoryUnavailable)
}
