package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// A late probe/refresh failure must not overwrite a concurrent reauthorization.
// Persist the scheduler event in the same statement as the conditional disable.
func (r *accountRepository) SetOpenAIHarvestErrorIfCredentialsMatch(ctx context.Context, id int64, accessToken, refreshToken, reason string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `
 WITH updated AS (
  UPDATE accounts AS a SET status=$1,error_message=$2,schedulable=FALSE,updated_at=NOW()
  WHERE a.id=$3 AND a.deleted_at IS NULL AND a.platform=$4 AND a.status=$5
   AND a.type IN ($6,$7)
   AND (($8<>'' AND a.credentials->>'access_token'=$8)
     OR ($8='' AND $9<>'' AND a.credentials->>'refresh_token'=$9))
  RETURNING a.id
 )
 INSERT INTO scheduler_outbox (event_type,account_id,group_id,payload)
 SELECT $10,updated.id,NULL,NULL FROM updated
 `, service.StatusError, reason, id, service.PlatformOpenAI, service.StatusActive,
		service.AccountTypeOAuth, service.AccountTypeSetupToken, accessToken, refreshToken, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}
