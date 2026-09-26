BEGIN;

ALTER TABLE outbox_events
    ADD COLUMN locked_by text;

ALTER TABLE outbox_events
    ADD CONSTRAINT outbox_events_lock_consistency_check CHECK (
        (locked_until IS NULL AND locked_by IS NULL)
        OR
        (locked_until IS NOT NULL AND locked_by IS NOT NULL AND length(trim(locked_by)) > 0)
    );

COMMIT;
