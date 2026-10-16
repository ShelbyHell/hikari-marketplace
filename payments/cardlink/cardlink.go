package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShelbyHell/hikari-marketplace/internal/adapter"
	"github.com/ShelbyHell/hikari-marketplace/internal/catalog"
)

// cardlink is the Cardlink API (https://cardlink.link/reference/api): every request carries
// the merchant's Bearer token, requests are form-encoded, answers are JSON. A bill is an
// invoice: POST /bill/create makes it, GET /bill/status tells about it.
type cardlink struct {
	api      string
	hc       *http.Client
	manifest catalog.Manifest
	log      *slog.Logger
}

const (
	// codeInvalid is Cardlink's HTTP 422, a request it found invalid.
	codeInvalid = "cardlink_http_422"
	// billTTL is how long a bill can be paid: as long as the panel keeps an unpaid invoice.
	billTTL = 24 * 60 * 60
)

// returnKeys are where the payer goes after paying or failing to: all three get return_url.
var returnKeys = []string{"success_url", "fail_url", "return_url"}

var (
	// billRe is a Cardlink id (a bill's, a shop's) as it goes into a query string.
	billRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	methodRe = regexp.MustCompile(`^(BANK_CARD|SBP)?$`)
	// errorRe is the key Cardlink puts into "message" when it refuses: api:error.bill_not_found.
	errorRe = regexp.MustCompile(`^api:error\.([a-z_.-]{1,48})$`)
)

func (p *cardlink) Info() adapter.Info {
	return adapter.Info{
		ID:           p.manifest.ID,
		Protocol:     p.manifest.Protocol,
		Version:      p.manifest.Version,
		Name:         p.manifest.Name,
		Currencies:   []string{"RUB", "USD", "EUR"},
		Capabilities: []string{adapter.CapWebhook},
		Settings: []adapter.Setting{
			{Key: "api_token", Label: adapter.Text{"ru": "Токен API", "en": "API token"}, Type: "string", Secret: true, Required: true, Pattern: `^[!-~]{1,200}$`},
			{Key: "shop_id", Label: adapter.Text{"ru": "ID магазина", "en": "Shop ID"}, Type: "string", Required: true, Pattern: billRe.String()},
			{Key: "method", Label: adapter.Text{"ru": "Способ оплаты (пусто: выбирает плательщик)", "en": "Payment method (empty: the payer picks)"}, Type: "string", Pattern: methodRe.String()},
			{Key: "payer_pays_commission", Label: adapter.Text{"ru": "Комиссию платит плательщик", "en": "The payer pays the fee"}, Type: "bool"},
		},
		Help: adapter.Text{
			"ru": "Токен API — в личном кабинете Cardlink → API интеграция, ID магазина — в списке магазинов. В настройках " +
				"магазина в поле Result URL укажите адрес уведомлений из панели. Способ оплаты: BANK_CARD карты, SBP СБП; " +
				"пусто — плательщик выберет сам. Если в кабинете включён белый список IP, добавьте в него адрес сервера панели.",
			"en": "The API token is in the Cardlink dashboard → API integration, the shop ID in the list of shops. Enter " +
				"the panel's notification URL as the shop's Result URL. Payment method: BANK_CARD cards, SBP; empty lets " +
				"the payer choose. If the dashboard has an IP allowlist, add the panel server's address to it.",
		},
	}
}

// call sends one request with the token; it is never kept or logged. A GET carries form as
// its query, a POST as its body.
func (p *cardlink) call(ctx context.Context, s adapter.Settings, method, path string, form url.Values, out any) error {
	u, body := p.api+path, io.Reader(nil)
	if method != http.MethodGet {
		body = strings.NewReader(form.Encode())
	} else if len(form) > 0 {
		u += "?" + form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.String("api_token"))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, adapter.MaxBody))
	if err != nil {
		return err
	}
	// Cardlink answers a refusal with {"success": false, "message": "api:error.<key>"},
	// mostly as 403: a missing bill, a foreign shop and an IP outside the allowlist alike.
	// Only the key is passed on, never the rest of the message.
	var res struct {
		Success json.RawMessage `json:"success"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(raw, &res)
	key := ""
	if m := errorRe.FindStringSubmatch(res.Message); m != nil {
		key = strings.NewReplacer(".", "_", "-", "_").Replace(m[1])
	}
	refused := string(res.Success) == "false" || string(res.Success) == `"false"`
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return adapter.BadCredentials("Cardlink refused the API token")
	case resp.StatusCode == http.StatusNotFound || key == "bill_not_found":
		return adapter.NotFound("Cardlink has no such bill")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return adapter.ProviderUnavailable(fmt.Sprintf("Cardlink answered HTTP %d", resp.StatusCode))
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return adapter.Refused(codeInvalid, "Cardlink refused the request as invalid (HTTP 422)")
	case key != "":
		return adapter.Refused("cardlink_"+key, fmt.Sprintf("Cardlink refused the request: %s (HTTP %d)", key, resp.StatusCode))
	case resp.StatusCode/100 != 2:
		return adapter.Refused("cardlink_http_"+strconv.Itoa(resp.StatusCode), fmt.Sprintf("Cardlink refused the request (HTTP %d)", resp.StatusCode))
	case refused:
		return adapter.Refused("cardlink_refused", "Cardlink refused the request")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return adapter.ProviderUnavailable("Cardlink gave an unexpected answer")
	}
	return nil
}

// Check reads the merchant's balances, the one call that needs nothing but the token. The
// shop ID is checked by the first bill: a wrong one is refused as cardlink_shop_not_found
// or cardlink_access_denied.
func (p *cardlink) Check(ctx context.Context, s adapter.Settings) error {
	return p.call(ctx, s, http.MethodGet, "/merchant/balance", nil, nil)
}

// CreateInvoice opens a one-time bill. Cardlink has no idempotency keys, and its lookup by
// order cannot be relied on (README), so a retried request can open a second bill; only the
// one the panel stored is ever checked or shown to the payer.
func (p *cardlink) CreateInvoice(ctx context.Context, r adapter.InvoiceRequest) (adapter.Invoice, error) {
	form := url.Values{
		"amount":      {adapter.FormatMinor(r.Amount)},
		"currency_in": {r.Currency},
		"shop_id":     {r.Settings.String("shop_id")},
		"order_id":    {r.IdempotencyKey},
		"type":        {"normal"},
		"ttl":         {strconv.Itoa(billTTL)},
	}
	if desc := adapter.Truncate(strings.TrimSpace(r.Description), 255); desc != "" {
		form.Set("description", desc)
	}
	if m := r.Settings.String("method"); m != "" {
		form.Set("payment_method", m)
	}
	if r.Settings.Bool("payer_pays_commission") {
		form.Set("payer_pays_commission", "1")
	}
	if r.ReturnURL != "" {
		for _, k := range returnKeys {
			form.Set(k, r.ReturnURL)
		}
	}
	var res struct {
		ID   string `json:"bill_id"`
		Page string `json:"link_page_url"`
		Link string `json:"link_url"`
	}
	err := p.call(ctx, r.Settings, http.MethodPost, "/bill/create", form, &res)
	var e *adapter.Error
	if r.ReturnURL != "" && errors.As(err, &e) && e.Code == codeInvalid {
		// Cardlink takes these URLs only on the domain the shop was registered with. A shop
		// on another domain gets the bill without them: the payer stays on Cardlink's page,
		// and the panel still hears of the payment from the postback. Both attempts are
		// logged, so a 422 for another reason (the amount, the currency) stays visible.
		p.log.Warn("cardlink refused the bill with the return URLs as invalid, trying without them", "payment_id", r.PaymentID)
		for _, k := range returnKeys {
			form.Del(k)
		}
		if err = p.call(ctx, r.Settings, http.MethodPost, "/bill/create", form, &res); errors.As(err, &e) && e.Code == codeInvalid {
			p.log.Warn("cardlink refused the bill without the return URLs as well", "payment_id", r.PaymentID)
			err = adapter.Refused(codeInvalid, "Cardlink refused the bill as invalid, with the return URLs and without them (HTTP 422)")
		}
	}
	if err != nil {
		return adapter.Invoice{}, err
	}
	link := res.Page
	if link == "" {
		link = res.Link
	}
	if !billRe.MatchString(res.ID) || !httpsURL(link) {
		return adapter.Invoice{}, adapter.ProviderUnavailable("Cardlink gave no payment link")
	}
	return adapter.Invoice{ExternalID: res.ID, PayURL: link}, nil
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// Status: SUCCESS and OVERPAID → paid; a bill that is no longer active → canceled; the rest
// pending. FAIL alone is not canceled: a failed attempt leaves the bill open, and the payer
// may try another card on the same link. UNDERPAID stays pending as well: the amount Cardlink
// gives is the bill's, not what arrived, so calling it paid would say more came than did.
func (p *cardlink) Status(ctx context.Context, s adapter.Settings, externalID string) (adapter.Status, error) {
	if !billRe.MatchString(externalID) {
		return adapter.Status{}, adapter.BadRequest("external_id is not a Cardlink bill id")
	}
	var res struct {
		Status   string      `json:"status"`
		Active   *bool       `json:"active"`
		Amount   json.Number `json:"amount"`
		Currency string      `json:"currency_in"`
	}
	if err := p.call(ctx, s, http.MethodGet, "/bill/status", url.Values{"id": {externalID}}, &res); err != nil {
		return adapter.Status{}, err
	}
	amount, ok := parseAmount(res.Amount.String())
	if !ok || res.Currency == "" {
		return adapter.Status{}, adapter.ProviderUnavailable("Cardlink gave a bill without an amount")
	}
	st := adapter.StatusPending
	switch {
	case res.Status == "SUCCESS" || res.Status == "OVERPAID":
		st = adapter.StatusPaid
	case res.Active != nil && !*res.Active:
		st = adapter.StatusCanceled
	}
	return adapter.Status{Status: st, Amount: amount, Currency: strings.ToUpper(res.Currency)}, nil
}

// parseAmount reads Cardlink's amount, which may come with more decimals than two
// (93988.37000000): past the cents only zeros may follow.
func parseAmount(s string) (int64, bool) {
	if whole, frac, ok := strings.Cut(s, "."); ok && len(frac) > 2 {
		if strings.Trim(frac[2:], "0") != "" {
			return 0, false
		}
		s = whole + "." + frac[:2]
	}
	return adapter.ParseMinor(s)
}

// signedFields are what each of Cardlink's postbacks signs, followed by the API token: a
// payment, a payout or a P2P deal, a refund, a chargeback. The first one is the payment.
var signedFields = [][]string{
	{"OutSum", "InvId"},
	{"Amount", "TrsId"},
	{"Amount", "Currency", "BillId", "PaymentId", "Id"},
	{"BillId", "PaymentId", "Id"},
}

// Webhook checks the postback's SignatureValue, strtoupper(md5(<fields>:api_token)), then
// names the bill (TrsId) of a SUCCESS or OVERPAID payment. A postback signed as anything
// else (a P2P deal shares the Result URL) or with another payment status is ignored; only a
// form no formula signs is bad_request.
func (p *cardlink) Webhook(_ context.Context, r adapter.WebhookRequest) (string, error) {
	form, err := url.ParseQuery(string(r.Body))
	if err != nil {
		return "", adapter.BadRequest("not a Cardlink postback")
	}
	kind := signedAs(form, r.Settings.String("api_token"))
	if kind < 0 {
		return "", adapter.BadRequest("the postback signature does not match the API token")
	}
	if st := form.Get("Status"); kind != 0 || (st != "SUCCESS" && st != "OVERPAID") {
		return "", nil
	}
	id := form.Get("TrsId")
	if !billRe.MatchString(id) {
		return "", adapter.BadRequest("a paid postback without a bill")
	}
	return id, nil
}

// signedAs is the index in signedFields of the formula the form's signature matches, -1 for
// none.
func signedAs(form url.Values, token string) int {
	got := strings.ToUpper(form.Get("SignatureValue"))
	for i, fields := range signedFields {
		parts := make([]string, 0, len(fields)+1)
		for _, f := range fields {
			parts = append(parts, form.Get(f))
		}
		sum := md5.Sum([]byte(strings.Join(append(parts, token), ":")))
		if same(got, strings.ToUpper(hex.EncodeToString(sum[:]))) {
			return i
		}
	}
	return -1
}

// same compares in constant time (hashes first, so the length does not leak); an empty
// expected value never matches.
func same(got, want string) bool {
	if want == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
