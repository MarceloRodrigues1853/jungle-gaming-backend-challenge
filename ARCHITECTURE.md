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

Uma referência só é aceita se apontar para uma transação `PROCESSED` do mesmo provedor,
jogador, carteira e rodada. `WIN` pode referenciar uma `BET` sem exigir valores iguais;
`REFUND` só pode referenciar uma `BET` e devolve seu valor integral; `ROLLBACK` pode
referenciar `BET`, `WIN` ou `REFUND` e inverte o movimento dessa transação. `BET` debita,
`WIN` e `REFUND` creditam, `LOSS` não movimenta saldo; `ROLLBACK` credita uma `BET`
revertida e debita um `WIN` ou `REFUND` revertido.

Política para evitar dupla devolução: uma transação pode ter no máximo uma reversão
bem-sucedida no total. Portanto, uma `BET` processada pode receber `REFUND` ou
`ROLLBACK`, mas não ambos. É permitido reverter uma `REFUND` com `ROLLBACK`; nesse caso,
a aposta continua marcada como já estornada pelo `REFUND`, impedindo uma segunda
reversão direta da `BET`. O domínio valida o tipo e o vínculo da referência; a migration
002 reforça a exclusividade no PostgreSQL. A tradução de conflitos de unicidade em um
código estável de rejeição será feita pelo caso de uso da aplicação.

`NewExternalTransaction` recebe um hash de 32 bytes já calculado. A canonicalização
do payload e o algoritmo/campos exatos do hash serão definidos no caso de uso comum
de HTTP e SQS, ainda não implementado.

### Processamento financeiro em memória

`ProcessWagerTransaction` coordena, em memória, a validação do vínculo com a carteira,
a aplicação do movimento, a criação do lançamento e a transição da transação para
`PROCESSED`. Carteira e transação são trabalhadas em cópias e publicadas juntas apenas
quando todas as validações passam. `LOSS` é processada sem lançamento ou incremento da
versão da carteira. Saldo insuficiente rejeita a transação sem alterar carteira nem
criar ledger; uma reversão que exigiria saldo indisponível usa o código estável
`REVERSAL_INSUFFICIENT_BALANCE`, distinto de `INSUFFICIENT_FUNDS` para aposta.

Essa atomicidade vale somente dentro desta chamada no processo. Ela não protege contra
concorrência entre requisições nem substitui uma transação PostgreSQL; bloqueio/controle
de versão e gravação atômica serão responsabilidade do caso de uso e repositório.

### Persistência PostgreSQL

O adaptador `internal/postgres` usa `pgx/v5` com SQL explícito. `Money` é mapeado para
`BIGINT` em unidades mínimas e a moeda segue em coluna `CHAR(3)`; a reidratação usa
`MoneyFromMinorUnits`, sem conversões para ponto flutuante.

`Store.ProcessWagerTransaction` abre uma transação `READ COMMITTED`, bloqueia a linha
da carteira por `SELECT ... FOR UPDATE`, insere a operação pendente, aplica as regras
do domínio e grava o saldo alterado, o resultado da operação e o lançamento no ledger
antes do mesmo `COMMIT`. Uma rejeição de negócio também fica auditável no registro da
transação, sem saldo nem ledger. `LOSS` não atualiza a carteira. O lock é por linha de
carteira: operações da mesma carteira são serializadas, mas carteiras independentes
podem avançar em paralelo em processos distintos.

O adaptador trata colisões dos índices únicos como possível replay: procura o registro
do mesmo provedor pela chave ou pelo ID externo e só devolve o resultado anterior quando
ambas as identidades e o hash do payload coincidem. Qualquer divergência resulta em
`ErrIdempotencyConflict`, sem nova movimentação. O saldo de replay vem de
`result_balance_minor`, não do saldo atual da carteira. O cálculo/canonicalização do
hash ainda pertence ao caso de uso comum de HTTP e SQS, que deve produzir os mesmos 32
bytes para o mesmo conteúdo de negócio.

Ainda não há publicação de outbox nesta operação; o registro atômico dos eventos será
adicionado antes de expor os fluxos HTTP/SQS.

## Próximas decisões e trabalho pendente

Ainda estão pendentes a canonicalização comum do payload entre HTTP/SQS, retomada de
referências pendentes com retry e expiração, mapeamento de conflitos de reversão para
códigos de rejeição, inbox/outbox, autenticação e isolamento por provedor, composição
e lifecycle com Uber Fx, API HTTP, consumidor SQS, métricas, logs estruturados, Docker
Compose e testes de integração/concorrência com serviços reais. As estratégias para
esses pontos serão documentadas junto com cada etapa, antes de serem apresentadas como
garantias da solução.
