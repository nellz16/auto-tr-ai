package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
)

func (a *App) executeJupiterSwap(
	ctx context.Context,
	cfg Config,
	inputMint string,
	outputMint string,
	amount string,
) (string, string, error) {
	privateKey, err := solana.PrivateKeyFromBase58(
		a.env.PrivateKeyBase58,
	)
	if err != nil {
		return "", "", err
	}

	taker := privateKey.PublicKey().String()

	orderEndpoint := strings.TrimRight(
		cfg.JupiterBaseURL,
		"/",
	) + "/order"

	query := url.Values{}
	query.Set("inputMint", inputMint)
	query.Set("outputMint", outputMint)
	query.Set("amount", amount)
	query.Set("taker", taker)
	query.Set(
		"slippageBps",
		strconv.Itoa(cfg.SlippageBps),
	)

	var order JupiterOrderResponse

	if err := a.doJupiterJSON(
		ctx,
		http.MethodGet,
		orderEndpoint+"?"+query.Encode(),
		nil,
		&order,
	); err != nil {
		return "", "", err
	}

	if order.Error != "" {
		return "", "", errors.New(order.Error)
	}

	if order.Transaction == "" ||
		order.RequestID == "" {

		return "", "", errors.New(
			"Jupiter order tidak memiliki transaction/requestId",
		)
	}

	signedTransaction, err := signJupiterTransaction(
		order.Transaction,
		privateKey,
	)
	if err != nil {
		return "", "", err
	}

	executePayload := map[string]string{
		"signedTransaction": signedTransaction,
		"requestId":         order.RequestID,
	}

	rawPayload, err := json.Marshal(executePayload)
	if err != nil {
		return "", "", err
	}

	executeEndpoint := strings.TrimRight(
		cfg.JupiterBaseURL,
		"/",
	) + "/execute"

	var execution JupiterExecuteResponse

	if err := a.doJupiterJSON(
		ctx,
		http.MethodPost,
		executeEndpoint,
		rawPayload,
		&execution,
	); err != nil {
		return "", "", err
	}

	if execution.Error != "" {
		return "", "", errors.New(execution.Error)
	}

	if execution.Signature == "" {
		return "", "", errors.New(
			"Jupiter execute tidak mengembalikan signature",
		)
	}

	return execution.Signature, order.OutAmount, nil
}

func (a *App) doJupiterJSON(
	ctx context.Context,
	method string,
	endpoint string,
	requestBody []byte,
	output any,
) error {
	var lastError error

	for attempt := 0; attempt < 2; attempt++ {
		if err := a.waitJupiter(ctx); err != nil {
			return err
		}

		var bodyReader *bytes.Reader

		if requestBody == nil {
			bodyReader = bytes.NewReader(nil)
		} else {
			bodyReader = bytes.NewReader(requestBody)
		}

		request, err := http.NewRequestWithContext(
			ctx,
			method,
			endpoint,
			bodyReader,
		)
		if err != nil {
			return err
		}

		request.Header.Set(
			"x-api-key",
			a.env.JupiterAPIKey,
		)

		if method != http.MethodGet {
			request.Header.Set(
				"Content-Type",
				"application/json",
			)
		}

		response, err := a.client.Do(request)
		if err != nil {
			lastError = err

			if attempt == 0 {
				if err := sleepContext(
					ctx,
					2*time.Second,
				); err != nil {
					return err
				}

				continue
			}

			return err
		}

		responseBody, readErr := io.ReadAll(
			response.Body,
		)
		response.Body.Close()

		if readErr != nil {
			return readErr
		}

		if response.StatusCode >= 200 &&
			response.StatusCode < 300 {

			if output == nil ||
				len(responseBody) == 0 {

				return nil
			}

			return json.Unmarshal(
				responseBody,
				output,
			)
		}

		apiError := newAPIError(
			"jupiter",
			response,
			responseBody,
		)

		lastError = apiError

		if attempt == 0 &&
			(response.StatusCode == http.StatusTooManyRequests ||
				response.StatusCode >= 500) {

			wait := apiError.RetryAfter

			if wait <= 0 {
				wait = 2 * time.Second
			}

			if err := sleepContext(
				ctx,
				wait,
			); err != nil {
				return err
			}

			continue
		}

		return apiError
	}

	return lastError
}

func signJupiterTransaction(
	transactionBase64 string,
	privateKey solana.PrivateKey,
) (string, error) {
	rawTransaction, err := base64.StdEncoding.DecodeString(
		transactionBase64,
	)
	if err != nil {
		return "", err
	}

	transaction, err := solana.TransactionFromDecoder(
		bin.NewBinDecoder(rawTransaction),
	)
	if err != nil {
		return "", err
	}

	_, err = transaction.Sign(
		func(publicKey solana.PublicKey) *solana.PrivateKey {
			if publicKey.Equals(privateKey.PublicKey()) {
				return &privateKey
			}

			return nil
		},
	)
	if err != nil {
		return "", err
	}

	signedBinary, err := transaction.MarshalBinary()
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(
		signedBinary,
	), nil
}
