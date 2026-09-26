BEGIN;

-- A migration inicial exigia referência para reversões, mas também bloqueava a
-- referência opcional de WIN prevista no contrato do desafio.
ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_transactions_check3;

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_transactions_reference_shape_check
    CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR kind = 'WIN'
        OR (kind NOT IN ('WIN', 'REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NULL)
    ),
    ADD CONSTRAINT wager_transactions_processed_win_reference_check
    CHECK (
        kind <> 'WIN'
        OR status <> 'PROCESSED'
        OR reference_external_transaction_id IS NULL
        OR reference_transaction_id IS NOT NULL
    );

COMMIT;
