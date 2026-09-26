# Execução local da API

## Dependências

- Go na versão declarada em `go.mod`;
- Docker Desktop com os serviços `postgres` e `keycloak` ativos;
- migrations 001, 002 e 003 aplicadas conforme `migrations/README.md`.

Inicie a infraestrutura:

```sh
docker compose up -d postgres keycloak
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
`PROVIDER_CLIENT_ID` e `PROVIDER_ID`.

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
