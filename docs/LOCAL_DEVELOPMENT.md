# Execução local da API

## Dependências

- Go na versão declarada em `go.mod`;
- Docker Desktop com os serviços `postgres`, `keycloak` e `localstack` ativos;
- migrations 001, 002, 003 e 004 aplicadas conforme `migrations/README.md`.

Inicie a infraestrutura:

```sh
docker compose up -d postgres keycloak localstack
docker compose ps
```

Inicie a API em outro terminal:

```sh
go run ./cmd/api
```

Por padrão, a aplicação usa `127.0.0.1:8090`, PostgreSQL e Keycloak locais. Esses
valores são apenas conveniências de desenvolvimento. Podem ser substituídos pelas
variáveis `HTTP_ADDRESS`, `DATABASE_URL`, `OIDC_INTROSPECTION_URL`,
`OIDC_INTROSPECTION_CLIENT_ID`, `OIDC_INTROSPECTION_CLIENT_SECRET`,
`PROVIDER_CLIENT_ID`, `PROVIDER_ID`, `AWS_REGION`, `SQS_ENDPOINT`,
`SQS_OUTPUT_QUEUE_URL`, `SQS_INPUT_QUEUE_URL`, `SQS_CONSUMER_NAME`,
`SQS_WAIT_TIME_SECONDS`, `SQS_VISIBILITY_TIMEOUT_SECONDS`, `SQS_MAX_MESSAGES`,
`OUTBOX_POLL_INTERVAL`, `OUTBOX_LOCK_DURATION` e `OUTBOX_BATCH_SIZE`.

O LocalStack provisiona `jungle-events.fifo` para eventos de saída e também prepara
`wager-transactions.fifo` e sua DLQ. O publicador
usa `eventId` como `MessageDeduplicationId` e `aggregateId` como `MessageGroupId`.
O consumidor usa o `messageId` do envelope na inbox e só remove uma mensagem depois
que inbox, transação, saldo, ledger e outbox foram confirmados no PostgreSQL.

## Envio manual pela fila SQS

Com a aplicação e os containers ativos, envie o envelope abaixo pela AWS CLI do
LocalStack. Substitua `PLAYER_ID` e `WALLET_ID` por uma carteira existente. Use o
`walletId` como `MessageGroupId` e um valor novo como `MessageDeduplicationId`:

```sh
docker compose exec -T localstack awslocal sqs send-message \
  --queue-url http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/wager-transactions.fifo \
  --message-group-id WALLET_ID \
  --message-deduplication-id sqs-demo-1 \
  --message-body '{"messageId":"sqs-demo-1","type":"WagerTransactionRequested","occurredAt":"2026-09-26T23:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"sqs-demo-bet-1","idempotencyKey":"provider-a:sqs-demo-bet-1","playerId":"PLAYER_ID","walletId":"WALLET_ID","roundId":"sqs-round-1","gameId":"sqs-demo","kind":"BET","money":{"amount":"1.00","currency":"BRL"}}}'
```

Confirme o processamento no banco:

```sql
SELECT consumer_name, message_id, completed_at FROM inbox_messages ORDER BY received_at DESC LIMIT 5;
SELECT id, status, result_balance_minor FROM wager_transactions ORDER BY created_at DESC LIMIT 5;
```

Mensagens inválidas ou falhas transitórias não são apagadas. A visibilidade recebe
backoff e, depois de cinco recebimentos, o redrive envia a mensagem para
`wager-transactions-dlq.fifo`.

O cliente `wallet-internal`, reservado às operações administrativas de carteira, usa
o segredo local `wallet-internal-local-secret`. Tokens desse cliente não
são aceitos no endpoint de apostas, e tokens de `provider-a` não concedem papel interno.

## Health checks

```http
GET http://127.0.0.1:8090/health/live
GET http://127.0.0.1:8090/health/ready
```

`live` confirma o processo HTTP. `ready` consulta o PostgreSQL e retorna `503` quando
a dependência não está pronta.

## Token no Postman

Crie uma requisição `POST`:

```text
http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token
```

Use `Body > x-www-form-urlencoded`:

```text
grant_type    client_credentials
client_id     provider-a
client_secret provider-a-local-secret
```

Copie somente o campo `access_token` da resposta. As credenciais são públicas e
inseguras por projeto; devem ser usadas exclusivamente no ambiente local.

Para testar abertura ou leitura de carteira, gere outro token com os mesmos campos,
alterando somente estas credenciais:

```text
client_id     wallet-internal
client_secret wallet-internal-local-secret
```

Use esse segundo `access_token` exclusivamente nas rotas `/wallets`.

## Abertura e leitura de carteira

Envie `POST http://127.0.0.1:8090/wallets` com `Authorization: Bearer
<internal_access_token>`, `Content-Type: application/json` e o corpo:

```json
{
  "playerId": "postman-player-1",
  "initialBalance": {
    "amount": "100.00",
    "currency": "BRL"
  }
}
```

A resposta esperada é `201` com `id`, saldo textual e `version: 1`. Copie o campo `id`
e consulte `GET http://127.0.0.1:8090/wallets/<id>` com o mesmo token interno; a resposta
esperada é `200`. Uma segunda abertura para o mesmo `playerId` e moeda retorna `409`.

## Envio de uma aposta

Crie uma requisição `POST` para:

```text
http://127.0.0.1:8090/wagering/transactions
```

Headers:

```text
Authorization   Bearer <access_token>
Content-Type    application/json
Idempotency-Key provider-a:postman-bet-1
```

Body JSON, substituindo `playerId` e `walletId` por uma carteira existente:

```json
{
  "providerId": "provider-a",
  "externalTransactionId": "postman-bet-1",
  "playerId": "PLAYER_ID",
  "walletId": "WALLET_ID",
  "roundId": "postman-round-1",
  "gameId": "postman-demo",
  "kind": "BET",
  "money": {
    "amount": "1.00",
    "currency": "BRL"
  }
}
```

O primeiro envio processado retorna `201`. Reenvie sem modificar body ou headers para
obter `200` com `idempotentReplay: true`; o saldo e o ledger não serão alterados outra
vez. Se alterar o conteúdo mantendo a mesma chave, a API retorna `409`.

Use `Ctrl+C` no terminal da API para acionar o shutdown gracioso do Fx.
