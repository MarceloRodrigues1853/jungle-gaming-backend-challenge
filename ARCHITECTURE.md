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

`NewExternalTransaction` recebe um hash de 32 bytes já calculado pelo caso de uso
compartilhado entre HTTP e SQS.

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
infraestrutura. Quando a referência não existe, a operação é confirmada como
`PENDING_REFERENCE` junto com `WagerTransactionPendingReference` na outbox.

Um worker reserva pendências com `FOR UPDATE SKIP LOCKED`, tentativas e proprietário
duráveis. A cada tentativa ele procura a referência no mesmo provedor; quando encontra
uma operação `PROCESSED`, resolve o vínculo e confirma saldo, ledger, estado e eventos
no mesmo commit. Enquanto ela não está pronta, reagenda com backoff exponencial.
A política atual usa TTL de 24 horas e no máximo 10 tentativas; o primeiro limite
atingido encerra a transação como `REJECTED`, usa `REFERENCE_NOT_FOUND` e publica o
evento de rejeição. Reservas abandonadas expiram após 30 segundos e podem ser
assumidas por outra instância.

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
como strings. Esses registros ainda não significam publicação: o worker da outbox é
responsável pelo envio e pela confirmação em `published_at`.

O publicador reserva lotes com `FOR UPDATE SKIP LOCKED`, registra `locked_by`, prazo
da reserva e número da tentativa, e permite múltiplas instâncias sem lock global.
Reservas abandonadas voltam a ficar elegíveis após o prazo. Falhas de SQS liberam o
registro com backoff exponencial limitado; sucesso preenche `published_at` somente se
a reserva ainda pertence ao worker. No FIFO de saída, `eventId` é a identidade de
deduplicação e `aggregateId` mantém a ordem por agregado. Como a confirmação do SQS e
do PostgreSQL não é atômica, uma interrupção entre essas duas etapas pode republicar
o mesmo `eventId`; consumidores devem deduplicá-lo.

### Consumidor SQS e inbox

`internal/sqsconsumer` usa long polling sobre `wager-transactions.fifo`, valida um
único envelope JSON sem campos desconhecidos e delega a mesma operação usada pelo
HTTP para `application.WagerService`. O corpo bruto recebe SHA-256 e o par
`(consumerName, messageId)` identifica a entrega na inbox. Uma reentrega com o mesmo
ID e conteúdo é segura; reutilizar o ID com outro hash é conflito permanente.

O PostgreSQL insere ou bloqueia a inbox dentro da mesma transação que bloqueia a
carteira, registra a operação, atualiza saldo, cria ledger e grava outbox. O campo
`completed_at` é preenchido antes desse único commit. O consumidor só chama
`DeleteMessage` depois do retorno bem-sucedido do commit, portanto uma interrupção
antes da remoção causa reentrega e replay persistente, não novo débito.

Falhas deixam a mensagem na fila e aumentam sua visibility timeout até o limite de
cinco minutos. A política de redrive move a mensagem para
`wager-transactions-dlq.fifo` após cinco recebimentos. O `MessageGroupId` de entrada
deve ser o `walletId`, mantendo ordem por carteira no broker sem substituir os locks
do PostgreSQL. No shutdown, o Fx cancela o long polling, aguarda o processamento atual
e só depois fecha as dependências.

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
de idempotência usa `409`, referência ainda ausente retorna `202`, carteira inexistente usa `404` e
indisponibilidade transitória usa `503`. Os erros têm envelope JSON e código estável.

### Consultas e reconciliação

As consultas de transação sempre incluem o `providerId` autenticado no predicado SQL.
`GET /wagering/transactions/{transactionId}` e a busca por ID externo não retornam uma
operação pertencente a outro provedor. Estados pendentes, códigos de falha, referência
resolvida e saldo observado no processamento permanecem disponíveis no resultado.

O ledger é ordenado por `(created_at, id)` decrescente e usa um cursor opaco Base64URL
com os dois valores. A próxima página aplica comparação de tupla, evitando repetir ou
pular registros com o mesmo timestamp. O limite padrão é 50 e o máximo é 100.

`POST /wallets/{walletId}/reconciliation` abre uma transação PostgreSQL somente de
leitura em `REPEATABLE READ`. Nessa mesma visão, lê o saldo armazenado e calcula
créditos menos débitos do ledger. A resposta informa os dois valores, diferença,
quantidade de lançamentos e consistência, sem modificar a carteira.

O binário `cmd/api` compõe configuração, pool PostgreSQL, repositórios, autenticação,
casos de uso, handlers e servidor usando `fx.Provide` e `fx.Invoke`. O pool é validado
no `OnStart`; depois o servidor abre sua porta. No `OnStop`, a ordem inversa encerra o
HTTP graciosamente antes de fechar o pool. O servidor configura prazos de leitura,
escrita, cabeçalhos e conexões ociosas. Instruções de Postman estão em
`docs/LOCAL_DEVELOPMENT.md`.

`GET /health/live` confirma somente que o processo HTTP responde. `GET /health/ready`
usa prazo de dois segundos para consultar PostgreSQL e os destinos SQS de entrada e
saída; devolve `503` se qualquer dependência não estiver disponível.

`GET /metrics` expõe métricas no formato Prometheus por processo. São contabilizados
resultados por estado, replays, retries e candidatos à DLQ do consumidor, conflitos
de concorrência, retries e atraso da outbox, latência de processamento e divergências
de reconciliação. As séries usam apenas labels de cardinalidade limitada; IDs e
payloads financeiros não aparecem nas métricas. Divergências também produzem log JSON
com `walletId` e quantidade de lançamentos, sem registrar valores financeiros.

O lock da carteira é adquirido antes da inserção de uma nova transação que possa
movimentar saldo. A ordem é intencional: inserir primeiro poderia manter um lock de
chave estrangeira e depois disputar `FOR UPDATE`, formando um deadlock entre processos.
O teste `internal/e2e` executa três contêineres com pools e memórias independentes,
dispara a disputa obrigatória e reinicia todos antes de verificar dados e replays. A
mesma suíte disputa uma operação entre HTTP e SQS, distribui 50 reenvios pelas três
instâncias e confirma que há somente um débito. Um cenário separado usa o redrive real
do LocalStack para comprovar a chegada de uma mensagem inválida à DLQ depois de cinco
recebimentos. O perfil de teste reduz visibility timeout e long polling para um
segundo, sem alterar o limite de tentativas.

O Keycloak anuncia `http://127.0.0.1:8080` como hostname canônico e permite backchannel
dinâmico. Assim, tokens obtidos no host mantêm o mesmo emissor quando a introspecção é
feita pela URL interna `http://keycloak:8080` da rede Docker.

## Próximas decisões e trabalho pendente

Ainda está pendente uma auditoria final do mapeamento de conflitos de reversão para
códigos de rejeição. As estratégias pendentes não serão apresentadas como garantias
até receberem teste correspondente.
