BEGIN;

ALTER TABLE wager_transactions
    ADD COLUMN reference_attempts integer NOT NULL DEFAULT 0 CHECK (reference_attempts >= 0),
    ADD COLUMN next_reference_attempt_at timestamptz,
    ADD COLUMN reference_expires_at timestamptz,
    ADD COLUMN reference_locked_until timestamptz,
    ADD COLUMN reference_locked_by text,
    ADD CONSTRAINT wager_transactions_reference_retry_shape_check CHECK (
        (status = 'PENDING_REFERENCE'
         AND next_reference_attempt_at IS NOT NULL
         AND reference_expires_at IS NOT NULL
         AND reference_expires_at >= next_reference_attempt_at)
        OR
        (status <> 'PENDING_REFERENCE'
         AND next_reference_attempt_at IS NULL
         AND reference_expires_at IS NULL
         AND reference_locked_until IS NULL
         AND reference_locked_by IS NULL)
    ),
    ADD CONSTRAINT wager_transactions_reference_lock_owner_check CHECK (
        (reference_locked_until IS NULL AND reference_locked_by IS NULL)
        OR
        (reference_locked_until IS NOT NULL AND length(trim(reference_locked_by)) > 0)
    );

CREATE INDEX wager_transactions_reference_retry_idx
    ON wager_transactions (next_reference_attempt_at, created_at, id)
    WHERE status = 'PENDING_REFERENCE';

COMMIT;
