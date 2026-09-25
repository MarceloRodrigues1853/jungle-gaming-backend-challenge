# Migrations

`001_financial_core.up.sql` cria o schema financeiro. A migration
`002_reversal_exclusivity.up.sql` exige referência interna nas reversões concluídas e
permite apenas uma reversão bem-sucedida por transação referenciada. O valor monetário
é persistido em unidades mínimas (`BIGINT`), compatível com
`domain.Money.MinorUnits()`; nenhuma coluna usa ponto flutuante.

Com PostgreSQL disponível e `psql` instalado, aplique a partir da raiz do projeto:

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
