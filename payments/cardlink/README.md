# Cardlink adapter

Bank cards and SBP through [Cardlink](https://cardlink.link/reference/api).
Protocol: [PROTOCOL.md](../../PROTOCOL.md). Image: `ghcr.io/shelbyhell/adapter-cardlink`. Currencies: RUB, USD, EUR.

## Settings

| key | | where |
|---|---|---|
| `api_token` | secret | Cardlink dashboard → API integration (`Authorization: Bearer`); the postback is signed with it too |
| `shop_id` | | the list of shops in the dashboard |
| `method` | optional | `BANK_CARD` or `SBP`; empty: the payer picks on Cardlink's page |
| `payer_pays_commission` | optional | on: Cardlink's fee is added to what the payer pays |

In the shop's settings in the dashboard, enter the notification URL the panel shows for this
adapter as the **Result URL**: Cardlink takes no notification URL per bill. If the dashboard
has an IP allowlist, add the panel server's address to it.

## Behaviour

| | |
|---|---|
| check | `GET /merchant/balance`: 401 → `bad_credentials`. The shop ID is checked by the first bill (`cardlink_shop_not_found`, `cardlink_access_denied`) |
| invoice | `POST /bill/create`, form-encoded: `amount` as `199.00`, `currency_in`, `shop_id`, `order_id` = the idempotency key, `type=normal`, `ttl=86400` (a day, as long as the panel keeps an unpaid invoice); `success_url`, `fail_url` and `return_url` are `return_url`. Cardlink takes those only on the shop's own domain: when it refuses the bill as invalid (HTTP 422), the bill is made again without them, and the payer stays on Cardlink's page. Both attempts are logged with the payment id, and when the second is refused too the error says so, so a 422 for another reason (the amount, the currency) is not hidden. The external id is `bill_id`, the payment page `link_page_url`. Cardlink has no idempotency keys, so a retried request may open a second bill (see below) |
| status | `GET /bill/status?id=`: `SUCCESS` and `OVERPAID` → paid; a bill no longer `active` → canceled; else pending. `FAIL` alone stays pending: a failed attempt leaves the bill open for another card, and a canceled payment could not be paid later. `UNDERPAID` stays pending too: the amount Cardlink gives is the bill's, not what arrived; settle those in the dashboard. Amount and currency are Cardlink's; extra zero decimals are read, fractions of a kopeck are not |
| webhook | the postback is form-encoded and signed with `strtoupper(md5(<fields>:api_token))` in `SignatureValue`, the fields depending on the postback: `OutSum:InvId` for a payment, `Amount:TrsId` for a payout or a P2P deal (which shares the Result URL), `Amount:Currency:BillId:PaymentId:Id` for a refund, `BillId:PaymentId:Id` for a chargeback. A form none of them signs is `bad_request` (constant-time compare). A payment with `SUCCESS` or `OVERPAID` names the bill (`TrsId`); every other signed postback is ignored (`{}`), so Cardlink does not send it again |
| refund | not supported (no `refund` capability): Cardlink opens its refund API only on a support request |

Errors: 401 → `bad_credentials`; 404 and `api:error.bill_not_found` (Cardlink sends it as 403) →
`not_found`; 429 and 5xx → `provider_unavailable`; 422 → `cardlink_http_422`; other refusals
with Cardlink's key → `cardlink_<key>` (`cardlink_ip_access_denied`, `cardlink_shop_not_found`,
`cardlink_merchant_banned`…), without one → `cardlink_http_<status>`.

## No lookup before a bill

The protocol asks an adapter without idempotency keys to look up the invoice a key already
made. Cardlink has `GET /bill/search?order_id=`, but it cannot be relied on for that: it
answers an order without bills with `403 api:error.bill_not_found`, the code it refuses
with, and the reference shows its items without `order_id`, so a bill it returns cannot be
confirmed as this order's. Handing back another order's bill would be worse than a second
bill nobody sees: the panel only ever checks and shows the one it stored, and the other one
expires unpaid after `ttl`. A retry without a lookup is also what the Platega adapter does.

Not checked against a live shop: the error keys, whether a failed attempt really leaves the
bill payable, and whether Cardlink answers return URLs on a foreign domain with 422 or drops
them silently (both work).
