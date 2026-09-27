# Execução local da API

## Dependências

- Go na versão declarada em `go.mod`;
- Docker Desktop com os serviços `postgres`, `keycloak` e `localstack` ativos;
- migrations 001, 002, 003, 004 e 005 aplicadas conforme `migrations/README.md`.

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
`SQS_MAX_RECEIVE_COUNT`,
`OUTBOX_POLL_INTERVAL`, `OUTBOX_LOCK_DURATION`, `OUTBOX_BATCH_SIZE`,
`REFERENCE_POLL_INTERVAL`, `REFERENCE_LOCK_DURATION`, `REFERENCE_BATCH_SIZE` e
`REFERENCE_MAX_ATTEMPTS`.

Uma `REFUND` ou `ROLLBACK` cuja operação original ainda não chegou retorna `202` e é
persistida como `PENDING_REFERENCE`. O worker tenta novamente com backoff, recupera
reservas abandonadas e rejeita com `REFERENCE_NOT_FOUND` após 10 tentativas ou 24 horas.

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
GET http://127.0.0.1:8090/metrics
```

`live` confirma o processo HTTP. `ready` consulta PostgreSQL, fila de entrada e fila
de saída, retornando `503` quando qualquer dependência não está pronta.

`metrics` é público para coleta local e usa o formato Prometheus. Ele expõe resultados
por status, replays idempotentes, retries e candidatos à DLQ do SQS, conflitos de
concorrência, retries e atraso da outbox, latência financeira e divergências de
reconciliação. Identificadores de jogador, carteira, transação e provedor não são
usados como labels.

## Composição e lifecycle do Fx

Com PostgreSQL, Keycloak e LocalStack ativos, valide a construção dos módulos e todos
os hooks reais de início e encerramento:

```powershell
$env:JUNGLE_BOOTSTRAP_TEST = "1"
go test -v ./internal/bootstrap -run TestAppStartsAndStopsWithRealDependencies -count=1
```

O teste usa uma porta HTTP temporária, inicia os três workers e confirma o encerramento
gracioso antes de fechar o pool PostgreSQL.

## Teste distribuído com três processos

Depois de subir as dependências e aplicar as migrations, inicie o perfil distribuído:

```sh
docker compose --profile distributed up --build -d api-1 api-2 api-3
JUNGLE_DISTRIBUTED_TEST=1 go test -v ./internal/e2e -count=1
```

No PowerShell:

```powershell
docker compose --profile distributed up --build -d api-1 api-2 api-3
$env:JUNGLE_DISTRIBUTED_TEST = "1"
go test -v ./internal/e2e -count=1
```

Os testes obtêm tokens `client_credentials` reais e usam as portas `8091`, `8092` e
`8093`. O primeiro disputa duas apostas sobre a mesma carteira e reinicia os três
serviços; após o reinício, confirma saldo `20.00`, dois lançamentos no ledger,
reconciliação sem diferença e replay idempotente. O segundo envia a mesma aposta
simultaneamente por HTTP e pela fila real, executa mais 50 reenvios concorrentes e
confirma um único débito. O terceiro envia um evento inválido e aguarda o redrive real
para `wager-transactions-dlq.fifo` após cinco recebimentos.

O perfil distribuído reduz apenas os tempos de espera e visibilidade do consumidor
para um segundo. Isso mantém a política de cinco recebimentos, mas permite verificar
a DLQ localmente sem esperar vários minutos.

Para reproduzir `go test -race` no Windows sem instalar um compilador C:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.4-alpine3.23 sh -c "apk add --no-cache gcc musl-dev && go test -race ./..."
```

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

## Consultas e reconciliação

Com o token interno, liste o ledger usando paginação por cursor:

```text
GET http://127.0.0.1:8090/wallets/WALLET_ID/ledger?limit=50
GET http://127.0.0.1:8090/wallets/WALLET_ID/ledger?limit=50&cursor=NEXT_CURSOR
```

O campo `nextCursor` aparece somente quando existe outra página. Para reconstruir o
saldo sem alterar a carteira:

```text
POST http://127.0.0.1:8090/wallets/WALLET_ID/reconciliation
Authorization: Bearer <internal_access_token>
```

Com o token de `provider-a`, consulte uma operação interna ou sua identidade externa:

```text
GET http://127.0.0.1:8090/wagering/transactions/TRANSACTION_ID
GET http://127.0.0.1:8090/providers/provider-a/wagering/transactions/EXTERNAL_TRANSACTION_ID
Authorization: Bearer <provider_access_token>
```

O provedor presente na URL precisa coincidir com a identidade do token. Uma tentativa
de consultar outro provedor retorna `403`.

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
