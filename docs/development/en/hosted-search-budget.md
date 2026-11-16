# Hosted web search billing

Requests declaring hosted search use a soft budget. Authentication, model access and available-balance checks still apply before the call; the gateway neither reserves a hard budget nor changes the client's tools or limits. Concurrent requests or a single request can exceed the balance available at admission.

The gateway charges reported token usage and the actual total search count using the model prices frozen for that request. Search uses the configured per-query rate; response items or tool-call counts are not a substitute for the actual count reported by the supplier. This does not automatically charge other tools.

When search is declared but the supplier does not report a count, or when the reported counts conflict or lack a corresponding price, the charge is marked unpriced. The gateway does not guess the count or treat an absent price as free. An administrator must inspect supplier usage and model pricing; such records are not fully settled.

For an upstream whose search count or price cannot be confirmed, administrators can mark the deployment's search capability unsupported. Standard endpoints and Chat, Responses, and Messages bypass reject search on that deployment before contacting the supplier; bypass never silently drops tools. Ordinary custom functions and read-only token counting remain available.

Responses that meter search include `X-XHub-Budget-State: soft`, `X-XHub-Billing-Scope: tokens_and_search`, `X-XHub-Tool-Costs: search_metered` and `X-XHub-Budget-Guarantee: soft`. The log's price snapshot and cost breakdown retain the billing scope, search count and unit price.

Regression tests use local HTTP suppliers and isolated PostgreSQL. Browser tests create and clean up models and keys in both supported UI languages. These tests do not establish external supplier search availability or guarantee complete usage reporting.
