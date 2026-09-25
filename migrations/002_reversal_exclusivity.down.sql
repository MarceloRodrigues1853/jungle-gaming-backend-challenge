BEGIN;

DROP INDEX wager_transactions_single_successful_reversal_per_reference_uq;
ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_transactions_processed_reversal_has_reference;

-- Restaura o comportamento anterior, que permitia um REFUND e um ROLLBACK
-- simultâneos apontando para a mesma transação.
CREATE UNIQUE INDEX wager_transactions_single_successful_reversal_uq
    ON wager_transactions (reference_transaction_id, kind)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

COMMIT;
