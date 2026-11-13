# Model marketplace and deployment prices

The marketplace syncs from Modelink every day. Models no longer returned by the source are marked as delisted while their full records remain available. Other suppliers may still offer them. Delisting does not disable deployments or delete usage records.

## Sync and confirmation

The initial schedule runs within a minute and then every 24 hours. Existing schedules, including explicitly disabled ones, are preserved. Administrators can sync immediately or change the schedule. Names, descriptions, capabilities, modalities, context limits, documentation and complete pricing rules are refreshed. Failed syncs retain the previous snapshot, record failure status and retry after one hour when scheduling is enabled.

Market data is stored separately from the accepted billing baseline. Neither syncing nor restarting changes deployed prices automatically. The deployment price tab compares current and proposed prices with nothing selected by default. Administrators can review one deployment or a selected batch; manually configured prices have an explicit replacement warning. Market quotes may differ from supplier contracts.

Confirmation sends only deployment IDs and preview versions. The server calculates the rates again and rejects the whole batch with 409 if any price or configuration changed. Successful updates persist deployment rates without recalculating existing bills.

Quick updates support fully expressible single-tier text Token rates, cache reads/writes, complete peak/off-peak rates and explicit per-search rates. Multiple context tiers, conditional modality/reasoning prices, unknown dimensions, missing prices and incomplete windows require manual configuration. The marketplace still displays their complete rules; nothing is silently reduced to the first tier or treated as free.

New supported catalog-priced deployments capture the latest market rates. Existing complex catalog entries retain their creation behavior; new complex source entries need manual prices. YAML deployments are managed in their configuration files. Runtime activation follows existing deployment editing behavior: the current gateway's in-memory directory is updated. Cross-instance configuration broadcasting is outside this change.

## Management API

Existing administrator session permissions apply; ordinary users and virtual keys cannot apply prices.

- `GET /price/catalog`: source metadata, delisting, local overrides and complete `pricing_rules_v2`.
- `POST /reload/model_cost_map`: sync market data now.
- `GET /schedule/model_cost_map_reload/status`: schedule, last attempt, last success and error status.
- `POST /schedule/model_cost_map_reload?hours=24` or `DELETE /schedule/model_cost_map_reload`: save or disable scheduling.
- `GET /price/deployments`: current/proposed rates, differences, unsupported reason and version.
- `POST /price/deployments/apply`: confirm 1–500 deployments. Invalid input returns 400, unauthorized access 401/403, conflicts 409 and storage failures 500.

~~~json
{"items":[{"id":"deployment-id","version":"preview-version"}]}
~~~

## Persistence and compatibility

| Record | Storage key |
| --- | --- |
| Daily market data | `market_snapshot/catalog` |
| Accepted billing baseline | `price_snapshot/catalog` |
| Local overrides | `price_model/<model ID>` |
| Local visibility | `price_listing/<model ID>` |
| Confirmed deployment rates | `proxy_models.litellm_params_json.rates` |
| Schedule and sync status | `model_cost_map/reload` |

No existing tables are migrated or deleted. The first successful sync freezes the existing baseline; future syncs do not replace it. Local models, providers and visibility settings are retained. The default source is `https://api.modelink.ai/v1/market/models`; `XHUB_PRICE_FEED_URL` can select a mirror. A transaction lock serializes syncing, schedule changes and price confirmation.

Disable scheduling before rolling back to an older binary, which may automatically replace the accepted billing baseline. Confirmed rates use the existing `rates` format. This change adds no inbox and changes no authorization, budget rules or coverage threshold.

## Verification

~~~bash
go test ./internal/catalog ./internal/gateway/models ./internal/store -count=1
go test ./cmd/regression -run '^TestModelContractMarketPriceConfirmation$' -count=1
bash scripts/e2e.sh e2e/scenarios/legacy/price-feed/sc-111.test.ts
~~~

Tests use real PostgreSQL, local source and protocol servers, including actual billed requests before and after confirmation, atomic conflict handling, historical bills, delisting/restoration, failed syncs, restarts and Chinese/English browser operations. Supplier credentials and contractual prices are not assumed.
