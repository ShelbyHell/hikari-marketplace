# Cardlink adapter

Bank cards and SBP through [Cardlink](https://cardlink.link/reference/api).
Protocol: [PROTOCOL.md](../../PROTOCOL.md). Image: `ghcr.io/getmikan/adapter-cardlink`. Currencies: RUB, USD, EUR.

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
| invoice | `POST /bill/create`, form-encoded: `amount` as `199.00`, `currency_in`, `shop_id`, `order_id` = the idempotency key, `type=normal`; `success_url`, `fail_url` and `return_url` are `return_url`. Cardlink takes those only on the shop's own domain: when it refuses the bill as invalid (HTTP 422), the bill is made again without them, and the payer stays on Cardlink's page. Cardlink has no idempotency keys and does not keep order ids unique, so a retried request may open a second bill; only the stored one is checked and shown to the payer. The external id is `bill_id`, the payment page `link_page_url` |
| status | `GET /bill/status?id=`: `SUCCESS` and `OVERPAID` → paid; a bill no longer `active` → canceled; else pending. `FAIL` alone stays pending: a failed attempt leaves the bill open for another card, and a canceled payment could not be paid later. `UNDERPAID` stays pending too: the amount Cardlink gives is the bill's, not what arrived; settle those in the dashboard. Amount and currency are Cardlink's; extra zero decimals are read, fractions of a kopeck are not |
| webhook | the postback is form-encoded; `SignatureValue` must be `strtoupper(md5(OutSum:InvId:api_token))` (constant time), else `bad_request`. `SUCCESS` and `OVERPAID` name the bill (`TrsId`), other statuses are ignored. Refund and chargeback postbacks go to their own URLs, not to the Result URL |
| refund | not supported (no `refund` capability): Cardlink opens its refund API only on a support request |

Errors: 401 → `bad_credentials`; 404 and `api:error.bill_not_found` (Cardlink sends it as 403) →
`not_found`; 429 and 5xx → `provider_unavailable`; 422 → `cardlink_http_422`; other refusals
with Cardlink's key → `cardlink_<key>` (`cardlink_ip_access_denied`, `cardlink_shop_not_found`,
`cardlink_merchant_banned`…), without one → `cardlink_http_<status>`.
