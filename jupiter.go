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

	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
)

func (a *App) executeJupiterSwap(ctx context.Context, cfg Config, inputMint, outputMint, amount string) (string, string, error) {
	pk, err := solana.PrivateKeyFromBase58(a.env.PrivateKeyBase58)
	if err != nil {
		return "", "", err
	}
	taker := pk.PublicKey().String()

	orderURL := strings.TrimRight(cfg.JupiterBaseURL, "/") + "/order"
	q := url.Values{}
	q.Set("inputMint", inputMint)
	q.Set("outputMint", outputMint)
	q.Set("amount", amount)
	q.Set("taker", taker)
	q.Set("slippageBps", strconv.Itoa(cfg.SlippageBps))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orderURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("x-api-key", a.env.JupiterAPIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("jupiter order HTTP %d: %s", resp.StatusCode, string(body))
	}

	var order JupiterOrderResponse
	if err := json.Unmarshal(body, &order); err != nil {
		return "", "", err
	}
	if order.Error != "" {
		return "", "", errors.New(order.Error)
	}
	if order.Transaction == "" || order.RequestID == "" {
		return "", "", fmt.Errorf("invalid jupiter order: %s", string(body))
	}

	signedB64, err := signJupiterTransaction(order.Transaction, pk)
	if err != nil {
		return "", "", err
	}

	executeURL := strings.TrimRight(cfg.JupiterBaseURL, "/") + "/execute"
	execPayload := map[string]string{
		"signedTransaction": signedB64,
		"requestId":         order.RequestID,
	}
	rawPayload, _ := json.Marshal(execPayload)

	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, executeURL, bytes.NewReader(rawPayload))
	if err != nil {
		return "", "", err
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("x-api-key", a.env.JupiterAPIKey)

	resp2, err := a.client.Do(req2)
	if err != nil {
		return "", "", err
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode >= 300 {
		return "", "", fmt.Errorf("jupiter execute HTTP %d: %s", resp2.StatusCode, string(body2))
	}

	var ex JupiterExecuteResponse
	if err := json.Unmarshal(body2, &ex); err != nil {
		return "", "", err
	}
	if ex.Error != "" {
		return "", "", errors.New(ex.Error)
	}
	if ex.Signature == "" {
		return "", "", fmt.Errorf("empty signature: %s", string(body2))
	}

	return ex.Signature, order.OutAmount, nil
}

func signJupiterTransaction(txBase64 string, pk solana.PrivateKey) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(txBase64)
	if err != nil {
		return "", err
	}

	tx, err := solana.TransactionFromDecoder(bin.NewBinDecoder(raw))
	if err != nil {
		return "", err
	}

	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(pk.PublicKey()) {
			return &pk
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	signed, err := tx.MarshalBinary()
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(signed), nil
}
