BEGIN;

DROP INDEX IF EXISTS wager_transactions_reference_retry_idx;
ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_reference_lock_owner_check,
    DROP CONSTRAINT IF EXISTS wager_transactions_reference_retry_shape_check,
    DROP COLUMN IF EXISTS reference_locked_by,
    DROP COLUMN IF EXISTS reference_locked_until,
    DROP COLUMN IF EXISTS reference_expires_at,
    DROP COLUMN IF EXISTS next_reference_attempt_at,
    DROP COLUMN IF EXISTS reference_attempts;

COMMIT;
