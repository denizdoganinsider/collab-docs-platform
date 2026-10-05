# Collab Docs Platform

Learning project, the third in a series: backend system design concepts through a
real, running system, one topic-cluster per month. This one is different from the
two before it in one way — it covers **all nine concepts from the source material
in a single system**, rather than splitting them across projects.

This README is the single source of truth for the project. It was written before
the first line of code, in the session that decided the topic, and it is meant to
be enough on its own to start month 1 in a fresh terminal with no other context.
Like the predecessor's README, it is a living document: every month appends a
`Month N` section (what was built, why, how to verify it) and updates
`Known trade-offs`. Sections marked *(planned)* describe intent and are rewritten
into *what happened* when the month closes.

**Timeline: October 2026 – March 2027, six months.** Predecessors:

| Project | Period | Concepts covered | Path |
|---|---|---|---|
| `notification-api` | Feb–May 2026 | Rate Limiting, Auth & Authorization, Caching (Redis), Webhooks | `~/notification-api` |
| `realtime-chat-platform` | Jul–Sep 2026 | Microservice, Reverse Proxy, WebSocket, Load Balancer, CDN | `~/realtime-chat-platform` |
| **`collab-docs-platform`** | **Oct 2026 – Mar 2027** | **all nine** | `~/collab-docs-platform` |

## The nine concepts

Source material: `~/Downloads/Backend System Design Concepts.docx`. The definitions
below are the document's own (Turkish), kept verbatim so the source travels with the
repo. The last three columns are this project's plan.

| # | Concept | Definition (from the source) | Month | Where it lives here |
|---|---|---|---|---|
| 1 | **Rate Limiting** | Rate limiting, bir istemcinin belirli bir zaman aralığında yapabileceği istek sayısını sınırlandırma tekniğidir. Amacı sistemi aşırı yüklenmeden, kötü niyetli kullanımdan (DDoS, brute force) ve kaynak israfından korumaktır. Genellikle IP, kullanıcı, API key veya token bazlı uygulanır. | 4 | gateway (HTTP, sliding window in Redis; login brute-force), doc-service (per-connection token bucket on WebSocket ops) |
| 2 | **Load Balancer** | Load balancer, gelen trafiği birden fazla sunucuya dengeli şekilde dağıtan bir bileşendir. Bu sayede sistem daha ölçeklenebilir, yüksek erişilebilir ve hataya dayanıklı olur. Bir sunucu devre dışı kaldığında trafik otomatik olarak diğerlerine yönlendirilebilir. | 3 | gateway (`internal/loadbalancer`: round-robin + consistent hash on document id, health checks) |
| 3 | **Authentication & Authorization** | Authentication, kullanıcının kimliğini doğrulama sürecidir (ör. login olmak). Authorization ise doğrulanmış kullanıcının hangi kaynaklara ve işlemlere erişebileceğini belirler. Kısaca: Authentication = Sen kimsin?, Authorization = Ne yapabilirsin? | 1 | gateway (JWT, `user`/`admin`), doc-service (per-document roles `owner`/`editor`/`viewer`) |
| 4 | **Caching (Redis)** | Caching, sık kullanılan verilerin daha hızlı erişim için geçici olarak saklanmasıdır. Redis gibi bellek tabanlı cache sistemleri, veritabanı sorgularını azaltarak performansı ciddi şekilde artırır. Özellikle oturum bilgileri, sık okunan veriler ve geçici hesaplamalar için kullanılır. | 4 | doc-service (per-user document list in Redis, invalidated across N instances) |
| 5 | **Microservice** | Microservice mimarisi, büyük bir uygulamanın bağımsız, küçük ve tek sorumluluğa sahip servisler halinde geliştirilmesini ifade eder. Her servis ayrı deploy edilebilir, ölçeklenebilir ve farklı teknolojilerle yazılabilir. Bu yaklaşım esneklik ve geliştirme hızı sağlar ancak operasyonel karmaşıklığı artırır. | 1 | four services, database per service, shared secrets not shared code, edge auth |
| 6 | **Webhooks** | Webhooks, bir sistemde gerçekleşen bir olayın başka bir sisteme otomatik HTTP isteği ile bildirilmesidir. Polling yerine anlık bildirim sağlar. Örneğin: ödeme alındığında, sipariş oluştuğunda veya bir işlem tamamlandığında karşı sisteme haber vermek için kullanılır. | 5 | notification-service (signed, retried deliveries) fed by a **transactional outbox** in doc-service |
| 7 | **Proxy** | Proxy, istemci ile hedef sunucu arasında yer alan bir aracı katmandır. Güvenlik, cache, loglama, erişim kontrolü veya anonimlik sağlamak için kullanılır. Reverse proxy ise genellikle sunucu tarafında konumlanır ve load balancing, SSL termination gibi görevler üstlenir. | 1 | gateway (`httputil.ReverseProxy`, the only public entry point) |
| 8 | **Web Socket** | WebSocket, istemci (browser, mobil uygulama vb.) ile sunucu arasında sürekli açık, çift yönlü (bidirectional) bir iletişim kanalı kuran bir protokoldür. HTTP'den farklı olarak her mesaj için yeni bağlantı açmaz; bu sayede gerçek zamanlı uygulamalarda (chat, canlı bildirimler, finans verileri, oyunlar) düşük gecikme ve daha verimli veri aktarımı sağlar. | 2 | doc-service (`/ws?doc=`: operational-transformation op stream, cursors, presence) |
| 9 | **CDN (Content Delivery Network)** | CDN, statik içerikleri (resim, video, CSS, JS vb.) kullanıcıya coğrafi olarak en yakın sunucudan sunan dağıtık bir ağ yapısıdır. Sayfa yüklenme sürelerini düşürür, ana sunucunun yükünü azaltır ve global kullanıcı deneyimini iyileştirir. | 6 | gateway edge cache in front of publish-service: immutable `/assets/:sha`, mutable `/p/:slug` with **purge** |

## Why a collaborative document editor

The scenario was chosen because every one of the nine concepts has a *natural*
reason to exist in it — none is bolted on to tick a box:

- **WebSocket** is not optional: two people typing in the same document need a
  bidirectional, low-latency channel. Polling would make the product unusable.
- **Load balancer** with **sticky routing** is forced by the concurrency model: a
  document's operation log is ordered by exactly one process, so every client of a
  document must reach the same instance. This is the same sticky-session problem
  the chat project met with rooms, but here it is not a convenience — it is
  correctness. Two instances ordering the same document would corrupt it.
- **Authorization** is richer than the predecessor's global `user`/`admin`: every
  document has its own `owner`/`editor`/`viewer` roles, and a viewer holding a
  perfectly valid WebSocket must still be refused when it sends an edit.
- **Rate limiting** has a new shape: not just requests per minute at the edge, but
  *operations per second on an open stream*. A client pasting a 5 MB file as one
  op, or a script sending 10 000 single-character ops, is the same problem as HTTP
  flooding wearing a different protocol.
- **Caching** meets a cross-instance invalidation problem for real: a user's
  document list is cached, and the instance that changes a membership is not the
  one that will serve the next list read. An in-process cache is the wrong tool
  the moment there are N instances, which is the argument for Redis.
- **Webhooks** carry document events to integrations (`document.shared`,
  `document.published`, `document.updated`), and one of them fires at keystroke
  rate — which forces *coalescing*, a problem the chat project's per-message
  webhooks never had. The predecessor's known trade-off "the delivery queue is in
  memory" is answered with a transactional outbox.
- **CDN** gets both halves of the lesson: immutable content-addressed assets
  (images embedded in documents, rendered publications) exactly as the chat
  project did, *and* a mutable pretty URL per published document — which means
  **purge**, the thing the chat project explicitly said it did not have.
- **Reverse proxy** and **microservice** are the skeleton everything hangs on,
  as before, with one change from day one: authentication is done once at the
  edge and forwarded as identity headers (see *Predecessor lessons*).

Alternatives considered and rejected: a live auction (the strongest alternative —
its bid-ordering conflict is genuinely the same lesson, but its WebSocket usage is
thinner and its CDN part is weaker); live-stream chat (too close to the chat
project); live location tracking (CDN and webhooks would have been forced).

Conflict resolution is **character-level operational transformation (OT)** in the
ot.js / Google Wave lineage: the server orders operations for each document and
clients transform against what they have not yet seen. CRDTs were rejected because
they remove the need for a single ordering process, which would remove the
sticky-routing lesson; block-level last-writer-wins was rejected as too shallow.
The OT core is specified in full below because it is the part a fresh session
cannot infer from the concept list.

## Architecture (target, end of month 6)

Four independently deployable Go services tied together for local development via
a Go workspace (`go.work`). doc-service runs as **N instances** from month 3.

```
                       ┌──────────────────┐
  browser  ───────────▶│     gateway       │   static/editor.html served from here
  (HTTP/WS)   :9000    │                   │
                       │ - register/login  │      consistent hash on doc id
                       │ - JWT -> X-User-ID│ ───┬────────▶ ┌────────────────┐
                       │ - ws-ticket       │    │          │  doc-service    │──┐
                       │ - reverse proxy   │    │          │  :9001          │  │
                       │ - load balancer   │    │          └───────┬────────┘  │
                       │ - rate limiter    │    │                  │           │ outbox
                       │ - edge cache      │    └────────▶ ┌───────┼────────┐  │ relay
                       └──┬─────────┬──────┘  round-robin  │  doc-service    │  │ (HTTP,
                          │         │         /documents   │  :9011          │──┤ signed
                          │         │                      └───────┬────────┘  │ by key)
                          │         │                              │           │
                   ┌──────▼──────┐  │                       ┌──────▼──────┐    │
                   │   MySQL     │  │                       │   Redis     │    │
                   │  :3308      │◀─┼───────────────────────│   :6381     │    │
                   │ docs_gateway│  │                       │ rate limits │    │
                   │ docs_service│  │                       │ doc-list    │    │
                   │ docs_notif  │  │                       │   cache     │    │
                   │ docs_publish│  │                       └─────────────┘    │
                   └─────────────┘  │                                          │
       ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│─ ─ ─
                                    │                                          ▼
   gateway ── /webhook /subscriptions /deliveries ──▶ ┌──────────────────────────┐
              (edge auth: X-User-ID + X-Gateway-Key)  │ notification-service :9003│
                                                      │ - webhooks, subscriptions │
                                                      │ - fan-out, HMAC signing   │──▶ user's
                                                      │ - retries with jitter     │    receiver
                                                      │ - SSRF-filtered transport │
                                                      └──────────────────────────┘

   doc-service ── POST /render (X-Publish-Key) ──────▶ ┌──────────────────────────┐
   gateway ──── POST /documents/:id/assets ──────────▶ │ publish-service   :9004   │
   gateway ──── GET /assets/:sha  (edge cache, immutable) ─▶│ - Markdown -> HTML        │
   gateway ──── GET /p/:slug      (edge cache, purgeable) ─▶│ - content-addressed files │
   publish-service ── DELETE /edge/p/:slug (purge) ──▶ gateway                        │
                                                      └──────────────────────────┘
```

### Services

- **gateway** is the only public entry point. It owns users and authentication
  (`/register`, `/login`, JWT issuance with a `role` claim), issues single-use
  WebSocket tickets, serves the editor page, and is the **reverse proxy** in front
  of everything else. It validates the JWT once and forwards identity downstream as
  `X-User-ID` + `X-User-Role`, with `X-Gateway-Key` proving the request came through
  it; `Authorization` is stripped so no backend ever sees the credential. From
  month 3 it is the **load balancer** across doc-service instances; from month 4
  the **rate limiter**; from month 6 the **edge cache**.
- **doc-service** owns documents: metadata, membership (per-document roles), the
  OT session for every open document, the append-only operation log, periodic
  snapshots, and in-document presence and cursors. It also writes the **outbox**
  rows that become webhooks. A document is served by exactly one instance at a
  time (consistent hash on its id) — the OT session is in-memory and per-process,
  which is the whole reason month 3 exists.
- **notification-service** owns webhooks: a user's endpoint and secret, their
  subscriptions (which documents, which event types), and a delivery log. It
  receives events from doc-service's outbox relay, fans out, signs, delivers with
  retries, and refuses to dial private addresses.
- **publish-service** turns a document at a version into a static HTML page, stores
  it (and uploaded images) content-addressed on disk, and maps a per-document
  **slug** to the current hash. It is the CDN's *origin*; the gateway is the edge.
- **No presence-service.** The chat project needed one because a room's members
  were spread across instances. Here a document lives on one instance, so "who is
  in this document" is in-memory and exact. Rebuilding Redis presence would be
  re-learning chat month 2. Redis in this project is for the two concepts that
  actually call for it: caching and rate limiting.
- **Database per service.** Four schemas in one MySQL container:
  `docs_gateway_db` (users), `docs_service_db` (documents, ops, members, outbox),
  `docs_notification_db` (webhooks, subscriptions, deliveries), `docs_publish_db`
  (publications). `user_id` columns outside the gateway schema have **no foreign
  key** to `users` — MySQL cannot enforce integrity across schemas owned by
  different deployables. The id is trusted because the gateway validated the token
  it came from. That missing constraint is the price of the boundary.

### Ports

Chosen so all three learning projects run side by side (`notification-api` binds
8080/3306/6379, `realtime-chat-platform` binds 8000–8011/3307/6380):

| Component | Port | Notes |
|---|---|---|
| gateway | 9000 | public |
| doc-service | 9001, 9011 (…9021) | one process per port from month 3 |
| notification-service | 9003 | reachable through the gateway only |
| publish-service | 9004 | reachable through the gateway only |
| MySQL | 3308 (host) → 3306 | container `docs_mysql` |
| Redis | 6381 (host) → 6379 | container `docs_redis` |
| webhook receiver (verification only) | 9900 | a throwaway local process |

### Secrets and who shares them

Every secret has **no default** — a service that needs one fails fast at startup
if it is unset. A predictable default would let anyone forge tokens or impersonate
a service. Shared *secret*, never shared *code* — each service reads its own
environment variable; there is no common Go module.

| Secret | Shared between | Purpose |
|---|---|---|
| `JWT_SECRET` | gateway only | signs and validates user tokens (no backend validates JWTs — see edge auth) |
| `GATEWAY_SHARED_KEY` | gateway → doc-service, notification-service, publish-service | proves `X-User-ID` was set by the gateway |
| `NOTIFICATION_API_KEY` | notification-service ← doc-service | outbox relay posting events |
| `PUBLISH_API_KEY` | publish-service ← doc-service | render requests |

Service-to-service keys are compared in constant time (`crypto/subtle`). The trust
they buy is exactly as good as the network boundary: a caller who can reach `:9001`
directly *and* holds `GATEWAY_SHARED_KEY` can claim any user id. That is true of
every such key in this project and is why none of them has a default.

## Repository layout

```
collab-docs-platform/
├── README.md                     this file
├── go.work                       use (./gateway ./doc-service); notification-service (m5) and publish-service (m6) join when they exist
├── docker-compose.yml            mysql:8 (3308) + redis:7 (6381); month 6 adds the services
├── Makefile                      vet, test, test-db, e2e, check
├── scripts/
│   └── e2e.sh                    end-to-end test over real HTTP (72 checks)
├── docs/                         the shareable six-month plan (proje-plani.html / .pdf, Turkish)
├── db/
│   ├── gateway_schema.sql        docs_gateway_db
│   ├── doc_schema.sql            docs_service_db (incl. outbox, used from m5)
│   ├── notification_schema.sql   docs_notification_db      (month 5)
│   └── publish_schema.sql        docs_publish_db           (month 6)
├── static/
│   └── editor.html               single-file client, served by the gateway at /
├── gateway/
│   ├── cmd/gateway/main.go       all wiring: routes, proxies, strategies per route
│   ├── config/                   env parsing, MySQL, Redis (month 4)
│   └── internal/
│       ├── controller/           auth, admin, health, ticket (m2)
│       ├── service/              user_service, ticket_service (m2)
│       ├── repository/           user_repository (raw SQL)
│       ├── domain/               User
│       ├── validation/           email + password policy
│       ├── middleware/           jwt, admin, request_id, logger, ws_ticket (m2), rate_limit (m4)
│       ├── proxy/                trusted_proxy, balanced_proxy (m3)
│       ├── loadbalancer/         strategy, round_robin, consistent_hash, pool (m3)
│       └── edgecache/            byte-bounded LRU + handler (m6)
├── doc-service/
│   ├── cmd/doc/main.go
│   ├── config/
│   └── internal/
│       ├── controller/           document, member, router (shared by main and the matrix test), ops (m2)
│       ├── service/              document_service, member_service, cache (m4), outbox_relay (m5)
│       ├── repository/           document, member, op (m2), outbox (m5) (raw SQL)
│       ├── domain/               Document, Member, Op (m2), OutboxEvent (m5)
│       ├── middleware/           gateway_auth, request_id, logger, instance (m3)
│       ├── ot/                   operation, apply, compose, transform  (+ exhaustive tests) (m2)
│       ├── session/              per-document Session goroutine, op ring, writer, snapshotter (m2)
│       ├── ws/                   handler, client (read/write pumps), message types (m2)
│       └── dispatch/             bounded non-blocking queues (copied pattern) (m5)
├── notification-service/         (month 5) same layering; deliverer, signer, ssrf, fanout
└── publish-service/              (month 6) render (goldmark), storage (content-addressed), slug map
```

Every service is the same four-layer shape as the predecessors — controller →
service → repository → domain, wired by constructor injection in `main.go`,
`database/sql` with raw SQL and `?` placeholders, no ORM. Echo v4 for HTTP,
`gorilla/websocket` for WebSocket, `go-redis/v9`, `golang-jwt/v5`, `bcrypt`.
Go 1.26 (whatever `go version` says when month 1 starts; pin it in `go.work`).

## Data model

Schemas are self-contained (`CREATE DATABASE IF NOT EXISTS` + `USE` + `IF NOT
EXISTS`) so each file can be applied to a live volume as well as run as a
`docker-entrypoint-initdb.d` script. Reminder from the chat project: those scripts
run **only when the data directory is empty**; adding a schema file later means
`docker exec -i docs_mysql mysql -uroot -proot < db/<file>.sql`.

### `docs_gateway_db` (gateway)

```sql
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT AUTO_INCREMENT PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role          ENUM('user','admin') NOT NULL DEFAULT 'user',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### `docs_service_db` (doc-service)

```sql
CREATE TABLE IF NOT EXISTS documents (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    owner_id    BIGINT NOT NULL,                 -- no FK: other schema, other deployable
    title       VARCHAR(255) NOT NULL,
    content     MEDIUMTEXT NOT NULL,             -- snapshot of the text at `version`
    version     BIGINT NOT NULL DEFAULT 0,       -- number of ops folded into `content`
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_documents_owner (owner_id, updated_at)
);

-- Append-only. Version v is the op that takes the document from state v-1 to v.
-- The primary key is the ordering guarantee: two instances that ever tried to
-- write version 42 for the same document would collide here, which is the
-- database-level backstop behind sticky routing.
CREATE TABLE IF NOT EXISTS document_ops (
    doc_id      BIGINT NOT NULL,
    version     BIGINT NOT NULL,
    user_id     BIGINT NOT NULL,
    op          JSON NOT NULL,                   -- [retain, "insert", -delete, ...]
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (doc_id, version)
);

CREATE TABLE IF NOT EXISTS document_members (
    doc_id      BIGINT NOT NULL,
    user_id     BIGINT NOT NULL,
    role        ENUM('owner','editor','viewer') NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (doc_id, user_id),
    INDEX idx_members_user (user_id)
);
-- The owner is also a row here (role='owner'), so every permission check is one
-- lookup in one table. documents.owner_id is kept for listing and for transfer.

-- Month 5. Written in the SAME transaction as the change it describes.
CREATE TABLE IF NOT EXISTS outbox (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_id     CHAR(32) NOT NULL UNIQUE,       -- random hex; the idempotency key downstream
    event_type   VARCHAR(64) NOT NULL,           -- document.shared | document.unshared | document.updated | document.published
    doc_id       BIGINT NOT NULL,
    payload      JSON NOT NULL,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    processed_at DATETIME(3) NULL,
    INDEX idx_outbox_pending (processed_at, id)
);
```

### `docs_notification_db` (notification-service, month 5)

The chat project's schema with an `event_type` filter on subscriptions and
`doc_id` instead of `room`:

```sql
CREATE TABLE IF NOT EXISTS webhooks (
    user_id    BIGINT PRIMARY KEY,
    url        VARCHAR(2048) NOT NULL,
    secret     VARCHAR(64) NOT NULL,             -- clear text: signing needs the value
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS subscriptions (
    user_id    BIGINT NOT NULL,
    doc_id     BIGINT NOT NULL,
    event_type VARCHAR(64) NOT NULL,             -- or '*'
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, doc_id, event_type),
    INDEX idx_subscriptions_doc (doc_id)
);

CREATE TABLE IF NOT EXISTS deliveries (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_id    CHAR(32) NOT NULL,
    event_type  VARCHAR(64) NOT NULL,
    user_id     BIGINT NOT NULL,
    doc_id      BIGINT NOT NULL,
    url         VARCHAR(2048) NOT NULL,
    status      ENUM('pending','delivered','failed') NOT NULL DEFAULT 'pending',
    attempts    INT NOT NULL DEFAULT 0,
    last_status INT NULL,
    last_error  VARCHAR(512) NULL,
    next_attempt_at DATETIME(3) NULL,            -- durable retry schedule (new vs chat)
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_deliveries_user_created (user_id, created_at),
    INDEX idx_deliveries_due (status, next_attempt_at),
    UNIQUE KEY uq_deliveries_event_user (event_id, user_id)
);
```

### `docs_publish_db` (publish-service, month 6)

```sql
CREATE TABLE IF NOT EXISTS publications (
    doc_id       BIGINT PRIMARY KEY,
    slug         VARCHAR(64) NOT NULL UNIQUE,    -- pretty URL: /p/<slug>
    version      BIGINT NOT NULL,                -- document version that was rendered
    html_hash    CHAR(64) NOT NULL,              -- sha256 of the rendered HTML, on disk
    published_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
);
-- Uploaded images and rendered pages live on disk under PUBLISH_DIR/<sha256>,
-- exactly as media-service did. There is no table for them: the address is the
-- content, and a file that exists is valid by construction.
```

## API surface

Auth classes: **public**; **user** (Bearer JWT at the gateway, forwarded as
`X-User-ID`/`X-User-Role` + `X-Gateway-Key`); **admin** (user with `role=admin`);
**service** (a shared key header, never reachable through the gateway). Month
column = when it first exists.

### gateway (:9000)

| Method | Path | Auth | Month | Notes |
|---|---|---|---|---|
| GET | `/` | public | 1 | serves `static/editor.html` (through the edge cache from month 6) |
| GET | `/health` | public | 1 | `{"status":"ok"}` |
| POST | `/register` | public | 1 | `{email,password}` → `201 {id,email,role}`; password policy: ≥ 8 chars, one upper, one digit |
| POST | `/login` | public | 1 | → `{token}` (24 h, claims `user_id`, `role`) |
| GET | `/me` | user | 1 | `{id,email,role}` |
| POST | `/ws-ticket` | user | 2 | → `{ticket}` opaque, 30 s, single-use |
| GET | `/ws?doc=<id>&ticket=<t>` | ticket | 2 | proxied to doc-service (consistent hash on `doc` from month 3) |
| GET | `/admin/users` | admin | 1 | paginated |
| GET | `/admin/documents` | admin | 1 | proxied to doc-service `/admin/documents` |
| GET | `/health/backends` | admin | 3 | `{doc_service:[{backend,healthy}]}` |
| GET | `/health/edge-cache` | admin | 6 | `{entries,used_bytes,max_bytes,hits,misses,purges}` |
| GET | `/p/:slug` | public | 6 | published page; edge cache, `max-age=60` + ETag, purgeable |
| GET | `/assets/:sha` | public | 6 | immutable content; edge cache, `max-age=31536000, immutable` |
| DELETE | `/edge/p/:slug` | service `X-Gateway-Key` | 6 | purge; called by publish-service on republish |
| * | `/documents*`, `/webhook`, `/subscriptions`, `/deliveries` | user | 1/5 | proxied, see below |

Admin routes are `user` routes that additionally require `role=admin` in the
token; the gateway enforces this before proxying, and backends re-check
`X-User-Role` for defence in depth.

### doc-service (:9001, :9011, …)

All routes require `X-Gateway-Key` + `X-User-ID` (+ `X-User-Role`) except
`/health`. `:id` routes are **hash-routed** to one instance from month 3;
`/documents` (list, create) is **round-robined**.

| Method | Path | Requires | Month | Notes |
|---|---|---|---|---|
| GET | `/health` | — | 1 | `{"status":"ok","instance":"doc-9001"}` |
| POST | `/documents` | user | 1 | `{title}` → `201 {id,title,version:0,role:"owner"}`; inserts `documents` + `document_members(owner)` in one tx |
| GET | `/documents?page=&per_page=` | user | 1 | documents the caller is a member of, newest `updated_at` first, `id` tiebreak; **cached per user from month 4** |
| GET | `/documents/:id` | member | 1 | `{id,title,content,version,role,members:[...]}` — served from the live session if open, else from the snapshot + ops replay |
| PATCH | `/documents/:id` | owner or editor | 1 | `{title}` only; content changes go through the WebSocket |
| DELETE | `/documents/:id` | owner | 1 | deletes doc, ops, members; closes the session and evicts sockets with close code 4004 |
| GET | `/documents/:id/members` | member | 1 | `[{user_id,role}]` |
| PUT | `/documents/:id/members/:user_id` | owner | 1 | `{role:"editor"|"viewer"}`; upsert; **month 5** also writes an outbox row |
| DELETE | `/documents/:id/members/:user_id` | owner | 1 | removes; the live session **evicts that user's sockets** (close 4003); owner cannot remove self |
| GET | `/documents/:id/ops?from=<v>` | member | 2 | ops with `version > from`, ascending, max 500 — client catch-up after reconnect |
| GET | `/ws?doc=<id>` | member | 2 | WebSocket upgrade; the OT session (see spec) |
| POST | `/documents/:id/publish` | owner | 6 | `{slug?}` → asks publish-service to render the current version; `{slug,url,version}` |
| POST | `/documents/:id/assets` | owner or editor | 6 | proxied straight to publish-service (multipart `file`, images, 5 MiB) → `{sha,url}` |
| GET | `/admin/documents` | admin | 1 | every document, paginated |

Error body everywhere: `{"error":"<message>"}`. A document the caller is not a
member of answers `403`, not `404`: ids are sequential, so hiding existence would
be theatre.

### notification-service (:9003, month 5)

| Method | Path | Requires | Notes |
|---|---|---|---|
| PUT | `/webhook` | user (via gateway) | `{url}` → `{url,secret}`; the secret is shown **only here** |
| DELETE | `/webhook` | user | |
| POST | `/subscriptions` | user | `{doc_id,event_type}`; `event_type` ∈ `document.shared, document.unshared, document.updated, document.published, *` |
| DELETE | `/subscriptions/:doc_id/:event_type` | user | |
| GET | `/deliveries?page=&per_page=` | user | the caller's own audit trail |
| POST | `/events` | service `X-Notification-Key` | `{event_id,event_type,doc_id,payload,occurred_at}` → `202`; duplicate `event_id` → `202` no-op |

### publish-service (:9004, month 6)

| Method | Path | Requires | Notes |
|---|---|---|---|
| POST | `/render` | service `X-Publish-Key` | `{doc_id,slug,version,title,content}` → renders Markdown → HTML, stores at `sha256(html)`, upserts `publications`, purges `/p/:slug` at the gateway → `{slug,html_hash}` |
| POST | `/assets` | `X-Gateway-Key` + `X-User-ID` | multipart image → `{sha}`; type sniffed from bytes, not the header |
| GET | `/p/:slug` | `X-Gateway-Key` | origin read: `200` HTML, `ETag: "<html_hash>"`, `Cache-Control: public, max-age=60` |
| GET | `/assets/:sha` | `X-Gateway-Key` | origin read: `ETag: "<sha>"`, `Cache-Control: public, max-age=31536000, immutable`, `304` on match |

## OT specification

This is the section that must be complete enough to implement from. It fixes the
operation format, the three algorithms, the server session, the wire protocol and
the client state machine. Everything is plain text; rich text, undo and offline
editing are out of scope for the whole project.

### Positions

Positions and lengths are counted in **Unicode code points**, not bytes and not
UTF-16 code units. Go: `[]rune(s)` / `utf8.RuneCountInString`. JavaScript:
`Array.from(str)` for splitting and `Array.from(str).length` for length — a
`textarea`'s `selectionStart` is in UTF-16 units and **must be converted** before
it is sent as a cursor position (`Array.from(value.slice(0, selectionStart)).length`).
An emoji is one position on both sides; getting this wrong desynchronises the two
peers on the first non-BMP character.

### Operation format

An operation is a JSON array of components applied left to right over the whole
document:

| Component | JSON | Meaning |
|---|---|---|
| retain *n* | positive integer | skip *n* code points unchanged |
| insert *s* | non-empty string | insert *s* at the current position |
| delete *n* | negative integer | delete *n* code points at the current position |

```
document:  "hello world"   (11)
op:        [6, "brave ", -5, "there"]
result:    "hello brave there"
```

Invariants, checked by `ot.Validate` on every op that arrives from the wire:

- `baseLen(op)` = sum of retains + sum of |deletes| **must equal** the length of
  the document the op is applied to. Mismatch → `400`/`error` — never "apply what
  fits".
- `targetLen(op)` = sum of retains + sum of insert lengths.
- Canonical form: no zero components, no two adjacent components of the same
  kind (merge them), and a delete followed by an insert is normalised to insert
  then delete — `compose` and `transform` rely on canonical input, and `Normalize`
  is applied to their output.
- Size: an op is at most 64 KiB serialised; the document at most 1 MiB
  (`DOC_MAX_CODEPOINTS=1048576`); an op whose `targetLen` exceeds it is refused.

### apply(doc, op) → doc

Walk the components with a cursor into `[]rune(doc)`; retain copies, insert
appends, delete skips. Error if the cursor overruns or underruns the input
(`baseLen` mismatch). O(len(doc)).

### compose(a, b) → c

`apply(apply(d, a), b) == apply(d, c)`. Precondition `targetLen(a) == baseLen(b)`.
Standard two-pointer merge over the components of `a` and `b`:

| head of `a` | head of `b` | emit | advance |
|---|---|---|---|
| any | delete *n* while `a`'s head is insert | drop min(n, len(insert)) chars of the insert | both, partially |
| insert *s* | retain *n* | insert min(n, len(s)) chars of *s* | both, partially |
| delete *n* | any | delete *n* | `a` |
| retain *n* | insert *s* | insert *s* | `b` |
| retain *n* | retain *m* | retain min | both, partially |
| retain *n* | delete *m* | delete min | both, partially |
| insert *s* | delete *n* | (cancel) | both, partially |

Used by the client to fold buffered local ops into one, and by the server's
snapshotter (compose the tail into the snapshot). Test against
`apply(apply(d,a),b) == apply(d, compose(a,b))` with a fuzzer over random ops —
this is the single most valuable test in the project.

### transform(a, b) → (a′, b′)

`a` and `b` were both made against the same document state.
`apply(apply(d, a), b′) == apply(apply(d, b), a′)`. Two-pointer merge:

| head of `a` | head of `b` | a′ gets | b′ gets | advance |
|---|---|---|---|---|
| insert *s* | any | insert *s* | retain len(s) | `a` |
| any | insert *t* | retain len(t) | insert *t* | `b` |
| retain *n* | retain *m* | retain min | retain min | both, partially |
| delete *n* | delete *m* | (nothing) | (nothing) | both, partially |
| retain *n* | delete *m* | (nothing) | delete min | both, partially |
| delete *n* | retain *m* | delete min | (nothing) | both, partially |

**Tie-break for concurrent inserts at the same position**: the first rule wins —
`a`'s insert is placed first. The convention for the whole system is **the op
already in the log wins the position**, so both sides always pass the logged op
as `a`:

- server: `(_, clientOp′) = transform(loggedOp, clientOp)` and applies `clientOp′`
- client: `(serverOp′, outstanding′) = transform(serverOp, outstanding)`, applies
  `serverOp′` to the textarea and keeps `outstanding′`

Document this in the code and test it explicitly
(`TestTransformConcurrentInsertLogOrderWins`). If the two sides ever disagree on
which argument is `a`, concurrent inserts at one position converge to *different*
texts and nothing else in the system will tell you why.

Test with the TP1 property over random ops, plus hand-written cases: insert vs
insert same position, delete overlapping delete, insert inside a deleted range
(the insert survives — it is placed at the start of the deletion), delete inside an
inserted range.

### Server: the document session

`doc-service/internal/session`: one `Session` per open document, owned by a
**single goroutine** (`run()`), reached only through channels. No mutex around
document state; ordering is the goroutine's serial execution. This is the same
structural guarantee as the chat project's `Room.run()`.

State: `content string` with its length in code points kept beside it (`ot.Apply`
takes and returns a string), `version int64`, `ring` of the last `OP_RING_SIZE=1000`
`(version, op)` pairs, `clients map[*Client]role`, `pendingSince` (ops since last
snapshot), `dirty bool`.

Lifecycle:

1. **Open** on first `/ws?doc=` (or first `GET /documents/:id` on this instance):
   load `documents.content, version`, then `document_ops WHERE version > snapshot`
   and fold them in. If that replay is more than `OP_RING_SIZE` ops long, snapshot
   immediately.
2. **Client op** `{v, op, seq}` from user `u`:
   - `u`'s role is `viewer` → `error{code:"forbidden"}`, op dropped, socket stays.
   - `v > version` → `error{code:"bad_version"}` and close 4400 (client is ahead of
     the server; impossible unless it is lying).
   - `version - v > OP_RING_SIZE` → close 4409 `"too_far_behind"`; the client
     reconnects and reloads (`snapshot` frame carries everything).
   - Otherwise: for each ring op with version in `(v, version]`, in order,
     `(_, op) = transform(ringOp, op)` — i.e. transform the incoming op past
     everything it has not seen, log order winning ties. Validate `baseLen`.
   - `content = apply(content, op)`; `version++`; push to ring; append to
     `pending writes`.
   - **Persist before ack**: the writer goroutine (one per session) does
     `INSERT INTO document_ops` for the batch it currently holds. Only after the
     commit returns does the session send `ack{v:newVersion, seq}` to the sender
     and `op{v, user_id, op}` to everyone else. Latency cost: one MySQL round trip
     per batch. What it buys: a client never holds an ack for an op the database
     does not have. A crash between the two is a *duplicate*, never a loss, and
     the `(doc_id, version)` primary key turns the duplicate into an error the
     client treats as "reconnect and catch up".
   - If the insert fails for any reason other than duplicate key → close every
     socket with 1011, drop the session; the next open replays from the database.
3. **Snapshot** when `pendingSince >= SNAPSHOT_EVERY_OPS=100` or
   `SNAPSHOT_EVERY_SECONDS=30` has passed with `dirty`: `UPDATE documents SET
   content=?, version=? WHERE id=? AND version < ?` (monotonic guard). Ops are
   **not** deleted after a snapshot; the log is history. (A pruning job is
   explicitly out of scope.)
4. **Cursor** `{pos, sel}`: validated against `len(content)`, fanned out to the
   other clients as `cursor{user_id, pos, sel, v}`, never stored, never persisted —
   the chat project's typing-indicator rule. Receivers transform stale cursors
   themselves.
5. **Membership change** arrives on the session channel (the HTTP handler on the
   *same* instance sends it — hash routing guarantees that): role downgraded to
   viewer → keep socket, future ops refused; removed → close 4003; upgraded →
   nothing to do. `presence` frame re-sent.
6. **Close** when the last client leaves: final snapshot if dirty, then the
   session is dropped after `SESSION_IDLE_SECONDS=60` (kept warm briefly for the
   reconnecting tab).
7. **Shutdown** (SIGTERM): stop accepting upgrades, snapshot every dirty session,
   close sockets with 1001 (going away). Clients reconnect; the load balancer's
   health check has already marked the instance down.

The writer never blocks the session goroutine: ops are handed over on a bounded
channel; when it is full the session applies **backpressure** by not reading from
sockets (the read pump blocks on the session channel) rather than dropping ops —
dropping an *edit* is not like dropping a presence event.

### Wire protocol

All frames are JSON text frames. Unknown `type` → `error{code:"bad_frame"}`,
socket stays open. Any malformed JSON → close 1003.

Client → server:

| Frame | Fields | Notes |
|---|---|---|
| `op` | `v` (int64, base version), `op` (array), `seq` (int, client-local, echoed in the ack) | at most **one in flight** per client; a second before the ack → close 4400 |
| `cursor` | `pos` (int), `sel` (int, selection end, ≥ pos) | rate-limited client-side to ~20/s; server drops extras silently |
| `ping` | — | optional; the server also sends protocol-level pings every 54 s (`pongWait` 60 s, same constants as chat) |

Server → client:

| Frame | Fields | Notes |
|---|---|---|
| `snapshot` | `doc_id`, `title`, `v`, `content`, `role`, `members:[{user_id,role}]`, `presence:[{user_id}]` | first frame after upgrade; also after `4409` reconnect |
| `ack` | `v` (new version), `seq` | sent only to the op's author |
| `op` | `v`, `user_id`, `op`, `seq` | sent to everyone *but* the author |
| `cursor` | `user_id`, `pos`, `sel`, `v` | |
| `presence` | `users:[{user_id}]` | on join/leave/membership change |
| `title` | `title` | after a successful `PATCH` |
| `error` | `code`, `message` | non-fatal (`forbidden`, `bad_frame`, `rate_limited` from m4) |

Close codes: `4003` membership revoked, `4004` document deleted, `4400` protocol
violation, `4409` too far behind — reload, `4429` rate limit exceeded (month 4),
`1011` server error, `1001` shutdown.

### Client state machine (ot.js)

Implemented in `static/editor.html`, ~150 lines of the file. Three states:

```
state        = Synchronized | AwaitingConfirm(outstanding) | AwaitingWithBuffer(outstanding, buffer)
version      = last server version applied locally
seq          = 0

localEdit(op):                            // from the textarea diff
  Synchronized:          send {op, v:version, seq:++seq}; state = AwaitingConfirm(op)
  AwaitingConfirm(o):    state = AwaitingWithBuffer(o, op)
  AwaitingWithBuffer(o,b): state = AwaitingWithBuffer(o, compose(b, op))

serverOp(sop):                            // "op" frame from another user
  Synchronized:          applyToTextarea(sop)
  AwaitingConfirm(o):    (sop', o') = transform(sop, o)          // server op as `a`: log order wins
                         applyToTextarea(sop'); state = AwaitingConfirm(o')
  AwaitingWithBuffer(o,b): (sop', o') = transform(sop, o)
                         (sop'', b') = transform(sop', b)
                         applyToTextarea(sop''); state = AwaitingWithBuffer(o', b')
  version = sop.v

serverAck(ack):
  version = ack.v
  AwaitingConfirm(o):      state = Synchronized
  AwaitingWithBuffer(o,b): send {op:b, v:version, seq:++seq}; state = AwaitingConfirm(b)
```

`applyToTextarea` must preserve the user's caret: convert the caret to a code-point
position, transform it through the incoming op (an insert before it shifts it, a
delete spanning it clamps it), apply the text, convert back and set
`selectionStart/End`. Also transform every remote cursor the same way.

**Generating ops from a `<textarea>`**: on every `input` event, diff the previous
value against the new one by longest common prefix and suffix (both in code
points). The middle is `[prefix, inserted?, -deletedLen?, suffix]`. This produces
exactly one op per event and is correct for typing, deleting, pasting and
cut — it is wrong only for a simultaneous replace at two places, which a textarea
cannot produce.

**Reconnect**: on any close except 4003/4004, wait 1 s (then 2, 4, up to 10) and
reconnect with a fresh ticket. On `snapshot`, if a local `outstanding`/`buffer`
exists, *drop it* and show a one-line notice — resubmitting an op against an
unknown server state is exactly what OT cannot do safely, and this is a learning
project, not a product. (Offline editing is out of scope.)

### Limits (defaults, all env-configurable)

| Name | Default | Enforced where |
|---|---|---|
| `DOC_MAX_CODEPOINTS` | 1 048 576 | session, on `targetLen` |
| `OP_MAX_BYTES` | 65 536 | ws read limit (`SetReadLimit`) and validator |
| `OP_RING_SIZE` | 1000 | session |
| `SNAPSHOT_EVERY_OPS` / `_SECONDS` | 100 / 30 | snapshotter |
| `SESSION_IDLE_SECONDS` | 60 | session close |
| `WS_OPS_PER_SECOND` (m4) | 20 burst 40 | per connection token bucket |
| `WS_OPS_PER_MINUTE_PER_USER` (m4) | 600 | Redis, across connections and instances |

## Roadmap

Each month ends with: the `Month N` section of this README rewritten from *planned*
into *what happened*, a Verification block that a reader can run, the
`Known trade-offs` list updated, `go test ./... -race` green, and one commit
`Month N: <summary>` (review findings addressed in a follow-up commit, as in the
predecessors). The four-service `Running locally` block below is updated as
services appear.

### Month 1 — Auth & Authorization, Microservice, Reverse Proxy *(done, ahead of schedule: 15–17 September 2026)*

Goal was: two services and a browser page that can log in, create documents,
share them, and open them read-only; no WebSocket; every decision about
identity and boundaries made now. That is what exists. The month was scheduled
for October and was pulled forward into three September sessions because the
README was finished early and the skeleton was cheap.

What was built:

1. **Repo skeleton**: `go.work` (two modules for now — `use` entries must point
   at real modules, so notification/publish join in months 5/6), `docker-compose.yml`
   (`docs_mysql` 3308, `docs_redis` 6381, health checks, initdb mounts),
   `db/gateway_schema.sql`, `db/doc_schema.sql` (all four tables including the
   month 5 `outbox`, so the initdb volume never needs a re-apply), `.gitignore`.
   Both schema files were verified to be idempotent against a live volume.
2. **gateway** — `/register` (`201`, duplicate email `409`, policy violations
   `400`), `/login` (`{token}`, 24 h, claims `user_id`/`role`/`exp`; wrong
   password and unknown user return the *same* `401` body), `/me`, `/health`,
   `/admin/users` (`{users,page,per_page,total}`, `per_page` clamped to 100).
   Ported from notification-api and re-cut: `validation/` (README policy —
   ≥ 8 chars, one upper, one digit; the predecessor's lowercase rule was dropped
   because the README is the source of truth), `middleware/jwt.go` (HS256
   enforced in the keyfunc, `alg=none` refused — tested), `admin.go`,
   `request_id.go`, `logger.go` (path only, never the query string), `service/`,
   `repository/` (`ORDER BY created_at DESC, id DESC`). `JWT_SECRET` and
   `GATEWAY_SHARED_KEY` have no default; a missing one is an `ERROR` log line
   and exit 1.
3. **Edge auth from day one**: `internal/proxy/trusted_proxy.go` (from chat,
   plus `X-User-Role`) overwrites any client-supplied `X-User-ID`, `X-User-Role`,
   `X-Gateway-Key`, strips `Authorization`, carries `X-Request-ID` across the
   hop, and answers a dead backend with `502 {"error":"backend unavailable"}`.
   `/documents*` is proxied for any authenticated user; `/admin/documents` only
   after the gateway's own `role=admin` check.
4. **doc-service** — `gateway_auth` (constant-time key compare, `X-User-ID`
   parsed as a positive int64, any role other than `admin` normalised to `user`),
   `RequireAdmin` re-check for defence in depth, documents CRUD, members CRUD,
   `/admin/documents`, pagination `updated_at DESC, id DESC`. Permission is one
   `document_members` lookup; the owner row is inserted in the same transaction
   as the document; delete removes ops, members and the document in one
   transaction. A non-member gets `403`, never `404`. The owner's own row is
   untouchable through the members API (`400` on self-demote / self-remove /
   assigning `owner`). Removing a member is immediate: the next read is `403`.
5. **Reverse proxy**: `httputil.ReverseProxy` to `DOC_SERVICE_URLS[0]`; more
   than one entry is accepted with a warning (used from month 3).
6. **`static/editor.html` v1**: login/register, document list with role and
   version, create, open (read-only `<textarea disabled>`), rename (owner and
   editor), delete (owner), members panel (owner: share by user id with a role,
   change role, remove; others: read-only list). Plain `fetch`, token in
   `localStorage` and always in a `Bearer` header. Served at `/` by the gateway
   so the page and the API share an origin. Exercised in Chrome: A shares with
   user 2 as editor; B logs in, sees the document with role `editor`, has Rename
   but neither Delete nor the share form, and renames it.
7. **Tests**: `validation`, `jwt` (expired, wrong key, `alg=none`, missing
   claim), `admin`, `request_id`, `user_service` (mock repo), `trusted_proxy`
   (three attacker-controlled headers, backend down), `gateway_auth` (11 cases),
   and the **permission matrix** — owner/editor/viewer/stranger × read, members,
   rename, share, unshare, delete — against a real MySQL when `DOCS_TEST_DSN` is
   set (skipped otherwise). Plus `scripts/e2e.sh`: starts both services with a
   random gateway key, registers fresh users, walks the block below and the
   matrix over real HTTP (67 checks), asserts on the logs, and cleans up.

Things that differed from the plan, and the lesson in each:

- **Echo's empty-prefix group is a trap.** `e.Group("", jwtMiddleware)`
  registers catch-all handlers for `/` and `/*` that carry the middleware. It
  shadowed `GET /` (the editor answered `401`) and turned every unknown path
  into a `401` instead of a `404`. Both services now attach middleware per
  route. Test: `GET /nope` is `404` on both ports.
- **`go run` leaves a child behind.** Killing the `go run` parent does not free
  the port; the compiled binary keeps listening and the next start fails with
  `bind: address already in use` while the *old* binary keeps answering. The
  e2e script kills by port (`lsof -ti`) for that reason.
- **`created_at` after `INSERT`**: `LastInsertId` gives the id, not the
  database-assigned timestamp. The repository reads the row back so `201`
  carries the real value instead of Go's zero time.
- **Response shapes** were pinned: `GET /documents` is a bare array (the
  verification block's `jq length` depends on it); the admin lists are
  `{users|documents, page, per_page, total}`.
- **Admin is assigned by SQL only.** There is no endpoint that grants
  `role=admin`; the verification does `UPDATE users SET role='admin'`. That is
  deliberate for a learning project and listed under trade-offs.

Design notes kept (unchanged from the plan, now backed by code):

- The gateway is the **only** JWT validator. Backends trust headers because the
  key proves who set them. This closes the chat project's "three copies of
  jwt.go" trade-off before it opens.
- Permission is checked in doc-service, not the gateway: the gateway does not
  know what a document is. *Authentication at the edge, authorization at the
  owner of the resource* is the month's lesson.
- `viewer` can read `/documents/:id` and (month 2) hold a socket; `editor` can
  also edit and rename; `owner` can also share, unshare, delete, publish.

Verification — run on a fresh volume (`docker compose down -v && docker compose up -d`),
output as observed on 2026-09-17:

```bash
JSON='Content-Type: application/json'
curl -s -X POST localhost:9000/register -H "$JSON" -d '{"email":"a@test.com","password":"Passw0rd1"}'
# {"id":1,"email":"a@test.com","role":"user","created_at":"2026-09-17T18:50:36Z"}
curl -s -X POST localhost:9000/register -H "$JSON" -d '{"email":"b@test.com","password":"Passw0rd1"}'
# {"id":2,"email":"b@test.com","role":"user","created_at":"2026-09-17T18:50:36Z"}
TOKEN_A=$(curl -s -X POST localhost:9000/login -H "$JSON" -d '{"email":"a@test.com","password":"Passw0rd1"}' | jq -r .token)
TOKEN_B=$(curl -s -X POST localhost:9000/login -H "$JSON" -d '{"email":"b@test.com","password":"Passw0rd1"}' | jq -r .token)

DOC=$(curl -s -X POST localhost:9000/documents -H "$JSON" -H "Authorization: Bearer $TOKEN_A" -d '{"title":"Design notes"}' | jq -r .id)
curl -s localhost:9000/documents/$DOC -H "Authorization: Bearer $TOKEN_B" | jq -c .
# {"error":"you do not have access to this document"}                         (403)
curl -s -X PUT localhost:9000/documents/$DOC/members/2 -H "$JSON" -H "Authorization: Bearer $TOKEN_A" -d '{"role":"viewer"}'
# {"user_id":2,"role":"viewer"}
curl -s localhost:9000/documents/$DOC -H "Authorization: Bearer $TOKEN_B" | jq .role
# "viewer"
curl -s -X PATCH localhost:9000/documents/$DOC -H "$JSON" -H "Authorization: Bearer $TOKEN_B" -d '{"title":"x"}'
# {"error":"you do not have access to this document"}                         (403)

# the boundary holds: identity headers from a client are discarded
curl -s localhost:9000/documents -H "Authorization: Bearer $TOKEN_B" -H "X-User-ID: 1" | jq length
# 1                       B's list (the one shared document), not A's
curl -s localhost:9001/documents -H "X-User-ID: 1"
# {"error":"missing or invalid gateway key"}                                  (401)

curl -s localhost:9000/documents/$DOC -H "Authorization: Bearer $TOKEN_A" | jq -c .
# {"id":1,"owner_id":1,"title":"Design notes","content":"","version":0,
#  "created_at":"2026-09-17T18:50:37.129Z","updated_at":"2026-09-17T18:50:37.129Z",
#  "role":"owner","members":[{"user_id":1,"role":"owner"},{"user_id":2,"role":"viewer"}]}

grep -ci -E 'authorization|bearer' doc-service.log
# 0

./scripts/e2e.sh
# passed: 67  failed: 0
```

Exit criteria — all met: the permission matrix test passes (`DOCS_TEST_DSN` set);
`editor.html` logs in, creates, shares and opens; `Authorization` never appears
in a doc-service log line; unknown paths are `404`; the e2e script is green.

Review findings addressed (pre-push panel on the month commit, follow-up
commit `Address review findings: …`):

- **A helper that writes the response and returns `nil` is a trap.** doc-service's
  `paging()` returned `c.JSON(400, …)`, whose success value is `nil`, so the list
  handlers carried on after the 400 was on the wire and appended a 200 payload to
  it. Fixed by returning a typed error and letting `respondError` write once; the
  matrix test and the e2e script now assert the 400 body is a single object.
- **Infrastructure failures were dressed up as client errors.** The gateway
  answered a dead MySQL on `/register` with `400` plus the driver's message,
  collapsed every repository error on `/login` into `401 invalid email or
  password`, and turned a `/me` blip into a `401` the editor treats as "log out".
  It now has a `respondError` like doc-service: validation `400`, duplicate `409`
  (including the unique-index loser of a concurrent registration, MySQL 1062),
  bad credentials `401`, deleted user `401`, everything else logged + bare `500`.
- Smaller: rename's read-back maps a mid-delete `ErrNoRows` to `403` instead of
  `500`; `page` is clamped to 1 000 000 so the offset cannot overflow into a
  negative `OFFSET`; a `DOC_SERVICE_URLS` entry without an `http(s)://` scheme
  and host fails at startup instead of 502-ing every request; both `sql.DB`
  pools are bounded (25/25, 5 min lifetime) and both servers set
  `ReadHeaderTimeout`/`IdleTimeout` (no `WriteTimeout`: month 2's WebSocket
  streams); the proxy transport has a 30 s `ResponseHeaderTimeout`; doc-service's
  global role constants moved to `domain` next to the per-document ones.
- Second round (commit `Fix review findings: …`): the `500` on a `/me` blip did
  not help while the editor's `boot()` logged out on *any* error, so only a `401`
  ends the session now and anything else is shown with the token kept; and the
  password upper bound was 128 while bcrypt refuses input over 72 bytes, which
  made a long password a logged `500` on `/register` — the limit is 72 bytes, a
  `400`.
- Not changed: the compose images are public `mysql:8`/`redis:7` (the org
  container whitelist does not apply to this personal learning repo; the
  predecessors use the same images) and the proxy keeps the inbound `Host`
  (doc-service is not host-routed; revisit if an ingress ever sits in front).

### Month 2 — November 2026 — WebSocket *(in progress — started 5 October 2026, ahead of schedule)*

Goal: two browsers editing the same document, live, on one doc-service instance.
This is the OT month; the spec above is the contract.

Done so far (items 1, 2 and 5; the rest of the scope below is still planned):

- `doc-service/internal/ot/` — `Component`/`Operation` with the README's JSON
  array form (`UnmarshalJSON` refuses floats, booleans, null and nested values),
  `BaseLen`/`TargetLen` in code points, `Validate` (zero components, adjacent
  same-kind, delete-before-insert, `targetLen` bound), `Normalize` (idempotent;
  delete+insert reordered to insert+delete), `Apply`, `Compose`, `Transform`.
  Tests: table cases for every row of the compose and transform tables, the
  four hand-written transform cases, `TestTransformConcurrentInsertLogOrderWins`
  (which also proves that swapping the arguments converges on a *different*
  text), and the two properties — `apply(apply(d,a),b) == apply(d,compose(a,b))`
  and TP1 — over 10 000 random documents/ops each, with an alphabet that mixes
  ASCII, Turkish letters, CJK and emoji so a byte/code-point slip fails the
  property. `FuzzCompose`/`FuzzTransform` wrap the same checks for
  `go test -fuzz`. The `-count=200` command below ran clean before anything else
  in the month was started.
- `GET /documents/:id/ops?from=<v>` — `OpRepository.ListAfter` (`version > from`,
  ascending, `LIMIT 500`) and `Insert` (the writer's single-row append; a
  duplicate `(doc_id, version)` comes back as the driver error, untouched),
  `DocumentService.ListOps` (any member; `from < 0` is a 400),
  `DocumentController.ListOps` (`from` parsed with the same no-write-then-nil
  rule as `paging`). The op body is relayed as stored (`json.RawMessage`):
  validation happens on the write path. The matrix test gained an `ops` row and
  `TestListOps` seeds three rows through the repository; e2e is at 72 checks.
- `doc-service/internal/session/` — `Manager` (registry, one `Session` per open
  document) and `Session` (one goroutine, channels in, no mutex on document
  state), behind two small interfaces: `Store` (load, ops after a version,
  append a batch, snapshot) and `Client` (`UserID`, non-blocking `Send`, `Close`
  with a code), so the package is tested with a fake of each and knows nothing
  of sockets. `repository.SessionStore` is the real `Store`:
  `OpRepository.InsertBatch` (one transaction; MySQL 1062 becomes
  `domain.ErrVersionTaken`) and `DocumentRepository.Snapshot` (the monotonic
  `version < ?` update). Decisions the spec left open:
  - **One trip to the database carries the queued ops and, when due, the
    snapshot of the version they end at.** The writer runs one job at a time, so
    a snapshot can never be ahead of the log, and batching needs no timer: what
    queued while the writer was busy is the next batch.
  - **Backpressure bound**: 256 ops applied in memory and not yet committed
    (`Limits.MaxPending`, not an environment variable); at the bound the session
    stops selecting on the frame channel, so the read pumps block in `Deliver`.
  - **A duplicate version on insert** closes every socket with `4409` and drops
    the session (memory is behind the log; reload is the only honest answer).
    Any other insert error is the spec's `1011`.
  - **A full send buffer** closes that client with `4409`.
  - **A client that joins while an op is uncommitted** gets it in its snapshot
    frame and is skipped when the `op` frame goes out (`joinedAt`), otherwise it
    would apply it twice. Its snapshot is then briefly ahead of the database; a
    crash in that window drops every socket, and the reconnect snapshot resets it.
  - **An op that fits alone but not after transformation** (two users filling
    the last free code points) is answered with `error{code:"too_large"}` and
    `4400`.
  - Cursor frames go through a 20/s token bucket per socket; the rest are dropped
    without a reply.
  The limits are read in `config` (`DOC_MAX_CODEPOINTS`, `OP_RING_SIZE`,
  `SNAPSHOT_EVERY_OPS`, `SNAPSHOT_EVERY_SECONDS`, `SESSION_IDLE_SECONDS`; a value
  that is set but not a positive integer stops startup). `main.go` builds the
  manager and calls its `Shutdown` before the HTTP server's, but nothing opens a
  session yet: that is item 3's upgrade handler, and with it the membership,
  title and delete notifications from the HTTP handlers. The ack-latency
  measurement in the design notes waits for real sockets too. Tests: 31 cases
  with `-race` (persist before ack, log-order tie-break, every close code, batch
  sizes, backpressure, snapshot triggers, idle drop and reopen from the log,
  shutdown) and a property — 2 000 random edits against current and stale
  versions, after which the log folded over the first snapshot must equal memory.
  `repository/session_store_test.go` runs the two SQL guarantees against MySQL
  (`DOCS_TEST_DSN`): a batch over a taken version writes nothing, and a snapshot
  never moves a document backwards.

Scope:

1. `doc-service/internal/ot/`: `Operation`, `Validate`, `Normalize`, `Apply`,
   `Compose`, `Transform` with table tests, the two property tests (compose
   correctness, TP1) driven by a random-op generator, and the tie-break test.
   **Do this first and alone**; nothing else in the month is worth starting while
   the fuzzer fails. *(done)*
2. `internal/session/`: `Session` goroutine, op ring, writer goroutine with
   batching, snapshotter, idle close, shutdown snapshot. *(done)*
3. `internal/ws/`: upgrade handler (checks membership → role; `WS_ALLOWED_ORIGINS`
   with the chat project's "missing Origin is allowed" rule), `Client` with read
   and write pumps (`pongWait` 60 s, `writeWait` 10 s, `SetReadLimit(OP_MAX_BYTES)`),
   slow-consumer eviction (a full send buffer closes the socket, as in chat —
   for an editor this is right: a client that cannot keep up will be behind and
   must reload anyway).
4. **Ticket exchange** ported from chat (`ticket_service.go`, `ws_ticket.go`): the
   token never travels in a URL; the gateway redeems the ticket, sets the identity
   headers on the outbound upgrade request, strips `?ticket=`.
5. `GET /documents/:id/ops?from=` for catch-up. *(done)*
6. `editor.html` v2: the state machine, textarea diffing, caret preservation,
   remote cursors (a coloured bar per user rendered in an overlay behind the
   textarea, or simply a list "user 2 at line 4" — the visual is not the lesson),
   presence list, reconnect with backoff.
7. `GET /documents/:id` answers from the live session when one is open on this
   instance (so it is never behind the sockets).

Design notes:

- Persist-before-ack is the month's big decision; write down the measured ack
  latency with a single client and with the writer batching under load.
- Why one goroutine per document and not a mutex: the transform-apply-log sequence
  must be atomic per document, and a goroutine makes it impossible to forget the
  lock. It also makes the `run()` loop the single place ordering is defined, which
  month 3 relies on.
- Cursor frames are never persisted or acked; the chat project's typing-indicator
  argument.

Verification:

```bash
TICKET=$(curl -s -X POST localhost:9000/ws-ticket -H "Authorization: Bearer $TOKEN_A" | jq -r .ticket)
websocat -v "ws://localhost:9000/ws?doc=$DOC&ticket=$TICKET"
# first frame => {"type":"snapshot","v":0,"content":"","role":"owner",...}
{"type":"op","v":0,"op":["hello"],"seq":1}
# => {"type":"ack","v":1,"seq":1}
# a second websocat as B (viewer) sees {"type":"op","v":1,"user_id":1,"op":["hello"]}
# B sends an op => {"type":"error","code":"forbidden"}
# upgrade B to editor; both type concurrently at the same position: both sides
# converge on the same text, log order first

docker exec -i docs_mysql mysql -uroot -proot -e \
  'SELECT version, JSON_COMPACT(op) FROM docs_service_db.document_ops WHERE doc_id=1 ORDER BY version'
# 101 ops => documents.version = 100, content = folded text (snapshot fired at 100)

kill -9 $(lsof -ti :9001); restart; reconnect => snapshot at the last acked version, nothing lost
go test ./doc-service/... -race -run 'TestCompose|TestTransform' -count=200
```

Exit criteria: two browser tabs typing simultaneously converge every time; the
fuzzers run 10 000 iterations clean; `kill -9` loses no acked op.

### Month 3 — December 2026 — Load Balancer *(planned)*

Goal: N doc-service instances behind the gateway, with the sticky-routing
correctness argument made visible.

Scope:

1. `gateway/internal/loadbalancer/` ported from chat: `Strategy` interface,
   `RoundRobin` (over the healthy list), `ConsistentHash` (150 virtual nodes,
   FNV-1a + MurmurHash3 finalizer — keep the chat project's regression tests
   `TestConsistentHashSpreadsRoomsEvenly` / `...MovesFewKeysWhenScalingOut`,
   renamed for documents), health poller (`LB_HEALTH_INTERVAL_SECONDS=5`, one
   synchronous sweep at startup), `GET /health/backends` (admin).
2. Per-route strategy in `main.go`:

   | Route | Strategy | Why |
   |---|---|---|
   | `GET /ws?doc=` | consistent hash on `doc` | the session is in one process's memory |
   | `/documents/:id` and everything under it | consistent hash on `:id` | the *same* process must serve reads (live session), membership changes (session eviction) and publish (current version) |
   | `GET /documents`, `POST /documents` | round-robin | user-scoped, any instance can answer from the database |

   The second row is the difference from the chat project, where only the socket
   was sticky. Here HTTP writes to a document must reach its session too — a
   membership removal handled on the wrong instance would leave the evicted user's
   socket open on the right one.
3. `INSTANCE_ID` (default `doc-<port>`, validated at startup), `X-Doc-Instance`
   response header (also on the 101 — pass headers to `Upgrade`), `request_id`
   shared across the proxy hop.
4. **Fail closed**: the ring is built over all configured instances and never
   rebuilt. A document whose instance is down gets `503 "the instance serving
   this document is unavailable"` until it returns. Same reasoning as chat month 3,
   with a stronger version: if the ring shrank and two instances ever held the
   same document, the `(doc_id, version)` primary key would start rejecting
   writes on one of them — the database backstop mentioned in the schema.
5. Shutdown ordering: SIGTERM → health endpoint returns 503 immediately → wait one
   health interval → snapshot and close sockets. The balancer stops sending new
   connections before the instance stops serving them.

Verification:

```bash
for d in 1 2 3 4; do printf 'doc %s  ' $d; for i in 1 2 3; do
  T=$(curl -s -X POST localhost:9000/ws-ticket -H "Authorization: Bearer $TOKEN_A" | jq -r .ticket)
  websocat -v "ws://localhost:9000/ws?doc=$d&ticket=$T" 2>&1 </dev/null | grep -o 'X-Doc-Instance: [^ ]*' | tr '\n' ' '
done; echo; done
# => doc 1  doc-9001 ×3    doc 2  doc-9011 ×3   ... never mixed

for i in 1 2 3 4; do curl -si localhost:9000/documents -H "Authorization: Bearer $TOKEN_A" | grep X-Doc-Instance; done
# => 9001 9011 9001 9011

# membership removal reaches the live session on the right instance
# (B connected to doc 1 on doc-9001; A removes B) => B's socket closes 4003 within 1 s

kill -9 $(lsof -ti :9011); sleep 5
curl -s localhost:9000/health/backends -H "Authorization: Bearer $TOKEN_ADMIN" | jq -c .doc_service
# doc hashed to 9011 => 503; doc hashed to 9001 => fine; /documents => 200 always
```

Exit criteria: no document is ever observed on two instances; the ring tests
pass; a killed instance's documents come back on the same instance.

### Month 4 — January 2027 — Caching (Redis) + Rate Limiting *(planned)*

Goal: Redis enters for the two concepts that need a shared store across N
instances. Both were done in `notification-api`; both are re-done here in the
form the multi-instance system forces.

**Caching** — `GET /documents` (the per-user list) is the hot read: the editor
polls it, and it joins `document_members` with `documents`. Cache in Redis:

```
doclist:<user_id>   value = JSON of the first page   TTL = 300 s
```

- Read-through on the round-robined list endpoint; write-through invalidation
  (`DEL`) on: document create/delete/rename (the owner's key and every member's),
  member add/remove (that member's key), and on **every snapshot** (because the
  list is sorted by `updated_at`, and `updated_at` moves when the snapshot
  lands — not on every op). The lesson: pick the granularity at which the cached
  view is *allowed* to be stale, and name it. Here: a document's position in the
  list may lag its last edit by up to `SNAPSHOT_EVERY_SECONDS`.
- Why Redis and not in-process: the instance that changes a membership
  (hash-routed on the doc) is not the instance that serves the next list read
  (round-robined). An in-process cache would need a cross-instance invalidation
  bus, which is a message queue in disguise. A shared cache moves the problem to
  one `DEL`.
- Cache miss stampede is *not* handled (single-flight would be the fix); named as a
  trade-off. Cache **the empty list** too (negative caching), otherwise a new user
  with no documents is a database read per poll.
- `presence` is deliberately not in Redis (see Architecture); the ticket store
  stays in-memory in the single gateway.

**Rate limiting** — three limiters, three shapes:

| Where | Key | Algorithm | Limit | On exceed |
|---|---|---|---|---|
| gateway, all authenticated HTTP | `user_id` (IP for public routes) | **sliding window log** in Redis, one Lua script (`ZADD`/`ZREMRANGEBYSCORE`/`ZCARD`/`EXPIRE` atomically) | 120/min | `429` + `Retry-After` |
| gateway, `POST /login` | `email` + IP | fixed window (INCR/EXPIRE, the notification-api port) | 5/min | `429`; the failure is the same for wrong password and rate-limited to avoid an enumeration oracle |
| doc-service, WebSocket `op` frames | per connection | **token bucket** in memory (`golang.org/x/time/rate`) | 20/s, burst 40 | `error{code:"rate_limited"}`; three in 10 s → close 4429 |
| doc-service, WebSocket `op` frames | `user_id` across connections/instances | Redis INCR per minute | 600/min | close 4429 |

- Sliding window vs the predecessor's fixed window: a fixed 60 s window admits 2×
  the limit across a boundary; the log shape does not, at the cost of O(limit)
  memory per key. Measure and write both numbers down.
- Why the WebSocket limiter is in-process: it is per connection, the connection is
  on one instance, and a Redis round trip per keystroke would put Redis on the
  ack path. The per-user Redis counter exists for the case the in-process one
  cannot see — the same user on ten connections across instances.
- `cursor` frames are dropped over 20/s silently rather than counted: they are
  cosmetic, and a client that sends too many loses cursor fidelity, not access.

Verification:

```bash
# cache
curl -s localhost:9000/documents -H "Authorization: Bearer $TOKEN_A" >/dev/null
docker exec docs_redis redis-cli --scan --pattern 'doclist:*'          # doclist:1
curl -s -X PUT localhost:9000/documents/$DOC/members/2 ... -d '{"role":"editor"}'
docker exec docs_redis redis-cli EXISTS doclist:2                       # 0 — invalidated on the other instance
# query log or a counter on /health: N list requests, 1 database read

# rate limit
for i in $(seq 1 130); do curl -s -o /dev/null -w '%{http_code}\n' localhost:9000/me -H "Authorization: Bearer $TOKEN_A"; done | sort | uniq -c
# => 120 × 200, 10 × 429
websocat ... then paste 100 op frames in one go => rate_limited errors, then close 4429
for i in $(seq 1 6); do curl -s -X POST localhost:9000/login -H "$JSON" -d '{"email":"a@test.com","password":"wrong"}' -w ' %{http_code}\n'; done
# => 5 × 401, then 429
```

Exit criteria: list reads hit Redis; invalidation crosses instances; all four
limiters demonstrably fire; Redis down → limiters **fail open** with a log line
(availability over strictness for a learning system — named as a trade-off) and
the cache is bypassed.

### Month 5 — February 2027 — Webhooks *(planned)*

Goal: document events reach a user's registered endpoint, signed, retried, and
**never lost between the database write and the delivery**. The chat project's
`notification-service` is the base; the transactional outbox is the new part.

Scope:

1. **Outbox in doc-service** (`docs_service_db.outbox`): membership add/remove,
   publish (m6) and title change write their event row **in the same transaction**
   as the change. `document.updated` is different: it is written by the
   *snapshotter*, at most once per document per snapshot, with the payload
   `{doc_id, version, editors:[user ids since last snapshot]}`. That is the
   coalescing: keystroke-rate edits become at most one webhook per 30 s / 100 ops
   per document. Naming the granularity again — the same move as the cache.
2. **Outbox relay**: a goroutine in every doc-service instance polls
   `SELECT ... FROM outbox WHERE processed_at IS NULL ORDER BY id LIMIT 50 FOR
   UPDATE SKIP LOCKED` every `OUTBOX_POLL_MS=500`, POSTs each to
   notification-service `/events` with `X-Notification-Key`, marks
   `processed_at` on `202`. `SKIP LOCKED` is what lets N instances poll the same
   table without double-sending; the `event_id` unique key downstream is the
   backstop if they ever do. A crash after the POST and before the mark → the
   same event is posted again → `202` no-op. At-least-once, made idempotent.
3. **notification-service**: port from chat (`webhook_service.go`, `signer.go`,
   `ssrf.go`, `deliverer.go`, `fanout_service.go`) with these changes:
   - subscriptions carry `event_type`; fan-out = subscribers of `(doc_id,
     event_type)` ∪ `(doc_id, '*')`, minus the actor, keeping those with an
     endpoint. There is no "who is online" filter — presence is not a service here.
   - `document.shared` is additionally delivered to the **invitee** if they have an
     endpoint, whether or not they subscribed (they could not have — they were not
     a member).
   - **Durable retries**: `deliveries.next_attempt_at` is the schedule; a delivery
     worker pool polls due rows (`SKIP LOCKED` again) instead of holding retries
     in memory with `time.Sleep`. Backoff 1 s, 2 s, 4 s, 8 s, 16 s with ±50%
     jitter, 5 attempts, `4xx` final, `5xx`/transport retried. A crash mid-backoff
     loses nothing — the row says when to try next. This answers chat's known
     trade-off "the delivery queue is in memory".
   - Signature unchanged from chat: `X-Signature: sha256=HMAC(secret,
     "<X-Timestamp>.<body>")`, `X-Event-Id`, `X-Event-Type` headers. `Sign` and
     `Verify` side by side.
   - SSRF filter unchanged: string check at registration, dial-time check in the
     transport, loopback only with `WEBHOOK_ALLOW_LOOPBACK=true` for the local
     receiver.
4. Gateway proxies `/webhook`, `/subscriptions`, `/deliveries` to
   notification-service with edge auth (single instance, plain reverse proxy).

Verification:

```bash
# receiver on :9900 that verifies the signature (Go snippet in chat README, month 4)
curl -s -X PUT localhost:9000/webhook -H "$JSON" -H "Authorization: Bearer $TOKEN_B" -d '{"url":"http://localhost:9900/hook"}' | jq
curl -s -X POST localhost:9000/subscriptions -H "$JSON" -H "Authorization: Bearer $TOKEN_B" -d "{\"doc_id\":$DOC,\"event_type\":\"*\"}"

# A types 300 characters over a minute => receiver sees ~2-3 document.updated, not 300
# A renames => document.updated? no: 'document.renamed' is not an event; title rides the next document.updated
# A removes B => B receives document.unshared (last one B will get for this doc)
curl -s localhost:9000/deliveries -H "Authorization: Bearer $TOKEN_B" | jq -c '.[] | {event_type,status,attempts}'

# durability: receiver answers 503; kill -9 notification-service between attempts; restart
# => the row's next_attempt_at is honoured, attempts continue, final status delivered
# outbox: kill -9 doc-service right after an outbox insert => processed by the OTHER instance's relay
docker exec -i docs_mysql mysql -uroot -proot -e 'SELECT COUNT(*) FROM docs_service_db.outbox WHERE processed_at IS NULL'   # 0
```

Exit criteria: no event without a delivery row; no delivery lost across a crash;
coalescing measured; SSRF cases refused.

### Month 6 — March 2027 — CDN *(planned)*

Goal: published documents and their images served through an edge cache with both
cache disciplines — immutable-forever and mutable-with-purge — and the project
hardened to a stopping point.

Scope:

1. **publish-service**: `POST /assets` (content-addressed image store, type sniffed
   from bytes, 5 MiB — media-service ported), `POST /render` (goldmark Markdown →
   HTML wrapped in a minimal template with the title; stored at
   `sha256(html)`; `publications` upsert; then `DELETE /edge/p/:slug` at the
   gateway), origin reads `/p/:slug` and `/assets/:sha` with the headers in the
   API table. Image references in documents are Markdown
   `![alt](/assets/<sha>)`; the editor's image button uploads and inserts that.
2. **Gateway edge cache** (`internal/edgecache`, ported from chat: byte-bounded LRU,
   per-object cap, single-flight coalescing on miss, `BYPASS` for uncacheable
   responses, `X-Cache` header) extended with the mutable case:
   - `/assets/:sha` — exactly as chat: cache forever, `304` from the URL alone.
   - `/p/:slug` — cached with the origin's `max-age=60`; after expiry the edge
     **revalidates** with `If-None-Match` (origin answers `304` cheaply from the
     `publications` row) rather than refetching; `X-Cache: REVALIDATED` when that
     happens. And **purge**: `DELETE /edge/p/:slug` evicts immediately, so a
     republish is visible on the next request, not in 60 s. The purge is
     best-effort (the gateway may be down; the TTL is the backstop) — which is
     exactly how real CDN purges behave and why the TTL is short.
   - `/` (editor.html) served through the same cache with `max-age=60` and an
     ETag of the file's hash — the static asset case.
   - The cache **never** stores a response to a request carrying `Authorization`
     or a `Set-Cookie`/`Cache-Control: private` response. Written as a test.
3. `GET /health/edge-cache` gains `purges` and `revalidations`.
4. Rate limit `POST /documents/:id/publish` to 6/min per document (render is the
   most expensive request in the system).
5. **Hardening / stopping point**: Dockerfile per service and a full
   `docker-compose.yml` (all four services, two doc-service replicas, MySQL,
   Redis) so the whole system starts with one command; graceful shutdown audited in
   every service (drain order documented); `go vet` and `-race` in a `make check`;
   a final pass over `Known trade-offs`.

Verification:

```bash
SHA=$(curl -s -X POST localhost:9000/documents/$DOC/assets -H "Authorization: Bearer $TOKEN_A" -F file=@diagram.png | jq -r .sha)
curl -si localhost:9000/assets/$SHA | grep -E 'X-Cache|Cache-Control'     # MISS, immutable
curl -si localhost:9000/assets/$SHA | grep X-Cache                        # HIT

curl -s -X POST localhost:9000/documents/$DOC/publish -H "$JSON" -H "Authorization: Bearer $TOKEN_A" -d '{"slug":"design-notes"}' | jq
curl -si localhost:9000/p/design-notes | grep -E 'X-Cache|ETag|Cache-Control'   # MISS, max-age=60
curl -si localhost:9000/p/design-notes | grep X-Cache                           # HIT
# edit + republish => the very next GET is MISS with the new ETag (purge worked)
# stop the gateway's purge endpoint (simulate) => the new version appears within 60 s (TTL backstop)
sleep 61; curl -si localhost:9000/p/design-notes | grep X-Cache                 # REVALIDATED
grep '"path":"/p/design-notes"' publish-service.log | wc -l                      # 2: one fetch, one 304
curl -si localhost:9000/p/design-notes -H "Authorization: Bearer $TOKEN_A" | grep X-Cache   # BYPASS

docker compose up --build   # the whole thing, then the month 1 smoke test end to end
```

Exit criteria: one origin fetch per version of a page, however many reads;
purge observable; `docker compose up` brings up all nine concepts.

## Conventions carried forward

Each of these was earned in a predecessor; the reason is the point, not the rule.

- **No default for any secret.** A binary rolled ahead of its config exits at
  startup, loudly. A predictable default would be a forgeable token.
- **Shared secret, not shared code.** No common Go module across services; each
  copies what it needs. Drift between copies is the honest cost of independent
  deployability. (This project has fewer copies to begin with because of edge auth.)
- **Database per service; no cross-schema foreign keys.** Same container,
  separate schemas, integrity enforced by the token that carried the id.
- **Authentication at the edge, authorization at the resource owner.** New here,
  decided in month 1 (see *Predecessor lessons*).
- **Constant-time comparison for every shared key** (`crypto/subtle`).
- **Nothing blocks a single-goroutine hot loop.** The session's writer, the outbox
  relay, and every outbound HTTP call sit behind a bounded queue. The one
  deliberate exception is documented above: the session applies backpressure to
  sockets rather than dropping edits.
- **`TIMESTAMP(3)`/`DATETIME(3)` and an `id` tiebreak on every paginated order.**
  Second-resolution ties make `LIMIT/OFFSET` non-deterministic.
- **`docker-entrypoint-initdb.d` runs only on an empty volume**; schema files are
  self-contained so they can be applied to a live one.
- **3× rule for any heartbeat/TTL pair** (interval × 3 ≤ TTL) — two misses tolerated.
- **Every proxied request carries a `request_id` visible on both sides**, and the
  answering instance names itself in a response header.
- **Logs record the path, never the query string** (tickets and tokens have been
  in query strings before).
- **A missing `Origin` header on a WebSocket upgrade is allowed**; a wrong one is
  refused. Cross-site WebSocket hijacking is a browser attack and browsers always
  send `Origin`.
- **Tests that need a live dependency skip when it is absent** (`t.Skip`), so
  `go test ./... -race` runs anywhere.
- **Commits are `Month N: <summary>`**, one per month, plus `Address review
  findings: …` follow-ups. Review is the pre-push panel; its findings are addressed,
  not suppressed.
- **The README is updated in the same commit as the code it describes**, and the
  "planned" text is replaced, not appended to — the reader should see what *is*,
  with the roadmap's original intent kept only where the outcome differed and the
  difference is the lesson.

## Reference implementations

Copy and adapt; do **not** import across repositories. Paths relative to `~`.

| Need | Source | What changes here |
|---|---|---|
| JWT issue/validate, admin middleware, password validation | `notification-api/internal/middleware/jwt.go`, `admin.go`, `internal/validation/validation.go`, `internal/service/user_service.go` | validation moves to the gateway only; backends read headers |
| Fixed-window Redis rate limiter | `notification-api/internal/middleware/rate_limiter.go` | keep for `/login`; sliding-window Lua for the general limiter |
| Redis cache service + invalidation on event | `notification-api/internal/service/cache_service.go`, `event_service.go` | invalidation is cross-instance and keyed per user |
| Paginated repository with limit/offset | `notification-api/internal/repository/notification_repository.go` | add `(3)` timestamps and id tiebreak (chat's fix) |
| Trusted proxy: identity headers, strip Authorization | `realtime-chat-platform/gateway/internal/proxy/trusted_proxy.go` | used for **every** backend from month 1; add `X-User-Role` |
| Reverse proxy + balanced proxy | `realtime-chat-platform/gateway/internal/proxy/reverse_proxy.go`, `balanced_proxy.go` | per-route strategy also for `/documents/:id` HTTP |
| Round-robin, consistent hash, health pool | `realtime-chat-platform/gateway/internal/loadbalancer/*.go` + tests | key is `doc` not `room` |
| WebSocket ticket exchange | `realtime-chat-platform/gateway/internal/service/ticket_service.go`, `internal/middleware/ws_ticket.go`, `controller/ticket_controller.go` | unchanged |
| WS client pumps, hub/room goroutine pattern, slow-consumer eviction | `realtime-chat-platform/chat-service/internal/ws/client.go`, `room.go`, `hub.go`, `handler.go` | `Room` becomes `Session`; broadcast becomes transform+apply+persist+ack |
| Bounded non-blocking dispatch queues | `realtime-chat-platform/chat-service/internal/dispatch/dispatcher.go` | outbox relay and delivery workers |
| Webhook signing, SSRF-safe transport, deliverer with jittered backoff, fan-out | `realtime-chat-platform/notification-service/internal/service/signer.go`, `ssrf.go`, `deliverer.go`, `fanout_service.go`, `webhook_service.go` | retries become durable rows; no presence filter; `event_type` |
| Content-addressed file store, byte sniffing, immutable headers | `realtime-chat-platform/media-service/internal/storage/store.go`, `controller/media_controller.go` | lives in publish-service; plus HTML render |
| Edge cache: byte-bounded LRU, coalescing, BYPASS, `X-Cache` | `realtime-chat-platform/gateway/internal/edgecache/cache.go`, `handler.go` | add revalidation and purge |
| Instance id validation, `X-*-Instance` header on 101 | `realtime-chat-platform/chat-service/internal/middleware/instance.go`, `ws/handler.go` | rename |
| Compose file with sibling-safe ports and initdb notes | `realtime-chat-platform/docker-compose.yml`, `db/*.sql` | 3308/6381, four schemas |
| Original roadmap format (Turkish, month-by-month) | `notification-api/Notification API (1).docx` | this README replaces it |
| Concept definitions | `Downloads/Backend System Design Concepts.docx` | reproduced above |

OT references (public): the ot.js `TextOperation`/`Client` design (apply,
compose, transform, the three-state client) is what the spec above follows; the
"log order wins ties" rule is the ShareDB/`ot-text` convention. Read those two if a
transform case is unclear; do not import them.

## Running locally

Valid as of month 1; later months add processes as listed in their sections.

```bash
# 1. MySQL (four schemas, two exist in month 1) + Redis
docker compose up -d

# 2. doc-service — one instance in months 1-2, two from month 3
cd doc-service && GATEWAY_SHARED_KEY=dev-gateway-key SERVER_PORT=9001 go run ./cmd/doc
cd doc-service && GATEWAY_SHARED_KEY=dev-gateway-key SERVER_PORT=9011 go run ./cmd/doc    # month 3+

# 3. notification-service (month 5+)
cd notification-service && GATEWAY_SHARED_KEY=dev-gateway-key NOTIFICATION_API_KEY=dev-notification-key \
  WEBHOOK_ALLOW_LOOPBACK=true go run ./cmd/notification

# 4. publish-service (month 6+)
cd publish-service && GATEWAY_SHARED_KEY=dev-gateway-key PUBLISH_API_KEY=dev-publish-key go run ./cmd/publish

# 5. gateway
cd gateway && JWT_SECRET=my-secret-key GATEWAY_SHARED_KEY=dev-gateway-key \
  DOC_SERVICE_URLS=http://localhost:9001,http://localhost:9011 go run ./cmd/gateway

# then open http://localhost:9000/ in two browsers
# (or run everything unattended: ./scripts/e2e.sh)
```

### Configuration

| Service | Variable | Default | Month |
|---|---|---|---|
| gateway | `SERVER_PORT` | `9000` | 1 |
| gateway | `JWT_SECRET` | *required* | 1 |
| gateway | `GATEWAY_SHARED_KEY` | *required* | 1 |
| gateway | `DB_DSN` | `root:root@tcp(localhost:3308)/docs_gateway_db?parseTime=true` | 1 |
| gateway | `DOC_SERVICE_URLS` | `http://localhost:9001` (comma-separated) | 1 (list from 3) |
| gateway | `NOTIFICATION_SERVICE_URL` | `http://localhost:9003` | 5 |
| gateway | `PUBLISH_SERVICE_URL` | `http://localhost:9004` | 6 |
| gateway | `STATIC_DIR` | `../static` | 1 |
| gateway | `LB_HEALTH_INTERVAL_SECONDS` | `5` | 3 |
| gateway | `REDIS_ADDR` | `localhost:6381` | 4 |
| gateway | `RATE_LIMIT_PER_MINUTE` | `120` | 4 |
| gateway | `LOGIN_LIMIT_PER_MINUTE` | `5` | 4 |
| gateway | `EDGE_CACHE_MAX_BYTES` | `67108864` | 6 |
| gateway | `EDGE_CACHE_MAX_OBJECT_BYTES` | `5242880` | 6 |
| doc-service | `SERVER_PORT` | `9001` | 1 |
| doc-service | `INSTANCE_ID` | `doc-<SERVER_PORT>` | 3 |
| doc-service | `GATEWAY_SHARED_KEY` | *required* | 1 |
| doc-service | `DB_DSN` | `root:root@tcp(localhost:3308)/docs_service_db?parseTime=true` | 1 |
| doc-service | `WS_ALLOWED_ORIGINS` | `http://localhost:9000` | 2 |
| doc-service | `DOC_MAX_CODEPOINTS`, `OP_MAX_BYTES`, `OP_RING_SIZE`, `SNAPSHOT_EVERY_OPS`, `SNAPSHOT_EVERY_SECONDS`, `SESSION_IDLE_SECONDS` | see Limits | 2 |
| doc-service | `REDIS_ADDR` | `localhost:6381` | 4 |
| doc-service | `DOCLIST_CACHE_TTL_SECONDS` | `300` | 4 |
| doc-service | `WS_OPS_PER_SECOND`, `WS_OPS_BURST`, `WS_OPS_PER_MINUTE_PER_USER` | `20`, `40`, `600` | 4 |
| doc-service | `NOTIFICATION_SERVICE_URL`, `NOTIFICATION_API_KEY`, `OUTBOX_POLL_MS` | `http://localhost:9003`, *required*, `500` | 5 |
| doc-service | `PUBLISH_SERVICE_URL`, `PUBLISH_API_KEY` | `http://localhost:9004`, *required* | 6 |
| notification-service | `SERVER_PORT`, `DB_DSN`, `GATEWAY_SHARED_KEY`, `NOTIFICATION_API_KEY`, `DELIVERY_WORKERS`, `WEBHOOK_ALLOW_LOOPBACK` | `9003`, …, *required*, *required*, `4`, `false` | 5 |
| publish-service | `SERVER_PORT`, `DB_DSN`, `GATEWAY_SHARED_KEY`, `PUBLISH_API_KEY`, `PUBLISH_DIR`, `MAX_UPLOAD_BYTES`, `GATEWAY_URL` (for purge) | `9004`, …, *required*, *required*, `./data`, `5242880`, `http://localhost:9000` | 6 |

### Applying a schema to an existing volume

```bash
docker exec -i docs_mysql mysql -uroot -proot < db/doc_schema.sql          # non-destructive
docker compose down -v && docker compose up -d                             # or full reset
```

### Tests

```bash
make test                      # go test ./gateway/... ./doc-service/... -race (database tests skip)
make test-db                   # same, with DOCS_TEST_DSN pointing at the compose MySQL (permission matrix runs)
make e2e                       # ./scripts/e2e.sh: both services + real HTTP + log assertions
make check                     # vet + test-db + e2e
go test ./doc-service/internal/session/ -race -count=1             # session: fake store + fake clients, incl. 2 000 random stale ops folded against the log (200 with -short)
go test ./doc-service/internal/ot/ -race -count=1                  # OT table tests + 10 000-iteration compose/TP1 properties (1 000 with -short)
go test ./doc-service/... -race -run 'TestCompose|TestTransform' -count=200
go test ./doc-service/internal/ot/ -run XXX -fuzz=FuzzTransform -fuzztime=30s   # open-ended; FuzzCompose likewise
```

`make` on macOS needs the Xcode license accepted once (`sudo xcodebuild -license accept`)
or `brew install make`; the targets are plain commands and can be run by hand.

## Known trade-offs and explicit non-goals

To be revised each month. Starting list, decided up front, plus what month 1 added
(marked *m1*):

- *m1* **Admin is granted by SQL, not by an endpoint.** `UPDATE users SET role='admin'`
  is the only way; a bootstrap-admin flow is product work. Tokens carry the role
  for 24 h, so a revoked admin keeps the claim until expiry (no token blacklist).
- *m1* **Sharing takes a raw user id and does not check that it exists.** The users
  table is in another schema owned by another deployable; the editor has no user
  search. A typo shares with nobody, harmlessly.
- *m1* **No CSRF concern by construction, no CORS at all.** The editor and the API
  share one origin and the token is a header, never a cookie. A second origin would
  need a CORS policy the project does not have.
- *m1* **Test users and the e2e run share the dev database.** The e2e script uses
  unique emails per run and deletes what it created; a crash mid-run can leave
  `e2e-*` rows behind. `docker compose down -v` is the reset.

- **Plain text only.** No rich text, no undo/redo, no comments. OT over
  attributed text is a different (bigger) algorithm; the system-design lessons do
  not need it.
- **No offline editing.** A reconnecting client with unacknowledged local ops
  drops them. Making that safe needs client-side op persistence and server-side
  replay, which is a product feature, not a concept on the list.
- **A dead instance takes its documents with it** until it is back (fail-closed
  sticky routing, as in chat). The "real" fix is a shared op log (Redis Streams
  or a queue) so any instance can host any document; that is distributed
  consensus in disguise and is deliberately not built.
- **Instance membership is static** (`DOC_SERVICE_URLS` at startup). Service
  discovery is a different lesson.
- **Health is polled, not observed** — a request in the gap between an instance
  dying and the next poll gets a `502`.
- **Single gateway.** The ticket store and the edge cache are per process. Two
  gateways would need Redis for tickets and would be two independent edges (which
  is what a CDN is, but without an origin shield).
- **Cache miss stampede is unhandled** on the document list. Single-flight per key
  is the known fix.
- **Rate limiters fail open when Redis is down.** Availability over strictness;
  the in-process WebSocket limiter still holds.
- **Webhook secrets are stored in the clear** (signing needs the value).
- **The op log is never pruned.** History is cheap at this scale; a retention job
  would be operational work, not a concept.
- **`document.updated` granularity is the snapshot interval.** An integration
  that wants every keystroke should be a WebSocket client, not a webhook.
- **Persist-before-ack costs a database round trip per op batch.** The alternative
  (ack first, persist async) was rejected because it can acknowledge an edit the
  database never receives.

## Predecessor lessons applied here

From `realtime-chat-platform`'s `Known trade-offs`, and what this project does
about each:

| Chat project trade-off | Here |
|---|---|
| Three copies of `jwt.go`, two services with none | **Resolved by construction**: edge auth from month 1; no backend validates a JWT |
| The delivery queue is in memory; a crash loses queued deliveries | **Resolved**: transactional outbox in doc-service + durable `next_attempt_at` retries in notification-service (month 5) |
| The edge cache has no purge; invalid content is not a thing | **Extended**: mutable `/p/:slug` with purge and revalidation alongside immutable assets (month 6) |
| No rate limiting on typing frames | **Resolved**: per-connection token bucket on ops, cursors dropped over rate (month 4) |
| Presence is eventually consistent; same-user-two-sockets flicker | **Sidestepped**: presence is per-session in-memory and exact, because documents are sticky |
| A dead instance takes its rooms with it | **Kept**, with the same reasoning and a stronger argument (the op log's primary key) |
| Instance membership static; health polled | **Kept** |
| Webhook secrets in the clear | **Kept** |
| Edge cache per gateway process | **Kept** |

From `notification-api`: fixed-window limiter → sliding window (month 4);
`time.Sleep` retry loop → durable schedule (month 5); cache invalidation on a
single process → cross-instance invalidation (month 4); hardcoded secrets and DSN
→ required environment variables, no defaults (month 1).
