BEGIN;

ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_transactions_processed_win_reference_check,
    DROP CONSTRAINT wager_transactions_reference_shape_check;

-- Restaura a regra anterior, que não permitia referência externa em WIN.
ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_transactions_reference_shape_check
    CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind NOT IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NULL)
    );

COMMIT;
