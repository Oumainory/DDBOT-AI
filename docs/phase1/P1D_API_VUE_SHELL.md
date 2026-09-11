# P1D — API + Vue Dashboard Shell

状态：**P1D DONE / CLOSED；P1E READY**。

P1D provides the first complete browser management loop without introducing
Source, Target, Subscription, Connector, Credential CRUD, or AI runtime:

```text
Setup → Login → Session restore → Overview / About → Logout
```

## API route table

| Route | Auth | Contract |
| --- | --- | --- |
| `POST /api/v2/setup` | Setup Token + required Origin | One-time admin bootstrap |
| `GET /api/v2/setup/status` | Public | `setup_required` or `ready` |
| `POST /api/v2/auth/login` | Origin optional by default | HttpOnly Session Cookie + CSRF token |
| `GET /api/v2/auth/session` | Cookie optional | Session bootstrap; unauthenticated is `200` |
| `POST /api/v2/auth/logout` | Session + Origin + CSRF | Revoke session and clear cookie |
| `GET /api/v2/platform/overview` | Authenticated | Non-sensitive platform read model |
| `GET /api/v2/about` | Authenticated | Product, build, AGPL and source metadata |
| `/healthz`, `/readyz` | Public | Existing platform probes |

All new API responses use the P1B envelope. Unknown `/api/v2` paths return a
JSON 404 and never receive the SPA index. Legacy `/api/v1` and `/api/debug`
routes remain registered in `admin` and are not rewritten.

## Backend boundaries

`internal/adminapi` is the authoritative `/api/v2` router. It delegates the
frozen Setup/Login/Session/Logout handlers to `internal/adminauth`, and adds a
reusable `RequireAuth` middleware. The middleware hashes and validates the
HttpOnly session cookie through the existing Auth Service and places only
non-raw session metadata in a request context. `RequireMutation` composes
Session + required Origin + `X-CSRF-Token` for future authenticated commands;
Setup and Login retain their independent P1B contracts.

Overview is a stable DTO, not a copy of health JSON. It reports only real
signals from the existing Probe plus the supplied Legacy online signal:

```json
{
  "product": {"name": "DDBOT-AI", "version": "dev", "commit": "unknown"},
  "platform": {
    "legacy_core": {"status": "available", "code": "legacy_online"},
    "admin_api": {"status": "available", "code": "admin_api_ready"},
    "sqlite": {"status": "available", "code": "sqlite_alive"},
    "secret_store": {"status": "recovery", "code": "secret_store_recovery"},
    "auth": {"status": "available", "code": "auth_ready"}
  }
}
```

`internal/buildinfo` uses explicit `-ldflags` metadata when supplied and falls
back to Go VCS build settings. It never hard-codes a historical commit or
exposes environment variables. About displays `DDBOT-AI`, version, commit,
build time, GNU AGPL-3.0, and the repository URL; a commit link is emitted
only for a valid hexadecimal SHA.

## Vue shell

The frontend lives in `web/` and is locked with npm and `package-lock.json`.
It uses Vue 3, TypeScript, Vite, Element Plus, Pinia and Vue Router. The API
client uses relative same-origin `fetch` with `credentials: "same-origin"`;
the browser never reads the Session Cookie. Pinia stores only authentication
state, username, CSRF token and expiry. Passwords and Setup Tokens remain
component-local and are cleared after submission.

Router bootstrap calls setup status and session before navigation. The guards
route first-run users to `/setup`, unauthenticated users to `/login`, and
authenticated users to `/overview`; `/about` is authenticated. A 401 clears
the in-memory auth state and returns to Login. Logout sends the in-memory CSRF
token, then clears state even if the server has already expired the session.

The shell contains only Overview and About navigation. Overview shows the
five platform cards and explicitly identifies the current Platform Foundation
scope. Secret Store Recovery is visible as a stable `Recovery` status; it does
not expose why the store entered Recovery or any secret material.

## Static serving and runtime

`web` builds deterministically into `internal/webui/dist`. The Go package
embeds that committed artifact with `embed.FS`; the production binary therefore
needs no Node, npm, Vite or dev server. Node is a build/CI dependency only.

The static handler serves real assets with their normal MIME types and gives
hashed `/assets/` files immutable caching. `index.html` is `no-store`.
Only `/`, `/setup`, `/login`, `/overview`, and `/about` fall back to
`index.html`; missing assets return 404. `/api/*`, `/healthz`, and `/readyz`
are explicitly excluded from fallback. Security headers include
`nosniff`, `no-referrer`, and `X-Frame-Options: DENY`.

Dashboard availability is independent of `/readyz`: a Secret Store Recovery
state may make readiness return 503 while the authenticated Dashboard remains
available to show the degraded state. Platform initialization remains
fail-open for Legacy acquisition, BuntDB, templates and OneBot delivery.

## Build and scope gates

```sh
cd web
npm ci
npm run typecheck
npm run test
npm run build
```

CI verifies that the build does not drift from `internal/webui/dist`, then
runs the existing Go, adapter, Compatibility 21/21 and three-target
`CGO_ENABLED=0` gates. P1D does not add migrations or modify migrations
001–005 and does not enter Source/Target/Subscription, Credential, Connector,
AI, Shadow or ENFORCE runtime.
