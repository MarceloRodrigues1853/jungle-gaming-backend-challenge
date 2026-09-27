// Package e2e contém verificações opt-in contra a infraestrutura local real.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

var distributedEndpoints = []string{
	"http://127.0.0.1:8091",
	"http://127.0.0.1:8092",
	"http://127.0.0.1:8093",
}

// TestThreeIndependentProcessesPreserveFinancialConsistency demonstra as
// garantias mais importantes usando três binários, pools e memórias separados.
func TestDistributedThreeProcessesPreserveFinancialConsistency(t *testing.T) {
	requireDistributedEnvironment(t)
	client := &http.Client{Timeout: 10 * time.Second}
	waitForAPIs(t, client)
	internalToken := clientCredentialsToken(t, client, "wallet-internal", "wallet-internal-local-secret")
	providerToken := clientCredentialsToken(t, client, "provider-a", "provider-a-local-secret")

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	playerID := "distributed-player-" + suffix
	wallet := openWallet(t, client, distributedEndpoints[0], internalToken, playerID)

	requests := []wagerRequest{
		newBet("distributed-bet-a-"+suffix, wallet.ID, playerID),
		newBet("distributed-bet-b-"+suffix, wallet.ID, playerID),
	}
	statuses := submitConcurrently(t, client, providerToken, requests)
	sort.Ints(statuses)
	if statuses[0] != http.StatusCreated || statuses[1] != http.StatusUnprocessableEntity {
		t.Fatalf("concurrent statuses = %v, want [201 422]", statuses)
	}

	// Reiniciar todos os processos prova que o resultado não depende de memória local.
	restartDistributedAPIs(t)
	waitForAPIs(t, client)

	assertWalletAndLedger(t, client, internalToken, wallet.ID)
	assertReconciliation(t, client, internalToken, wallet.ID)
	assertReplay(t, client, providerToken, requests)
}

// TestDistributedHTTPAndSQSShareIdempotency cruza os dois transportes e depois
// repete a mesma operação cinquenta vezes em paralelo sem duplicar o débito.
func TestDistributedHTTPAndSQSShareIdempotency(t *testing.T) {
	requireDistributedEnvironment(t)
	client := &http.Client{Timeout: 10 * time.Second}
	waitForAPIs(t, client)
	internalToken := clientCredentialsToken(t, client, "wallet-internal", "wallet-internal-local-secret")
	providerToken := clientCredentialsToken(t, client, "provider-a", "provider-a-local-secret")

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	playerID := "cross-transport-player-" + suffix
	wallet := openWallet(t, client, distributedEndpoints[0], internalToken, playerID)
	wager := newBetWithAmount("cross-transport-bet-"+suffix, wallet.ID, playerID, "25.00")

	queue := newSQSClient(t)
	start := make(chan struct{})
	var httpStatus int
	var httpErr, sqsErr error
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		httpStatus, httpErr = submitWagerRequest(client, distributedEndpoints[1], providerToken, wager)
	}()
	go func() {
		defer group.Done()
		<-start
		sqsErr = sendWagerMessage(context.Background(), queue, wager, "cross-message-"+suffix)
	}()
	close(start)
	group.Wait()
	if httpErr != nil || sqsErr != nil {
		t.Fatalf("cross transport errors = HTTP %v, SQS %v", httpErr, sqsErr)
	}
	if httpStatus != http.StatusCreated && httpStatus != http.StatusOK {
		t.Fatalf("HTTP race status = %d, want 201 or replay 200", httpStatus)
	}
	waitForProviderTransaction(t, client, providerToken, wager.ExternalTransactionID)

	statuses := submitRepeatedly(t, client, providerToken, wager, 50)
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("parallel replay %d status = %d, want 200", index, status)
		}
	}
	assertWalletBalanceAndLedgerCount(t, client, internalToken, wallet.ID, "75.00", 2)
}

// TestDistributedInvalidMessageReachesDLQ comprova o redrive real configurado
// no LocalStack depois de cinco recebimentos sem confirmação.
func TestDistributedInvalidMessageReachesDLQ(t *testing.T) {
	requireDistributedEnvironment(t)
	queue := newSQSClient(t)
	messageID := fmt.Sprintf("invalid-message-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"messageId":%q,"type":"UnsupportedEvent","occurredAt":"2026-09-27T00:00:00Z","data":{}}`, messageID)
	_, err := queue.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    aws.String("http://127.0.0.1:4566/000000000000/wager-transactions.fifo"),
		MessageBody: aws.String(body), MessageGroupId: aws.String("invalid-messages"),
		MessageDeduplicationId: aws.String(messageID),
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		output, receiveErr := queue.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String("http://127.0.0.1:4566/000000000000/wager-transactions-dlq.fifo"),
			MaxNumberOfMessages: 10, WaitTimeSeconds: 1, VisibilityTimeout: 1,
		})
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		for _, message := range output.Messages {
			if aws.ToString(message.Body) != body {
				continue
			}
			_, deleteErr := queue.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String("http://127.0.0.1:4566/000000000000/wager-transactions-dlq.fifo"),
				ReceiptHandle: message.ReceiptHandle,
			})
			if deleteErr != nil {
				t.Fatal(deleteErr)
			}
			return
		}
	}
	t.Fatalf("message %s did not reach the DLQ", messageID)
}

func requireDistributedEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("JUNGLE_DISTRIBUTED_TEST") != "1" {
		t.Skip("set JUNGLE_DISTRIBUTED_TEST=1 with the distributed Compose profile running")
	}
}

type walletResponse struct {
	ID      string `json:"id"`
	Balance money  `json:"balance"`
}

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type wagerRequest struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 money  `json:"money"`
}

func newBet(externalID, walletID, playerID string) wagerRequest {
	return newBetWithAmount(externalID, walletID, playerID, "80.00")
}

func newBetWithAmount(externalID, walletID, playerID, amount string) wagerRequest {
	return wagerRequest{ProviderID: "provider-a", ExternalTransactionID: externalID,
		PlayerID: playerID, WalletID: walletID, RoundID: "distributed-round",
		GameID: "distributed-game", Kind: "BET", Money: money{Amount: amount, Currency: "BRL"}}
}

func openWallet(t *testing.T, client *http.Client, endpoint, token, playerID string) walletResponse {
	t.Helper()
	body := map[string]any{"playerId": playerID, "initialBalance": money{Amount: "100.00", Currency: "BRL"}}
	response := doJSON(t, client, http.MethodPost, endpoint+"/wallets", token, "", body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("open wallet status = %d, body = %s", response.StatusCode, readBody(response))
	}
	var wallet walletResponse
	decodeResponse(t, response, &wallet)
	return wallet
}

func submitConcurrently(t *testing.T, client *http.Client, token string, requests []wagerRequest) []int {
	t.Helper()
	statuses := make([]int, len(requests))
	errorsByRequest := make([]error, len(requests))
	var group sync.WaitGroup
	for index := range requests {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			encoded, err := json.Marshal(requests[index])
			if err != nil {
				errorsByRequest[index] = err
				return
			}
			request, err := http.NewRequest(http.MethodPost, distributedEndpoints[index+1]+"/wagering/transactions", bytes.NewReader(encoded))
			if err != nil {
				errorsByRequest[index] = err
				return
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "provider-a:"+requests[index].ExternalTransactionID)
			response, err := client.Do(request)
			if err != nil {
				errorsByRequest[index] = err
				return
			}
			statuses[index] = response.StatusCode
			_ = response.Body.Close()
		}(index)
	}
	group.Wait()
	for index, err := range errorsByRequest {
		if err != nil {
			t.Fatalf("concurrent request %d: %v", index, err)
		}
	}
	return statuses
}

// newSQSClient aponta o SDK para o LocalStack real usado pelo ambiente Compose.
func newSQSClient(t *testing.T) *sqs.Client {
	t.Helper()
	configuration, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load SQS configuration: %v", err)
	}
	return sqs.NewFromConfig(configuration, func(options *sqs.Options) {
		options.BaseEndpoint = aws.String("http://127.0.0.1:4566")
	})
}

// submitWagerRequest executa uma chamada sem usar testing.T dentro da goroutine.
func submitWagerRequest(client *http.Client, endpoint, token string, wager wagerRequest) (int, error) {
	encoded, err := json.Marshal(wager)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/wagering/transactions", bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "provider-a:"+wager.ExternalTransactionID)
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, nil
}

// sendWagerMessage publica o mesmo contrato financeiro pela fila FIFO.
func sendWagerMessage(ctx context.Context, client *sqs.Client, wager wagerRequest, messageID string) error {
	envelope := struct {
		MessageID  string       `json:"messageId"`
		Type       string       `json:"type"`
		OccurredAt string       `json:"occurredAt"`
		Data       wagerMessage `json:"data"`
	}{
		MessageID: messageID, Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Data: wagerMessage{wagerRequest: wager, IdempotencyKey: "provider-a:" + wager.ExternalTransactionID},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String("http://127.0.0.1:4566/000000000000/wager-transactions.fifo"),
		MessageBody: aws.String(string(body)), MessageGroupId: aws.String(wager.WalletID),
		MessageDeduplicationId: aws.String(messageID),
	})
	return err
}

// wagerMessage adiciona a chave de idempotência ao contrato comum da operação.
type wagerMessage struct {
	wagerRequest
	IdempotencyKey string `json:"idempotencyKey"`
}

// waitForProviderTransaction aguarda até o consumidor SQS persistir um estado terminal.
func waitForProviderTransaction(t *testing.T, client *http.Client, token, externalID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response := doJSON(t, client, http.MethodGet,
			distributedEndpoints[2]+"/providers/provider-a/wagering/transactions/"+url.PathEscape(externalID), token, "", nil)
		if response.StatusCode == http.StatusOK {
			var transaction struct {
				Status string `json:"status"`
			}
			decodeResponse(t, response, &transaction)
			_ = response.Body.Close()
			if transaction.Status == "PROCESSED" || transaction.Status == "REJECTED" || transaction.Status == "FAILED" {
				return
			}
		} else {
			_ = response.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("transaction %s did not reach a terminal state", externalID)
}

// submitRepeatedly distribui os reenvios pelas três instâncias simultaneamente.
func submitRepeatedly(t *testing.T, client *http.Client, token string, wager wagerRequest, count int) []int {
	t.Helper()
	statuses := make([]int, count)
	errorsByRequest := make([]error, count)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			statuses[index], errorsByRequest[index] = submitWagerRequest(client,
				distributedEndpoints[index%len(distributedEndpoints)], token, wager)
		}(index)
	}
	close(start)
	group.Wait()
	for index, err := range errorsByRequest {
		if err != nil {
			t.Fatalf("parallel replay %d: %v", index, err)
		}
	}
	return statuses
}

// assertWalletBalanceAndLedgerCount verifica o efeito financeiro final em outra instância.
func assertWalletBalanceAndLedgerCount(t *testing.T, client *http.Client, token, walletID, expectedBalance string, expectedLedgerCount int) {
	t.Helper()
	response := doJSON(t, client, http.MethodGet, distributedEndpoints[0]+"/wallets/"+walletID, token, "", nil)
	defer response.Body.Close()
	var wallet walletResponse
	decodeResponse(t, response, &wallet)
	if response.StatusCode != http.StatusOK || wallet.Balance.Amount != expectedBalance {
		t.Fatalf("wallet status/balance = %d/%s, want 200/%s", response.StatusCode, wallet.Balance.Amount, expectedBalance)
	}

	ledgerResponse := doJSON(t, client, http.MethodGet, distributedEndpoints[1]+"/wallets/"+walletID+"/ledger?limit=50", token, "", nil)
	defer ledgerResponse.Body.Close()
	var ledger struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeResponse(t, ledgerResponse, &ledger)
	if ledgerResponse.StatusCode != http.StatusOK || len(ledger.Items) != expectedLedgerCount {
		t.Fatalf("ledger status/entries = %d/%d, want 200/%d", ledgerResponse.StatusCode, len(ledger.Items), expectedLedgerCount)
	}
}

func assertWalletAndLedger(t *testing.T, client *http.Client, token, walletID string) {
	t.Helper()
	response := doJSON(t, client, http.MethodGet, distributedEndpoints[0]+"/wallets/"+walletID, token, "", nil)
	defer response.Body.Close()
	var wallet walletResponse
	decodeResponse(t, response, &wallet)
	if response.StatusCode != http.StatusOK || wallet.Balance.Amount != "20.00" {
		t.Fatalf("wallet status/balance = %d/%s, want 200/20.00", response.StatusCode, wallet.Balance.Amount)
	}

	ledgerResponse := doJSON(t, client, http.MethodGet, distributedEndpoints[1]+"/wallets/"+walletID+"/ledger?limit=50", token, "", nil)
	defer ledgerResponse.Body.Close()
	var ledger struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeResponse(t, ledgerResponse, &ledger)
	if ledgerResponse.StatusCode != http.StatusOK || len(ledger.Items) != 2 {
		t.Fatalf("ledger status/entries = %d/%d, want 200/2", ledgerResponse.StatusCode, len(ledger.Items))
	}
}

func assertReconciliation(t *testing.T, client *http.Client, token, walletID string) {
	t.Helper()
	response := doJSON(t, client, http.MethodPost, distributedEndpoints[2]+"/wallets/"+walletID+"/reconciliation", token, "", map[string]any{})
	defer response.Body.Close()
	var result struct {
		Consistent bool  `json:"consistent"`
		Difference money `json:"difference"`
	}
	decodeResponse(t, response, &result)
	if response.StatusCode != http.StatusOK || !result.Consistent || result.Difference.Amount != "0.00" {
		t.Fatalf("reconciliation = status %d, consistent %v, difference %s", response.StatusCode, result.Consistent, result.Difference.Amount)
	}
}

func assertReplay(t *testing.T, client *http.Client, token string, requests []wagerRequest) {
	t.Helper()
	for index, wager := range requests {
		response := doJSON(t, client, http.MethodPost, distributedEndpoints[index]+"/wagering/transactions",
			token, "provider-a:"+wager.ExternalTransactionID, wager)
		defer response.Body.Close()
		var result struct {
			IdempotentReplay bool `json:"idempotentReplay"`
		}
		decodeResponse(t, response, &result)
		if response.StatusCode != http.StatusOK || !result.IdempotentReplay {
			t.Fatalf("replay %d = status %d, idempotent %v", index, response.StatusCode, result.IdempotentReplay)
		}
	}
}

func clientCredentialsToken(t *testing.T, client *http.Client, clientID, secret string) string {
	t.Helper()
	values := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	request, err := http.NewRequest(http.MethodPost,
		"http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
	}
	decodeResponse(t, response, &token)
	if response.StatusCode != http.StatusOK || token.AccessToken == "" {
		t.Fatalf("token for %s = status %d", clientID, response.StatusCode)
	}
	return token.AccessToken
}

func waitForAPIs(t *testing.T, client *http.Client) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for _, endpoint := range distributedEndpoints {
		for {
			response, err := client.Get(endpoint + "/health/ready")
			if err == nil && response.StatusCode == http.StatusOK {
				_ = response.Body.Close()
				break
			}
			if response != nil {
				_ = response.Body.Close()
			}
			if time.Now().After(deadline) {
				t.Fatalf("API %s did not become ready: %v", endpoint, err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func restartDistributedAPIs(t *testing.T) {
	t.Helper()
	root := repositoryRoot(t)
	command := exec.Command("docker", "compose", "--profile", "distributed", "restart", "api-1", "api-2", "api-3")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restart distributed APIs: %v\n%s", err, output)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}

func doJSON(t *testing.T, client *http.Client, method, endpoint, token, idempotencyKey string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode status %d response: %v", response.StatusCode, err)
	}
}

func readBody(response *http.Response) string {
	value, _ := io.ReadAll(response.Body)
	return string(value)
}
