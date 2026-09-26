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
hash pertence ao caso de uso comum de HTTP e SQS em `internal/application`.

### Entrada comum e hash canônico

`application.WagerService` recebe o `providerId` da identidade autenticada e a chave de
idempotência do transporte, valida a representação textual de dinheiro e constrói a
mesma entidade para entradas HTTP ou SQS. Identificadores com espaços nas extremidades
são rejeitados em vez de normalizados silenciosamente; valores monetários exigem a
forma decimal exata já definida por `domain.ParseMoney`.

Operações com referência consultam o PostgreSQL por `(providerId,
externalTransactionId)`. A busca nunca usa somente o identificador externo, impedindo
que uma reversão de um provedor alcance a transação de outro. Ausência é devolvida ao
caso de uso como `ErrReferenceNotFound`; falhas de consulta permanecem erros de
infraestrutura. A persistência de `PENDING_REFERENCE` será adicionada na etapa de retry.

O hash é SHA-256 sobre um JSON produzido por uma struct de ordem fixa com os campos
`providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`,
`kind`, `amount`, `currency` e `referenceExternalTransactionId`. O valor monetário é
canonicalizado pela representação de `Money`. A chave de idempotência, IDs internos,
timestamps e metadados de HTTP/SQS ficam fora do hash. Assim, o mesmo conteúdo de
negócio gera os mesmos 32 bytes nos dois transportes, enquanto provedores ou conteúdos
diferentes não compartilham identidade.

Operações externas processadas ou rejeitadas gravam um snapshot do resultado na outbox
no mesmo commit financeiro. Movimentações confirmadas também gravam
`WalletBalanceChanged`; `LOSS` e rejeições não criam esse segundo evento. Replays
idempotentes devolvem o resultado já persistido sem duplicar eventos. A abertura
positiva de carteira usa o mesmo contrato e grava seus dois eventos no commit inicial.

Os envelopes possuem `eventId`, `eventType`, `aggregateId`, `correlationId`,
`occurredAt`, versão `1` e `data` tipado. Valores monetários continuam representados
como strings. Esses registros ainda não significam publicação: um worker separado
será responsável pelo envio e pela confirmação em `published_at`.

O publicador reserva lotes com `FOR UPDATE SKIP LOCKED`, registra `locked_by`, prazo
da reserva e número da tentativa, e permite múltiplas instâncias sem lock global.
Reservas abandonadas voltam a ficar elegíveis após o prazo. Falhas de SQS liberam o
registro com backoff exponencial limitado; sucesso preenche `published_at` somente se
a reserva ainda pertence ao worker. No FIFO de saída, `eventId` é a identidade de
deduplicação e `aggregateId` mantém a ordem por agregado. Como a confirmação do SQS e
do PostgreSQL não é atômica, uma interrupção entre essas duas etapas pode republicar
o mesmo `eventId`; consumidores devem deduplicá-lo.

### Identidade de provedores

O ambiente local usa Keycloak em modo de desenvolvimento, com o realm `jungle-dev` e
clientes de serviço para obter e introspectar tokens via `client_credentials`. O pacote
`internal/auth` consulta o endpoint de introspecção com credenciais de cliente
confidencial, aceita apenas tokens ativos e mapeia `client_id` por uma allowlist
explícita para um papel. `provider-a` recebe o papel `PROVIDER` e seu `providerId`;
`wallet-internal` recebe `INTERNAL` sem poder representar um provedor. Assim, a origem
autenticada determina tanto o escopo quanto o tipo de operação autorizado. As
credenciais do realm importado são apenas de desenvolvimento local.

O adaptador `internal/httpapi` protege `POST /wagering/transactions` com Bearer token,
introspecta a credencial e injeta o principal validado no contexto. O `providerId` do
JSON precisa coincidir com esse principal antes de o caso de uso ser chamado; ele nunca
é aceito isoladamente como prova de identidade. Um token interno é recusado nessa rota,
mesmo sendo válido. `POST /wallets` e `GET /wallets/{walletId}` exigem o papel
`INTERNAL`, impedindo que tokens de provedores sejam reutilizados para essa finalidade.

### Abertura e leitura de carteira

`POST /wallets` cria uma única carteira por `(playerId, currency)`. Saldo inicial zero
persiste somente a carteira. Saldo positivo persiste carteira, `OPENING` processada,
lançamento de crédito e os eventos `WagerTransactionProcessed` e
`WalletBalanceChanged` na outbox dentro de uma única transação PostgreSQL. A carteira
nasce com versão 1 nos dois casos. A constraint única do banco transforma uma segunda
abertura para o mesmo jogador e moeda em conflito HTTP `409`.

`GET /wallets/{walletId}` reidrata o agregado sem reaplicar movimentos e devolve valor
monetário como string. Carteira ausente retorna `404`. As duas rotas usam o mesmo limite
de autenticação interna e nunca aceitam um token de provedor.

### Contrato HTTP de operações

O endpoint limita o corpo a 64 KiB, exige `application/json`, rejeita campos desconhecidos,
múltiplos valores JSON e `Idempotency-Key` ausente. Uma criação processada retorna `201`;
replay idempotente retorna `200`; rejeição financeira retorna `422`; estados pendentes
retornam `202`. Entrada inválida usa `400`, divergência de identidade usa `403`, conflito
de idempotência ou referência ausente usa `409`, carteira inexistente usa `404` e
indisponibilidade transitória usa `503`. Os erros têm envelope JSON e código estável.

O binário `cmd/api` compõe configuração, pool PostgreSQL, repositórios, autenticação,
casos de uso, handlers e servidor usando `fx.Provide` e `fx.Invoke`. O pool é validado
no `OnStart`; depois o servidor abre sua porta. No `OnStop`, a ordem inversa encerra o
HTTP graciosamente antes de fechar o pool. O servidor configura prazos de leitura,
escrita, cabeçalhos e conexões ociosas. Instruções de Postman estão em
`docs/LOCAL_DEVELOPMENT.md`.

`GET /health/live` confirma somente que o processo HTTP responde. `GET /health/ready`
usa prazo de dois segundos para consultar o PostgreSQL e devolve `503` se a dependência
não estiver disponível. A prontidão de SQS será incorporada quando o consumidor existir.

## Próximas decisões e trabalho pendente

Ainda estão pendentes a retomada de referências pendentes com retry e expiração,
mapeamento de conflitos de reversão para códigos de rejeição, inbox, demais rotas da
API HTTP, consumidor SQS, métricas adicionais,
Dockerfile e testes de concorrência
distribuída com pelo menos três processos independentes. Os testes PostgreSQL locais já
cobrem replay e operações simultâneas, mas não substituem esse cenário multi-processo.
As estratégias para esses pontos serão documentadas junto com cada etapa, antes de serem
apresentadas como garantias da solução.
