# Migrations

`001_financial_core.up.sql` cria o schema financeiro. A migration
`002_reversal_exclusivity.up.sql` exige referência interna nas reversões concluídas e
permite apenas uma reversão bem-sucedida por transação referenciada. O valor monetário
é persistido em unidades mínimas (`BIGINT`), compatível com
`domain.Money.MinorUnits()`; nenhuma coluna usa ponto flutuante.

Inicie o PostgreSQL local pela raiz do projeto:

```sh
docker compose up -d postgres
docker compose ps
```

O serviço publica a porta somente em `127.0.0.1:5432`, persiste os dados no volume
`postgres_data` e usa uma senha de desenvolvimento local (`local_dev_only`) quando
`POSTGRES_PASSWORD` não é definida. Sobrescreva-a no ambiente antes de subir o serviço
se preferir outra credencial. Para parar sem apagar os dados, use
`docker compose down`; não use `docker compose down -v` a menos que queira remover o
volume e todos os dados locais.

As migrations também podem ser aplicadas sem instalar `psql` no host, usando o cliente
que vem no container:

```sh
docker compose exec -T postgres psql -U jungle_app -d jungle_gaming -v ON_ERROR_STOP=1 < migrations/001_financial_core.up.sql
docker compose exec -T postgres psql -U jungle_app -d jungle_gaming -v ON_ERROR_STOP=1 < migrations/002_reversal_exclusivity.up.sql
```

Com as migrations aplicadas, execute os testes de integração contra esse banco. No
PowerShell:

```powershell
$env:JUNGLE_TEST_DATABASE_URL = "postgres://jungle_app:local_dev_only@127.0.0.1:5432/jungle_gaming?sslmode=disable"
go test ./...
```

No Git Bash:

```sh
JUNGLE_TEST_DATABASE_URL="postgres://jungle_app:local_dev_only@127.0.0.1:5432/jungle_gaming?sslmode=disable" go test ./...
```

Os testes criam carteiras e transações com IDs exclusivos; esses registros permanecem
no volume local e não são apagados, preservando a regra append-only do ledger.

Se usar credenciais sobrescritas, ajuste usuário e banco nos comandos acima. Com
PostgreSQL e `psql` instalados localmente, as alternativas a seguir também funcionam:

```powershell
psql "$env:DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_financial_core.up.sql
psql "$env:DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/002_reversal_exclusivity.up.sql
```

No Bash, use:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_financial_core.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/002_reversal_exclusivity.up.sql
```

Para reverter todas as migrations, execute os arquivos `down` em ordem inversa: primeiro
`002_reversal_exclusivity.down.sql`, depois `001_financial_core.down.sql`. A primeira
restaura a regra de unicidade anterior; a segunda remove todas as tabelas e dados
financeiros do schema. Faça backup antes de usar a segunda em qualquer ambiente com
dados que precisem ser preservados.

Cada migration controla sua própria transação. `ON_ERROR_STOP` faz o `psql` encerrar
no primeiro erro. Aplicação e rollback automatizados pelo binário da aplicação ainda
serão adicionados quando a composição da infraestrutura for implementada.

O adaptador `internal/postgres` utiliza `pgx/v5` e persiste uma operação financeira
com `READ COMMITTED` e `SELECT ... FOR UPDATE` na linha da carteira. A inserção da
transação, alteração de saldo, registro do resultado e lançamento de ledger pertencem
ao mesmo commit; veja `ARCHITECTURE.md` para o limite exato e as garantias ainda
pendentes, incluindo replay de idempotência e outbox.
