package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/getmikan/marketplace/internal/adapter"
	"github.com/getmikan/marketplace/internal/catalog"
)

const (
	apiToken = "72|oBCB7Z3SmUm1gvkpEdRcSR2q1ERHpG4vD3DNBmuT"
	shopID   = "LXZv3R7Q8B"
	token    = "adapter-token-for-tests"
	billID   = "GkLWvKx3"
)

// fakeCardlink answers like Cardlink for one bill the test sets up.
type fakeCardlink struct {
	mu           sync.Mutex
	status       string
	active       bool
	amount       any
	currency     string
	refuse       string // an api:error key every call is refused with, as 403
	refuseReturn bool   // the shop is on another domain: bills with return URLs are invalid
	invalid      bool   // every bill is invalid
	paths        []string
	created      []url.Values
}

func (f *fakeCardlink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	answer := func(code int, v any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	if r.Header.Get("Authorization") != "Bearer "+apiToken {
		answer(http.StatusUnauthorized, map[string]any{"message": "Unauthenticated."})
		return
	}
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	if f.refuse != "" {
		answer(http.StatusForbidden, map[string]any{"success": false, "message": "api:error." + f.refuse})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/merchant/balance":
		answer(http.StatusOK, map[string]any{"success": "true", "balances": []any{map[string]any{"currency": "RUB",
			"balance_available": "93988.37000000", "balance_locked": "0.00000000", "balance_hold": "0.00000000"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/bill/create":
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			answer(http.StatusUnprocessableEntity, map[string]any{"message": "not a form"})
			return
		}
		_ = r.ParseForm()
		f.created = append(f.created, r.PostForm)
		switch {
		case r.PostForm.Get("shop_id") != shopID:
			answer(http.StatusForbidden, map[string]any{"success": false, "message": "api:error.shop_not_found"})
		case f.invalid || f.refuseReturn && r.PostForm.Has("success_url"):
			answer(http.StatusUnprocessableEntity, map[string]any{"message": "The success url must match the shop's domain.",
				"errors": map[string]any{"success_url": []string{"The success url must match the shop's domain."}}})
		default:
			answer(http.StatusOK, map[string]any{"success": "true", "link_url": "https://cardlink.link/link/" + billID,
				"link_page_url": "https://cardlink.link/transfer/" + billID, "bill_id": billID})
		}
	case r.Method == http.MethodGet && r.URL.Path == "/bill/status":
		if r.URL.Query().Get("id") != billID {
			answer(http.StatusForbidden, map[string]any{"success": false, "message": "api:error.bill_not_found"})
			return
		}
		answer(http.StatusOK, map[string]any{"id": billID, "order_id": "mikan-4217", "status": f.status, "active": f.active,
			"type": "NORMAL", "amount": f.amount, "currency_in": f.currency, "created_at": "2026-10-14 10:00:00", "success": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type env struct {
	t    *testing.T
	fake *fakeCardlink
	h    http.Handler
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, fake: &fakeCardlink{status: "NEW", active: true, amount: json.Number("199"), currency: "RUB"}, logs: &bytes.Buffer{}}
	srv := httptest.NewServer(e.fake)
	t.Cleanup(srv.Close)
	m, err := catalog.ParseManifest(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(e.logs, nil))
	e.h, err = adapter.NewHandler(&cardlink{api: srv.URL, hc: srv.Client(), manifest: m, log: log}, token, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if strings.Contains(e.logs.String(), apiToken) {
			t.Error("the API token is in the log")
		}
	})
	return e
}

func (e *env) call(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("%s %s: not JSON: %q", method, path, w.Body.String())
	}
	return w.Code, out
}

func settings(extra ...any) map[string]any {
	s := map[string]any{"api_token": apiToken, "shop_id": shopID}
	for i := 0; i+1 < len(extra); i += 2 {
		s[extra[i].(string)] = extra[i+1]
	}
	return s
}

func wantError(t *testing.T, code int, out map[string]any, wantCode int, wantErr string) {
	t.Helper()
	if code != wantCode || out["code"] != wantErr {
		t.Fatalf("got %d %v, want %d %s", code, out, wantCode, wantErr)
	}
}

func invoiceBody(s map[string]any) map[string]any {
	return map[string]any{"settings": s, "payment_id": 4217, "idempotency_key": "mikan-4217", "amount": 19900, "currency": "RUB",
		"description": "VPN · Месяц", "return_url": "https://panel.example.com/tg"}
}

func TestInfo(t *testing.T) {
	e := newEnv(t)
	code, out := e.call(http.MethodGet, "/v1/info", nil)
	if code != http.StatusOK || out["id"] != "cardlink" || out["protocol"] != 1.0 {
		t.Fatalf("info: %d %v", code, out)
	}
	if got, _ := json.Marshal(out["capabilities"]); string(got) != `["webhook"]` {
		t.Fatalf("capabilities: %s", got)
	}
	if got, _ := json.Marshal(out["currencies"]); string(got) != `["RUB","USD","EUR"]` {
		t.Fatalf("currencies: %s", got)
	}
	secrets := map[string]bool{}
	for _, f := range out["settings"].([]any) {
		m := f.(map[string]any)
		secrets[m["key"].(string)] = m["secret"].(bool)
	}
	if len(secrets) != 4 || !secrets["api_token"] || secrets["shop_id"] || secrets["method"] || secrets["payer_pays_commission"] {
		t.Fatalf("secret flags: %v", secrets)
	}
}

func TestCheck(t *testing.T) {
	e := newEnv(t)
	if code, out := e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings()}); code != http.StatusOK || len(out) != 0 {
		t.Fatalf("good token: %d %v", code, out)
	}
	if e.fake.paths[0] != "GET /merchant/balance" {
		t.Fatalf("check asked %v", e.fake.paths)
	}
	code, out := e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings("api_token", "wrong")})
	wantError(t, code, out, http.StatusUnprocessableEntity, adapter.CodeBadCredentials)
	code, out = e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings("method", "CRYPTO")})
	wantError(t, code, out, http.StatusUnprocessableEntity, adapter.CodeBadSettings)
	code, out = e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings("shop_id", "../shop")})
	wantError(t, code, out, http.StatusUnprocessableEntity, adapter.CodeBadSettings)
	code, out = e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings("payer_pays_commission", "yes")})
	wantError(t, code, out, http.StatusUnprocessableEntity, adapter.CodeBadSettings)

	// Cardlink's 403 carries its own reason: the admin sees which one.
	e.fake.refuse = "ip_access_denied"
	code, out = e.call(http.MethodPost, "/v1/check", map[string]any{"settings": settings()})
	wantError(t, code, out, http.StatusUnprocessableEntity, "cardlink_ip_access_denied")
}

func TestCreateInvoice(t *testing.T) {
	e := newEnv(t)
	code, out := e.call(http.MethodPost, "/v1/invoices", invoiceBody(settings()))
	if code != http.StatusOK || out["external_id"] != billID || out["pay_url"] != "https://cardlink.link/transfer/"+billID {
		t.Fatalf("invoice: %d %v", code, out)
	}
	in := e.fake.created[0]
	want := url.Values{"amount": {"199.00"}, "currency_in": {"RUB"}, "shop_id": {shopID}, "order_id": {"mikan-4217"}, "type": {"normal"},
		"ttl": {"86400"}, "description": {"VPN · Месяц"}, "success_url": {"https://panel.example.com/tg"}, "fail_url": {"https://panel.example.com/tg"},
		"return_url": {"https://panel.example.com/tg"}}
	if e.fake.paths[0] != "POST /bill/create" || in.Encode() != want.Encode() {
		t.Fatalf("request: %v\n got %s\nwant %s", e.fake.paths, in.Encode(), want.Encode())
	}

	// The method and the fee go along when they are set.
	code, out = e.call(http.MethodPost, "/v1/invoices", invoiceBody(settings("method", "SBP", "payer_pays_commission", true)))
	if code != http.StatusOK {
		t.Fatalf("with method: %d %v", code, out)
	}
	if in = e.fake.created[1]; in.Get("payment_method") != "SBP" || in.Get("payer_pays_commission") != "1" {
		t.Fatalf("method request: %v", in)
	}

	// Without return_url the bill has no return URLs.
	b := invoiceBody(settings())
	b["return_url"] = ""
	if code, out = e.call(http.MethodPost, "/v1/invoices", b); code != http.StatusOK {
		t.Fatalf("without return_url: %d %v", code, out)
	}
	if in = e.fake.created[2]; in.Has("success_url") || in.Has("fail_url") || in.Has("return_url") {
		t.Fatalf("return URLs sent although there are none: %v", in)
	}

	// USD and EUR are Cardlink's too; other currencies are not.
	b = invoiceBody(settings())
	b["currency"] = "USD"
	if code, out = e.call(http.MethodPost, "/v1/invoices", b); code != http.StatusOK || e.fake.created[3].Get("currency_in") != "USD" {
		t.Fatalf("USD: %d %v", code, out)
	}
	b["currency"] = "GBP"
	code, out = e.call(http.MethodPost, "/v1/invoices", b)
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)

	// A wrong shop is named as such.
	code, out = e.call(http.MethodPost, "/v1/invoices", invoiceBody(settings("shop_id", "Other123")))
	wantError(t, code, out, http.StatusUnprocessableEntity, "cardlink_shop_not_found")
}

func TestCreateInvoiceShopOnAnotherDomain(t *testing.T) {
	e := newEnv(t)
	e.fake.refuseReturn = true
	code, out := e.call(http.MethodPost, "/v1/invoices", invoiceBody(settings()))
	if code != http.StatusOK || out["external_id"] != billID {
		t.Fatalf("invoice: %d %v", code, out)
	}
	if len(e.fake.created) != 2 || !e.fake.created[0].Has("success_url") || e.fake.created[1].Has("success_url") ||
		e.fake.created[1].Has("fail_url") || e.fake.created[1].Has("return_url") || e.fake.created[1].Get("order_id") != "mikan-4217" {
		t.Fatalf("the second bill must go without the return URLs: %v", e.fake.created)
	}
	if !strings.Contains(e.logs.String(), "trying without them") || !strings.Contains(e.logs.String(), "payment_id=4217") {
		t.Fatalf("the first attempt is not logged: %s", e.logs)
	}

	// Invalid for another reason: the second answer is the one returned, and a bill
	// without return URLs is not tried twice.
	e.fake.invalid = true
	code, out = e.call(http.MethodPost, "/v1/invoices", invoiceBody(settings()))
	wantError(t, code, out, http.StatusUnprocessableEntity, "cardlink_http_422")
	if len(e.fake.created) != 4 || !strings.Contains(out["message"].(string), "without them") ||
		!strings.Contains(e.logs.String(), "without the return URLs as well") {
		t.Fatalf("both attempts must be told apart: %d calls, %v, %s", len(e.fake.created), out, e.logs)
	}
	b := invoiceBody(settings())
	b["return_url"] = ""
	code, out = e.call(http.MethodPost, "/v1/invoices", b)
	wantError(t, code, out, http.StatusUnprocessableEntity, "cardlink_http_422")
	if len(e.fake.created) != 5 {
		t.Fatalf("calls: %d, want 5", len(e.fake.created))
	}
}

func (e *env) status(id string) (int, map[string]any) {
	return e.call(http.MethodPost, "/v1/status", map[string]any{"settings": settings(), "external_id": id})
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		status string
		active bool
		want   string
	}{
		{"NEW", true, "pending"},
		{"PROCESS", true, "pending"},
		{"SUCCESS", true, "paid"},
		{"SUCCESS", false, "paid"},
		{"OVERPAID", false, "paid"},
		// Money came, but less than the bill: the admin settles it in Cardlink.
		{"UNDERPAID", true, "pending"},
		// A failed attempt leaves the bill open for another card.
		{"FAIL", true, "pending"},
		{"FAIL", false, "canceled"},
		// Its time ran out or the merchant switched it off.
		{"NEW", false, "canceled"},
	} {
		e.fake.status, e.fake.active = c.status, c.active
		code, out := e.status(billID)
		if code != http.StatusOK || out["status"] != c.want || out["amount"] != 19900.0 || out["currency"] != "RUB" {
			t.Fatalf("%s active=%v: %d %v, want %s", c.status, c.active, code, out, c.want)
		}
	}
	for amount, want := range map[any]float64{json.Number("199.5"): 19950, json.Number("100.05"): 10005, "199.00000000": 19900, "35400": 3540000} {
		e.fake.amount = amount
		if _, out := e.status(billID); out["amount"] != want {
			t.Fatalf("amount %v: %v, want %v", amount, out, want)
		}
	}
	// Past the cents only zeros may follow.
	e.fake.amount = json.Number("177.451")
	code, out := e.status(billID)
	wantError(t, code, out, http.StatusBadGateway, adapter.CodeProviderUnavailable)
	e.fake.amount = json.Number("199")

	// Cardlink says "no such bill" with a 403.
	code, out = e.status("Unknown12")
	wantError(t, code, out, http.StatusNotFound, adapter.CodeNotFound)
	code, out = e.status("../etc")
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
}

// signature is strtoupper(md5(<values>:tok)), the way Cardlink signs its postbacks.
func signature(tok string, values ...string) string {
	h := md5.Sum([]byte(strings.Join(append(values, tok), ":")))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

func postback(status, sum, inv, trs, sig string) string {
	return url.Values{"Status": {status}, "InvId": {inv}, "Commission": {"2.54"}, "CurrencyIn": {"RUB"}, "OutSum": {sum},
		"TrsId": {trs}, "AccountType": {"BANK_CARD"}, "SignatureValue": {sig}}.Encode()
}

func (e *env) webhook(body string) (int, map[string]any) {
	return e.call(http.MethodPost, "/v1/webhook", map[string]any{"settings": settings(), "remote_ip": "203.0.113.5",
		"headers": map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}},
		"body":    base64.StdEncoding.EncodeToString([]byte(body))})
}

func TestWebhook(t *testing.T) {
	e := newEnv(t)
	sig := signature(apiToken, "199.00", "mikan-4217")

	code, out := e.webhook(postback("SUCCESS", "199.00", "mikan-4217", billID, sig))
	if code != http.StatusOK || out["external_id"] != billID {
		t.Fatalf("success: %d %v", code, out)
	}
	code, out = e.webhook(postback("OVERPAID", "199.00", "mikan-4217", billID, strings.ToLower(sig)))
	if code != http.StatusOK || out["external_id"] != billID {
		t.Fatalf("overpaid, lowercase signature: %d %v", code, out)
	}
	// Other statuses are not ours to act on.
	for _, st := range []string{"FAIL", "UNDERPAID", ""} {
		if code, out = e.webhook(postback(st, "199.00", "mikan-4217", billID, sig)); code != http.StatusOK || len(out) != 0 {
			t.Fatalf("status %q: %d %v", st, code, out)
		}
	}
	// Cardlink's other postbacks, each signed its own way, are ignored, not refused: a P2P
	// deal comes to the same Result URL, and a refused one is sent again.
	for name, form := range map[string]url.Values{
		"p2p deal": {"TrsId": {"AbxvAM6vJk"}, "InvId": {"p2p-1"}, "Status": {"SUCCESS"}, "Amount": {"123"}, "Currency": {"RUB"},
			"SignatureValue": {signature(apiToken, "123", "AbxvAM6vJk")}},
		"payout": {"TrsId": {"ZqmawDQ9B7l"}, "Amount": {"50010.00000000"}, "IsAuto": {"false"}, "Status": {"SUCCESS"},
			"SignatureValue": {signature(apiToken, "50010.00000000", "ZqmawDQ9B7l")}},
		"refund": {"Id": {"R1"}, "Amount": {"199.00"}, "Currency": {"RUB"}, "Status": {"SUCCESS"}, "InvId": {"mikan-4217"},
			"BillId": {billID}, "PaymentId": {"P1"}, "SignatureValue": {signature(apiToken, "199.00", "RUB", billID, "P1", "R1")}},
		"chargeback": {"Id": {"C1"}, "Status": {"SUCCESS"}, "InvId": {"mikan-4217"}, "BillId": {billID}, "PaymentId": {"P1"},
			"SignatureValue": {signature(apiToken, billID, "P1", "C1")}},
	} {
		if code, out = e.webhook(form.Encode()); code != http.StatusOK || len(out) != 0 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		// The same postback signed with another token is forged.
		form.Set("SignatureValue", signature("guess", "123", "AbxvAM6vJk"))
		code, out = e.webhook(form.Encode())
		wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	}
	// Forged: signed with another token, over another sum, or not signed.
	code, out = e.webhook(postback("SUCCESS", "199.00", "mikan-4217", billID, signature("guess", "199.00", "mikan-4217")))
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	code, out = e.webhook(postback("SUCCESS", "1.00", "mikan-4217", billID, sig))
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	code, out = e.webhook(postback("SUCCESS", "199.00", "mikan-4217", billID, ""))
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	code, out = e.webhook(`{"Status":"SUCCESS"}`)
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	code, out = e.webhook("%zz")
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
	// Signed and paid, but no bill.
	code, out = e.webhook(postback("SUCCESS", "199.00", "mikan-4217", "", sig))
	wantError(t, code, out, http.StatusBadRequest, adapter.CodeBadRequest)
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
}
