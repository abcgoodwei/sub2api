//go:build integration

package repository

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIHarvestAuthConditionalDisable(t *testing.T) {
	ctx := context.Background()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	repo := &accountRepository{client: client, sql: integrationDB}
	a, err := client.Account.Create().SetName("harvest-auth-synthetic").SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).SetStatus(service.StatusActive).SetSchedulable(true).
		SetCredentials(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}).Save(ctx)
	require.NoError(t, err)
	changed, err := repo.SetOpenAIHarvestErrorIfCredentialsMatch(ctx, a.ID, "old-access", "old-refresh", "rejected")
	require.NoError(t, err)
	require.False(t, changed, "late 401 must not disable new credentials")
	changed, err = repo.SetOpenAIHarvestErrorIfCredentialsMatch(ctx, a.ID, "", "old-refresh", "rejected")
	require.NoError(t, err)
	require.False(t, changed, "late refresh error must not disable new credentials")
	changed, err = repo.SetOpenAIHarvestErrorIfCredentialsMatch(ctx, a.ID, "new-access", "new-refresh", "rejected")
	require.NoError(t, err)
	require.True(t, changed)
	current, err := client.Account.Get(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusError, current.Status)
	require.False(t, current.Schedulable)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&count))
	require.Positive(t, count)
}
