BEGIN;

-- Toda reversão concluída precisa apontar para uma transação já resolvida.
ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_transactions_processed_reversal_has_reference
    CHECK (
        status <> 'PROCESSED'
        OR kind NOT IN ('REFUND', 'ROLLBACK')
        OR reference_transaction_id IS NOT NULL
    );

-- Uma transação só pode receber uma reversão bem-sucedida no total.
-- Isso torna REFUND e ROLLBACK mutuamente exclusivos sobre a mesma aposta.
DROP INDEX wager_transactions_single_successful_reversal_uq;
CREATE UNIQUE INDEX wager_transactions_single_successful_reversal_per_reference_uq
    ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

COMMIT;
