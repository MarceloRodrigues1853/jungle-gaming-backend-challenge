BEGIN;

CREATE TABLE wallets (
    id          text PRIMARY KEY CHECK (length(trim(id)) > 0),
    player_id   text NOT NULL CHECK (length(trim(player_id)) > 0),
    currency    char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance     bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    version     bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    UNIQUE (player_id, currency),
    UNIQUE (id, player_id, currency),
    CHECK (updated_at >= created_at)
);

CREATE TABLE wager_transactions (
    id                              text PRIMARY KEY CHECK (length(trim(id)) > 0),
    source                          text NOT NULL CHECK (source IN ('INTERNAL', 'EXTERNAL')),
    provider_id                     text,
    external_transaction_id         text,
    idempotency_key                 text,
    payload_hash                   bytea,
    wallet_id                       text NOT NULL,
    player_id                       text NOT NULL,
    currency                        char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    round_id                        text,
    game_id                         text,
    kind                            text NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    amount_minor                    bigint NOT NULL CHECK (amount_minor >= 0),
    reference_external_transaction_id text,
    reference_transaction_id        text,
    status                          text NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    failure_code                    text,
    result_balance_minor            bigint CHECK (result_balance_minor >= 0),
    created_at                      timestamptz NOT NULL,
    updated_at                      timestamptz NOT NULL,
    processed_at                    timestamptz,
    FOREIGN KEY (wallet_id, player_id, currency) REFERENCES wallets (id, player_id, currency),
    CHECK (updated_at >= created_at),
    CHECK (
      (source = 'INTERNAL' AND kind = 'OPENING' AND provider_id IS NULL
       AND external_transaction_id IS NULL AND idempotency_key IS NULL
       AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
       AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)
      OR
      (source = 'EXTERNAL' AND kind <> 'OPENING' AND provider_id IS NOT NULL AND length(trim(provider_id)) > 0
       AND external_transaction_id IS NOT NULL AND length(trim(external_transaction_id)) > 0
       AND idempotency_key IS NOT NULL AND length(trim(idempotency_key)) > 0
       AND payload_hash IS NOT NULL AND octet_length(payload_hash) = 32
       AND round_id IS NOT NULL AND length(trim(round_id)) > 0
       AND game_id IS NOT NULL AND length(trim(game_id)) > 0)
    ),
    CHECK ((kind IN ('BET', 'WIN', 'REFUND', 'ROLLBACK') AND amount_minor > 0)
        OR (kind IN ('LOSS', 'OPENING') AND amount_minor >= 0)),
    CHECK ((kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind NOT IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NULL)),
    CHECK ((status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL))
);

CREATE UNIQUE INDEX wager_transactions_provider_external_uq
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE source = 'EXTERNAL';
CREATE UNIQUE INDEX wager_transactions_provider_idempotency_uq
    ON wager_transactions (provider_id, idempotency_key)
    WHERE source = 'EXTERNAL';
CREATE INDEX wager_transactions_pending_idx
    ON wager_transactions (created_at, id) WHERE status IN ('PENDING', 'PENDING_REFERENCE');
CREATE UNIQUE INDEX wager_transactions_single_opening_uq
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';
CREATE UNIQUE INDEX wager_transactions_single_successful_reversal_uq
    ON wager_transactions (reference_transaction_id, kind)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_transactions_reference_fk
    FOREIGN KEY (reference_transaction_id) REFERENCES wager_transactions (id);

CREATE TABLE wallet_ledger_entries (
    id              text PRIMARY KEY CHECK (length(trim(id)) > 0),
    wallet_id       text NOT NULL REFERENCES wallets (id),
    transaction_id  text NOT NULL REFERENCES wager_transactions (id),
    direction       text NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount_minor    bigint NOT NULL CHECK (amount_minor > 0),
    balance_before  bigint NOT NULL CHECK (balance_before >= 0),
    balance_after   bigint NOT NULL CHECK (balance_after >= 0),
    created_at      timestamptz NOT NULL,
    UNIQUE (wallet_id, transaction_id),
    CHECK (CASE direction
        WHEN 'CREDIT' THEN balance_before <= 9223372036854775807::bigint - amount_minor
                         AND balance_after = balance_before + amount_minor
        WHEN 'DEBIT' THEN balance_before >= amount_minor
                        AND balance_after = balance_before - amount_minor
        ELSE FALSE
    END)
);

CREATE FUNCTION reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only' USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER wallet_ledger_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TABLE inbox_messages (
    consumer_name  text NOT NULL CHECK (length(trim(consumer_name)) > 0),
    message_id     text NOT NULL CHECK (length(trim(message_id)) > 0),
    payload_hash   bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    received_at    timestamptz NOT NULL,
    completed_at   timestamptz,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    event_id       text PRIMARY KEY CHECK (length(trim(event_id)) > 0),
    aggregate_id   text NOT NULL CHECK (length(trim(aggregate_id)) > 0),
    event_type     text NOT NULL CHECK (length(trim(event_type)) > 0),
    payload        jsonb NOT NULL,
    occurred_at    timestamptz NOT NULL,
    attempts       integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    locked_until   timestamptz,
    published_at   timestamptz,
    last_error     text
);
CREATE INDEX outbox_events_pending_idx
    ON outbox_events (next_attempt_at, occurred_at, event_id)
    WHERE published_at IS NULL;

COMMIT;
