package shein

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultAPIBaseURL = "https://openapi.sheincorp.com"

type Credentials struct {
	ShopKey   string `json:"shop_key"`
	OpenKeyID string `json:"-"`
	SecretKey string `json:"-"`
	BaseURL   string `json:"api_base_url"`
}

type APIError struct {
	Status  int
	Code    string
	Message string
	TraceID string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("SHEIN API error code=%s msg=%s", e.Code, e.Message)
	}
	return e.Message
}

type Client struct {
	baseURL   string
	openKeyID string
	secretKey string
	http      *http.Client
	now       func() time.Time
	randomKey func() (string, error)
}

func NewClient(credentials Credentials, timeout time.Duration) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(credentials.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	return &Client{
		baseURL:   baseURL,
		openKeyID: credentials.OpenKeyID,
		secretKey: credentials.SecretKey,
		http:      &http.Client{Timeout: timeout},
		now:       time.Now,
		randomKey: newRandomKey,
	}
}

func BuildSignature(keyID, secretKey, path, timestampMS, randomKey string) string {
	message := keyID + "&" + timestampMS + "&" + path
	mac := hmac.New(sha256.New, []byte(secretKey+randomKey))
	_, _ = mac.Write([]byte(message))
	digestHex := hex.EncodeToString(mac.Sum(nil))
	return randomKey + base64.StdEncoding.EncodeToString([]byte(digestHex))
}

func newRandomKey() (string, error) {
	buffer := make([]byte, 3)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate SHEIN signature random key: %w", err)
	}
	return hex.EncodeToString(buffer)[:5], nil
}

func (c *Client) Request(ctx context.Context, method, path string, body map[string]any, query url.Values) (map[string]any, error) {
	if !strings.HasPrefix(path, "/open-api/") {
		return nil, errors.New("SHEIN API path must start with /open-api/")
	}
	if c.openKeyID == "" || c.secretKey == "" {
		return nil, errors.New("SHEIN store credentials are incomplete")
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPost {
		return nil, errors.New("SHEIN API method must be GET or POST")
	}

	var reader io.Reader
	if method == http.MethodPost {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode SHEIN request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("create SHEIN request: %w", err)
	}

	timestamp := strconv.FormatInt(c.now().UnixMilli(), 10)
	randomKey, err := c.randomKey()
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	request.Header.Set("x-lt-openKeyId", c.openKeyID)
	request.Header.Set("x-lt-timestamp", timestamp)
	request.Header.Set("x-lt-signature", BuildSignature(c.openKeyID, c.secretKey, path, timestamp, randomKey))

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("SHEIN request failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read SHEIN response: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &APIError{Status: response.StatusCode, Message: "SHEIN returned invalid JSON"}
	}
	code := strings.TrimSpace(fmt.Sprint(parsed["code"]))
	traceID := strings.TrimSpace(fmt.Sprint(parsed["traceId"]))
	if traceID == "<nil>" {
		traceID = ""
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || code != "0" {
		return nil, &APIError{
			Status: response.StatusCode, Code: code,
			Message: strings.TrimSpace(fmt.Sprint(parsed["msg"])), TraceID: traceID,
		}
	}
	return parsed, nil
}
