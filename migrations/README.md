# Migrations

`001_financial_core.up.sql` cria o schema financeiro e `001_financial_core.down.sql`
remove esse schema. O valor monetário é persistido em unidades mínimas (`BIGINT`),
compatível com `domain.Money.MinorUnits()`; nenhuma coluna usa ponto flutuante.

Com PostgreSQL disponível e `psql` instalado, aplique a partir da raiz do projeto:

```powershell
psql "$env:DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_financial_core.up.sql
```

No Bash, use:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_financial_core.up.sql
```

Para reverter, execute o arquivo `down` correspondente com o mesmo comando. **A
reversão remove todas as tabelas e os dados financeiros deste schema.** Faça backup
antes de usá-la em qualquer ambiente com dados que precisem ser preservados.

Cada migration controla sua própria transação. `ON_ERROR_STOP` faz o `psql` encerrar
no primeiro erro. Aplicação e rollback automatizados pelo binário da aplicação ainda
serão adicionados quando a composição da infraestrutura for implementada.
