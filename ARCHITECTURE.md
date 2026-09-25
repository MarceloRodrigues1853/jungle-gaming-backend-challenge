# Decisões de arquitetura

Este documento acompanha a implementação em etapas. Cada seção distingue garantias
já implementadas de decisões que dependem das próximas camadas, para não apresentar
como concluído algo que ainda não foi construído.

## Domínio financeiro implementado

### Dinheiro

`domain.Money` armazena valores em unidades mínimas (`int64`) e carrega a moeda junto
do valor. A entrada decimal aceita exatamente duas casas e um código de três letras
maiúsculas. Parsing, aritmética e comparação não usam ponto flutuante; overflow e
operações entre moedas diferentes são rejeitados. A lista de moedas oficialmente
suportadas ainda será definida pela camada de aplicação.

### Carteira e ledger

`domain.Wallet` encapsula saldo e versão. Crédito e débito exigem valor positivo,
moeda compatível e timestamp não regressivo; débito sem saldo falha sem alterar o
estado. `WalletLedgerEntry` valida a equação entre saldo anterior, movimento e saldo
posterior. O schema inicial reforça unicidade, não negatividade e append-only no
PostgreSQL.

### Transações de aposta

`domain.WagerTransaction` separa criação (`NewExternalTransaction` e
`NewOpeningTransaction`) de reidratação (`RehydrateWagerTransaction`). A reidratação
valida um snapshot e não reaplica efeitos financeiros. O snapshot explícito permite
que adaptadores de persistência fora do pacote `domain` convertam dados sem expor os
campos mutáveis da entidade.

As regras de valor implementadas são:

- `BET`, `WIN`, `REFUND` e `ROLLBACK` exigem valor positivo;
- `LOSS` exige exatamente zero;
- `OPENING` é interno, não recebe metadados de provedor e exige valor positivo; a
  abertura de saldo zero não deve criar essa transação;
- `REFUND` e `ROLLBACK` exigem referência externa; `WIN` pode recebê-la;
- uma operação com referência informada só pode ser processada depois que a referência
  interna tiver sido resolvida.

O estado inicial é `PENDING`. Quando a referência ainda não existe, a operação pode
ir para `PENDING_REFERENCE`; ao resolver, volta a `PENDING` com o ID interno guardado.
`PROCESSED`, `REJECTED` e `FAILED` são terminais. O resultado de saldo é guardado na
transação processada para permitir replay do resultado original, sem recalcular a
operação.

`NewExternalTransaction` recebe um hash de 32 bytes já calculado. A canonicalização
do payload e o algoritmo/campos exatos do hash serão definidos no caso de uso comum
de HTTP e SQS, ainda não implementado.

## Próximas decisões e trabalho pendente

Ainda não estão implementados os adaptadores e garantias de execução: transações SQL
e coordenação por carteira, idempotência persistente, referência pendente com retry e
expiração, resolução de conflitos de reversão, inbox/outbox, autenticação e isolamento
por provedor, composição e lifecycle com Uber Fx, API HTTP, consumidor SQS, métricas,
logs estruturados, Docker Compose e testes de integração com serviços reais. As
estratégias para esses pontos serão documentadas junto com cada etapa, antes de serem
apresentadas como garantias da solução.
