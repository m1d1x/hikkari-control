package porkbun

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestAccountBalanceAndSpendLimit(t *testing.T) {
	c := server(t, func(path string, body map[string]any, _ http.Header) (int, string) {
		if body["apikey"] != "k" {
			t.Errorf("%s: credentials not in body", path)
		}
		switch path {
		case "account/balance":
			return 200, `{"status":"SUCCESS","balance":1234,"display":"$12.34"}`
		case "account/apiSettings":
			return 200, `{"status":"SUCCESS","monthlySpend":154,"spendLimit":{"limit":10000,"source":"default","spent":154,"remaining":9846}}`
		}
		t.Errorf("unexpected path %q", path)
		return 404, `{}`
	})
	bal, err := c.Balance(context.Background())
	if err != nil || bal != 1234 {
		t.Fatalf("Balance = %d, %v; want 1234", bal, err)
	}
	lim, err := c.SpendLimit(context.Background())
	if err != nil {
		t.Fatalf("SpendLimit: %v", err)
	}
	if !lim.Limited || lim.LimitCents != 10000 || lim.SpentCents != 154 {
		t.Errorf("SpendLimit = %+v", lim)
	}
}

func TestSpendLimitNoneIsUnlimited(t *testing.T) {
	c := server(t, func(string, map[string]any, http.Header) (int, string) {
		return 200, `{"status":"SUCCESS","monthlySpend":0,"spendLimit":{"limit":0,"source":"none","spent":0}}`
	})
	lim, err := c.SpendLimit(context.Background())
	if err != nil || lim.Limited {
		t.Fatalf("SpendLimit = %+v, %v; want unlimited", lim, err)
	}
}

func TestDomainsListsTheAccount(t *testing.T) {
	c := server(t, func(path string, body map[string]any, _ http.Header) (int, string) {
		if path != "domain/listAll" {
			t.Errorf("path = %q", path)
		}
		if body["start"] != float64(0) {
			t.Errorf("start = %v", body["start"])
		}
		return 200, `{"status":"SUCCESS","count":2,"domains":[
			{"domain":"Cedar1.LOL","tld":"lol","status":"ACTIVE","expireDate":"2027-10-21 12:00:00","apiAccess":1},
			{"domain":"brand.com","tld":"com","status":"ACTIVE","expireDate":"2027-01-01 00:00:00","apiAccess":0}]}`
	})
	ds, err := c.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Domain != "cedar1.lol" || !ds[0].APIAccess || ds[1].APIAccess || ds[0].ExpireDate == "" {
		t.Fatalf("domains = %+v", ds)
	}
}

func TestCanSetARecordRehearses(t *testing.T) {
	var dry any
	c := server(t, func(path string, body map[string]any, _ http.Header) (int, string) {
		switch path {
		case "dns/create/ok1.lol":
			dry = body["dryRun"]
			return 200, `{"status":"SUCCESS","dryRun":true,"wouldSucceed":true}`
		case "dns/create/nope1.lol":
			return 200, `{"status":"SUCCESS","dryRun":true,"wouldSucceed":false,"message":"API access is not enabled for this domain"}`
		}
		return 404, `{}`
	})
	if err := c.CanSetARecord(context.Background(), "ok1.lol"); err != nil {
		t.Fatalf("ok: %v", err)
	}
	if dry != true {
		t.Fatalf("dryRun = %v, want true: a check must never write", dry)
	}
	var apiErr *APIError
	if err := c.CanSetARecord(context.Background(), "nope1.lol"); !errors.As(err, &apiErr) || apiErr.Message == "" {
		t.Fatalf("refused: err = %v, want the registrar's message", err)
	}
}

func TestBalanceBadKey(t *testing.T) {
	c := server(t, func(string, map[string]any, http.Header) (int, string) {
		return 400, `{"status":"ERROR","message":"Invalid API key. (001)","code":"INVALID_API_KEYS_001"}`
	})
	if _, err := c.Balance(context.Background()); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}
