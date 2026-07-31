# Tracked stub OpenAPI + GraphQL schemas for `internal/contracts` discovery
# and agent locate (`projects contracts`). Real OSS beds under `.testbeds/active`
# (and legacy `.testbeds/real-oss`) rarely ship on-disk OpenAPI (often
# runtime-generated); these stubs fill that gap.
#
# Layout:
#   api/   — OpenAPI JSON + GraphQL (shared /orders + Order)
#   web/   — OpenAPI YAML + GraphQL (shared /orders + Order)
#   openapi/, graphql/ — shallow-dir name probes