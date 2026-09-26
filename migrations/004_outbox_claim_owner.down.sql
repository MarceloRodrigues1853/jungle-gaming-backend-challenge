BEGIN;

ALTER TABLE outbox_events
    DROP CONSTRAINT IF EXISTS outbox_events_lock_consistency_check,
    DROP COLUMN IF EXISTS locked_by;

COMMIT;
