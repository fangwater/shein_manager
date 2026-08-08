package shein

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildSignatureMatchesVerifiedAlgorithm(t *testing.T) {
	got := BuildSignature(
		"open-key",
		"secret-key",
		OrderListPath,
		"1752570849017",
		"abcde",
	)
	want := "abcdeY2NjMTMyYmMwM2NmZjVlMjQ0NGYwMmQyMjE2OTEzMmQwNTBhNWFmNjkwNTJjZDMwOTYyMWEzMjgwZjdkOTNlYg=="
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestClientPostSignsPathAndReturnsJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != OrderListPath {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("x-lt-openKeyId") != "open-key" {
			t.Errorf("open key header missing")
		}
		if request.Header.Get("x-lt-timestamp") != "1752570849017" {
			t.Errorf("timestamp = %q", request.Header.Get("x-lt-timestamp"))
		}
		wantSignature := BuildSignature("open-key", "secret-key", OrderListPath, "1752570849017", "abcde")
		if request.Header.Get("x-lt-signature") != wantSignature {
			t.Errorf("signature = %q", request.Header.Get("x-lt-signature"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["page"] != float64(1) {
			t.Errorf("body page = %#v", body["page"])
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":"0","msg":"OK","info":{"list":[]}}`))
	}))
	defer server.Close()

	client := NewClient(Credentials{OpenKeyID: "open-key", SecretKey: "secret-key", BaseURL: server.URL}, time.Second)
	client.now = func() time.Time { return time.UnixMilli(1752570849017) }
	client.randomKey = func() (string, error) { return "abcde", nil }
	result, err := client.Request(context.Background(), http.MethodPost, OrderListPath, map[string]any{"page": 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result["code"] != "0" {
		t.Fatalf("result = %#v", result)
	}
}

func TestLogisticsTrackUsesQueryWithoutChangingSignedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("orderNo") != "ORDER-1" || request.URL.Query().Get("packageNo") != "PKG-1" {
			t.Errorf("query = %s", request.URL.RawQuery)
		}
		want := BuildSignature("open-key", "secret-key", LogisticsTrackPath, "1752570849017", "abcde")
		if request.Header.Get("x-lt-signature") != want {
			t.Errorf("signature includes unexpected query data")
		}
		_, _ = writer.Write([]byte(`{"code":"0","msg":"OK","info":{}}`))
	}))
	defer server.Close()
	client := NewClient(Credentials{OpenKeyID: "open-key", SecretKey: "secret-key", BaseURL: server.URL}, time.Second)
	client.now = func() time.Time { return time.UnixMilli(1752570849017) }
	client.randomKey = func() (string, error) { return "abcde", nil }
	if _, err := client.LogisticsTrack(context.Background(), "ORDER-1", "PKG-1", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"code":"400100","msg":"invalid request","traceId":"trace-1"}`))
	}))
	defer server.Close()
	client := NewClient(Credentials{OpenKeyID: "open-key", SecretKey: "secret-key", BaseURL: server.URL}, time.Second)
	_, err := client.Request(context.Background(), http.MethodPost, OrderDetailPath, map[string]any{}, nil)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != "400100" || apiErr.TraceID != "trace-1" {
		t.Fatalf("error = %#v", err)
	}
}
