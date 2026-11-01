package porkbun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// server builds a Client pointed at an httptest server whose handler is h. The
// handler gets the request path (without the base) and the decoded body.
func server(t *testing.T, h func(path string, body map[string]any, hdr http.Header) (int, string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		path := strings.TrimPrefix(r.URL.Path, "/")
		code, resp := h(path, body, r.Header)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return New("k", "s", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestPingSendsCredentials(t *testing.T) {
	var gotKey, gotSecret string
	c := server(t, func(path string, body map[string]any, _ http.Header) (int, string) {
		if path != "ping" {
			t.Errorf("path = %q, want ping", path)
		}
		gotKey, _ = body["apikey"].(string)
		gotSecret, _ = body["secretapikey"].(string)
		return 200, `{"status":"SUCCESS","yourIp":"203.0.113.7"}`
	})
	ip, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip = %q", ip)
	}
	if gotKey != "k" || gotSecret != "s" {
		t.Errorf("credentials in body = %q/%q", gotKey, gotSecret)
	}
}

func TestCheckDomainParsesPrice(t *testing.T) {
	c := server(t, func(path string, _ map[string]any, _ http.Header) (int, string) {
		if path != "domain/checkDomain/cedar4821.lol" {
			t.Errorf("path = %q", path)
		}
		return 200, `{"status":"SUCCESS","response":{"avail":"yes","price":"1.54"}}`
	})
	res, err := c.CheckDomain(context.Background(), "cedar4821.lol")
	if err != nil {
		t.Fatalf("CheckDomain: %v", err)
	}
	if !res.Available || res.CostCents != 154 {
		t.Errorf("got avail=%v cost=%d, want yes/154", res.Available, res.CostCents)
	}
}

func TestRegisterableBoolish(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"status":"SUCCESS","apiRegisterable":true}`, true},
		{`{"status":"SUCCESS","apiRegisterable":"yes"}`, true},
		{`{"status":"SUCCESS","apiRegisterable":false}`, false},
		{`{"status":"SUCCESS","apiRegisterable":"no"}`, false},
	} {
		c := server(t, func(_ string, _ map[string]any, _ http.Header) (int, string) {
			return 200, tc.body
		})
		got, err := c.Registerable(context.Background(), ".lol")
		if err != nil {
			t.Fatalf("Registerable(%s): %v", tc.body, err)
		}
		if got != tc.want {
			t.Errorf("Registerable(%s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestCreateDryRunReadsPreflight(t *testing.T) {
	c := server(t, func(path string, body map[string]any, _ http.Header) (int, string) {
		if path != "domain/create/cedar4821.lol" {
			t.Errorf("path = %q", path)
		}
		if body["dryRun"] != true {
			t.Errorf("dryRun not set: %v", body["dryRun"])
		}
		if body["cost"].(float64) != 154 {
			t.Errorf("cost = %v, want 154", body["cost"])
		}
		if body["agreeToTerms"] != "yes" {
			t.Errorf("agreeToTerms = %v", body["agreeToTerms"])
		}
		return 200, `{"status":"SUCCESS","wouldSucceed":true,"sufficientFunds":true,"withinMonthlySpendLimit":true,"balance":"8.46"}`
	})
	res, err := c.Create(context.Background(), CreateParams{Domain: "cedar4821.lol", CostCents: 154, DryRun: true})
	if err != nil {
		t.Fatalf("Create dryRun: %v", err)
	}
	if !res.WouldSucceed || !res.SufficientFunds || !res.WithinMonthlyLimit {
		t.Errorf("preflight = %+v", res)
	}
	if res.BalanceCents != 846 {
		t.Errorf("balance = %d, want 846", res.BalanceCents)
	}
}

func TestCreateRealCarriesIdempotencyKey(t *testing.T) {
	var gotKey string
	c := server(t, func(_ string, body map[string]any, hdr http.Header) (int, string) {
		gotKey = hdr.Get("Idempotency-Key")
		if _, ok := body["dryRun"]; ok {
			t.Errorf("real create must omit dryRun, got %v", body["dryRun"])
		}
		return 200, `{"status":"SUCCESS","orderId":12216680}`
	})
	res, err := c.Create(context.Background(), CreateParams{Domain: "cedar4821.lol", CostCents: 154, IdempotencyKey: "nekko-buy-1"})
	if err != nil {
		t.Fatalf("Create real: %v", err)
	}
	if res.OrderID != "12216680" {
		t.Errorf("orderId = %q, want 12216680", res.OrderID)
	}
	if gotKey != "nekko-buy-1" {
		t.Errorf("Idempotency-Key = %q", gotKey)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want error
	}{
		{"Domain is not available", ErrUnavailable},
		{"Insufficient funds in your account", ErrInsufficientFunds},
		{"ORDER_TOO_LARGE: exceeds monthly spend limit", ErrOrderTooLarge},
		{"Account verification required", ErrVerificationRequired},
		{"Invalid API key", ErrBadCredentials},
	} {
		c := server(t, func(_ string, _ map[string]any, _ http.Header) (int, string) {
			b, _ := json.Marshal(map[string]string{"status": "ERROR", "message": tc.msg})
			return 400, string(b)
		})
		_, err := c.CheckDomain(context.Background(), "x.lol")
		if !errors.Is(err, tc.want) {
			t.Errorf("message %q classified as %v, want %v", tc.msg, err, tc.want)
		}
	}
}

func TestUnknownErrorIsAPIError(t *testing.T) {
	c := server(t, func(_ string, _ map[string]any, _ http.Header) (int, string) {
		return 500, `{"status":"ERROR","message":"something odd"}`
	})
	_, err := c.CheckDomain(context.Background(), "x.lol")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.HTTPStatus != 500 || apiErr.Message != "something odd" {
		t.Errorf("APIError = %+v", apiErr)
	}
}

func TestRateLimited(t *testing.T) {
	c := server(t, func(_ string, _ map[string]any, _ http.Header) (int, string) {
		return 429, `{"status":"ERROR","message":"rate limit"}`
	})
	_, err := c.CheckDomain(context.Background(), "x.lol")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestDollarsToCents(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"1.54", 154, true},
		{"11", 1100, true},
		{"2.0", 200, true},
		{"0.99", 99, true},
		{"8.46", 846, true},
		{"1.", 100, true},
		{"", 0, false},
		{"-1.00", 0, false},
		{"abc", 0, false},
	} {
		got, err := dollarsToCents(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("dollarsToCents(%q) err=%v, ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("dollarsToCents(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// domain/create's balance comes as cents (a number, like account/balance) or
// as dollars (a string); both read as cents.
func TestTheCreateBalanceReadsEitherWay(t *testing.T) {
	for raw, want := range map[string]int{`2846`: 2846, `"28.46"`: 2846, `"$28.46"`: 2846, ``: 100, `null`: 100} {
		if got := createBalanceCents(json.RawMessage(raw), "$1.00"); got != want {
			t.Errorf("balance %s = %d cents, want %d", raw, got, want)
		}
	}
}
