# API Protocol Comparison: Partner-Facing API

This document evaluates protocol options for exposing the AVS aggregator's functionality to partner applications and the Studio frontend.

---

## Current Architecture

```
Partner App → Widget/iframe → Studio REST API → SDK (gRPC) → AVS Aggregator
                                                                    ↕
                                                              Operators (gRPC streaming)
```

- **Internal**: Operators communicate with the aggregator over gRPC (protobuf). This handles task streaming, result reporting, and real-time state sync.
- **External**: Studio (Next.js) proxies browser/partner requests to the aggregator via `@avaprotocol/sdk-js`, which speaks gRPC internally.
- **Partners and browsers never call gRPC directly.**

---

## Options Evaluated

### 1. gRPC / Protobuf (current internal protocol)

Binary RPC framework using HTTP/2 and protocol buffers for serialization. Already used for operator ↔ aggregator communication.

**Strengths:**
- Schema-first with strong type safety and codegen for most languages
- Efficient binary encoding, lower bandwidth than JSON
- First-class streaming (server, client, bidirectional) — critical for operator task sync
- Backward compatibility via proto field numbering

**Weaknesses for partner use:**
- No native browser support — requires grpc-web + an Envoy or Connect proxy
- Partners need the protobuf toolchain and generated client stubs to integrate
- Binary on the wire — cannot inspect with curl, browser devtools, or Postman
- Smaller ecosystem for API gateways, rate limiters, WAFs, and monitoring
- gRPC status codes are less familiar than HTTP status codes to most developers
- Error messages are opaque without proto definitions

### 2. REST (JSON over HTTP) — recommended for partner API

Standard request/response over HTTP using JSON. The most widely adopted API style.

**Strengths:**
- Universal — any language with HTTP and JSON works, zero client libraries required
- First API call possible with a single curl command
- Directly inspectable in browser devtools, Postman, and logging tools
- Massive ecosystem: every API gateway, CDN, WAF, rate limiter, and monitoring tool supports it natively
- OpenAPI/Swagger provides interactive documentation and try-it-out UIs
- HTTP status codes are universally understood
- Easy to version with URL prefixes (`/v1/`, `/v2/`)

**Weaknesses:**
- No built-in streaming (possible via SSE or WebSockets, but not native)
- No schema enforcement without additional tooling (OpenAPI, Zod, etc.)
- Slightly higher bandwidth than binary protobuf (negligible for this use case)

### 3. Connect (connectrpc.com)

Wire-compatible with gRPC but also supports plain JSON over HTTP. Built by the Buf team.

**Strengths:**
- Reuses existing `.proto` definitions — no schema rewrite
- Supports `fetch()` in browsers without a proxy
- Streaming support over standard HTTP
- Incremental migration from existing gRPC server

**Weaknesses:**
- Still proto-first — partners need to understand protobuf schema concepts
- Smaller community and tooling ecosystem than REST
- Less familiar to most web developers than plain REST
- Documentation tooling less mature than OpenAPI/Swagger

### 4. GraphQL

Query language that lets clients request exactly the fields they need.

**Strengths:**
- Flexible queries — client picks fields, avoids over/under-fetching
- Strong typing via schema
- Good tooling (`gqlgen` for Go, Apollo/urql for frontend)
- Single endpoint simplifies routing

**Weaknesses:**
- Overkill when the API surface is well-defined RPCs (which ours is)
- Adds complexity: query parsing, resolver architecture, N+1 prevention
- Caching is harder than REST (no HTTP cache semantics by default)
- Partners must learn GraphQL query syntax

---

## Side-by-Side: gRPC vs REST for Partner API

| Concern                | gRPC / Protobuf                                  | REST (JSON/HTTP)                                  |
| ---------------------- | ------------------------------------------------ | ------------------------------------------------- |
| **Partner onboarding** | High — proto toolchain, codegen, language support | Low — `curl` or `fetch`, any language              |
| **First API call**     | Minutes to hours (setup proto, generate client)   | Seconds (`curl -X POST ...`)                      |
| **Browser support**    | No (needs grpc-web + proxy)                       | Native `fetch()`, works everywhere                |
| **Debugging**          | Binary — needs grpcurl or generated client        | curl, Postman, browser devtools                   |
| **Documentation**      | Proto files + separate docs site                  | OpenAPI/Swagger with interactive try-it-out        |
| **Type safety**        | Excellent — schema-first codegen                  | Good with OpenAPI — codegen available but optional |
| **Performance**        | Better — binary encoding, HTTP/2 multiplexing     | Good enough for partner request volumes            |
| **Streaming**          | First-class (server, client, bidirectional)        | SSE or WebSockets (sufficient for notifications)  |
| **Versioning**         | Proto field numbering                              | URL prefix (`/v1/`, `/v2/`) — universally understood |
| **Error handling**     | gRPC status codes (less familiar)                  | HTTP status codes — universally understood         |
| **Ecosystem**          | Smaller — interceptors for auth, rate limiting     | Massive — every gateway, WAF, CDN supports it      |
| **API key auth**       | Custom gRPC metadata interceptors                  | Standard `X-API-Key` header                        |
| **Rate limiting**      | Custom interceptor or sidecar proxy                | Off-the-shelf middleware (Redis, API gateway)      |

---

## Decision

**REST for all partner-facing and browser-facing endpoints. gRPC remains for internal operator communication.**

### Rationale

1. **Audience**: Partners are app developers (web, mobile, backend). REST is the lingua franca — no onboarding friction, no toolchain requirements.

2. **Browser constraint**: The Studio frontend and embedded widget cannot call gRPC natively. REST eliminates the need for a grpc-web proxy layer.

3. **Operational simplicity**: REST works with standard infrastructure — API gateways, CDNs, rate limiters, WAFs, logging, and monitoring all support it out of the box.

4. **Performance is not the bottleneck**: Partner API traffic is low-frequency CRUD (create automation, check status, cancel). The performance gap between JSON and protobuf is irrelevant at these volumes. The performance-sensitive path (operator task streaming) stays on gRPC.

5. **Industry standard**: Google, Stripe, Cloudflare, and most cloud providers use this exact pattern — gRPC internally, REST for the public API.

### What stays on gRPC

| Path                          | Protocol | Why                                              |
| ----------------------------- | -------- | ------------------------------------------------ |
| Operator ↔ Aggregator         | gRPC     | Streaming task sync, binary efficiency, real-time |
| Studio SDK → Aggregator       | gRPC     | SDK already speaks gRPC, server-to-server         |
| Partner App → Studio          | REST     | Browser-compatible, zero-friction onboarding      |
| Widget Frontend → Studio      | REST     | Browser `fetch()`, no proxy needed                |

### Implementation path

Studio already acts as the REST proxy layer (see `EXECUTION_CHECKOUT.md`). The partner API extends this with:
- Versioned routes (`/api/v1/executions`)
- API key authentication (`X-API-Key` header)
- Rate limiting (per-key token bucket)
- OpenAPI spec for documentation and client codegen

See `EXECUTION_CHECKOUT.md` → "Partner API: Auth, Rate Limiting, and Versioning" for implementation details.
