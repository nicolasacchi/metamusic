# Hosted connectors (claude.ai, ChatGPT)

A hosted connector lets a cloud assistant use the same MCP tools as a personal access token, signed in
through your own OpenID Connect provider. Examples use `mm.example.com` (the app) and `auth.example.com`
(the provider); replace them with yours.

## How it works

- The app is the OAuth **resource** at `https://mm.example.com/mcp`. It never issues OAuth tokens itself.
- The provider issues RFC 9068 JWT access tokens (RS256) whose audience is the MCP URL, with the custom
  scope `metamusic` and the claim `mm_user` (the username).
- The app accepts a token only if the signature, issuer, audience and expiry verify, the scope is present,
  `mm_user` is the allow-listed user, and the OAuth client id is listed in `MM_OIDC_CLIENTS`.
- A connector is an **agent** principal. Its scopes come from its **grant** (created on first use with
  `read,write`), never from the token. `generate` and every spend limit are set on `/human/connectors`
  (or by an agent whose token has the `admin` scope). The global monthly cap stays human-only, so a
  connector budget can never exceed it.
- Every call is audited and the first connection sends a "Connector connected" push.

## Provider setup

Start from `deploy/authelia-oidc.example.yml` (Authelia 4.39 key names; nothing in it is secret). It defines
the claims policy and the `metamusic` scope, an owner-only authorization policy requiring two factors, and one
public client for claude.ai. A hosted client needs only a client id; no secret is shared with the app.

Then configure the app (see the README table):

```yaml
MM_PUBLIC_URL: https://mm.example.com        # must be https
MM_TRUSTED_USERS: owner
MM_OIDC_ISSUER: https://auth.example.com
MM_OIDC_CLIENTS: metamusic-claude=Claude
```

`MM_OIDC_CLIENTS` is an allow-list: a valid token from any other client is refused with `CONNECTOR_FORBIDDEN`.
`MM_OIDC_SCOPES` defaults to `openid metamusic`. If a hosted client needs a refresh token to survive the next
day, set `MM_OIDC_SCOPES="openid offline_access metamusic"` and repeat the sign-in (device checklist DC-M4a-2).

### Network reachability

The provider's discovery document, token endpoint and JWKS must be reachable

- from the public internet (the hosted client calls them), and
- from inside the app container (the app fetches the signing keys; `MM_OIDC_JWKS_URL` overrides the discovered
  location if it differs from the public one).

The app prefetches the keys at start-up and refreshes them on key-id misses.

## Adding the connector

- **claude.ai** (web and mobile): Settings, Connectors, add a custom connector with the MCP URL
  `https://mm.example.com/mcp` and the client id (leave the secret empty). Sign in through the provider and
  accept the consent page. The provider shows the consent page on every authorization; that is expected.
- **ChatGPT**: use a predefined (confidential) client with `client_secret_post`; the same MCP URL.

Ask the assistant to "search my tracks for X" (`search_tracks`) and to add a comment (`add_comment`). The comment
shows the client label as its author.

## Verifying with `oidc probe`

```bash
metamusic oidc probe --server https://mm.example.com --client-id metamusic-probe
```

The probe runs the real authorization-code flow (you paste the redirected URL), prints a redacted claims report
(never a token) and finishes with one `POST /mcp`, which must answer **403 `CONNECTOR_FORBIDDEN`**: the probe's
client is deliberately not in `MM_OIDC_CLIENTS`, so that answer proves the server verified signature, issuer,
audience and expiry without creating a grant.

## Troubleshooting

| Symptom | Meaning |
| --- | --- |
| 401 `UNAUTHENTICATED` / `invalid_token` | Token malformed, expired, or issuer, audience or keys do not match the provider. The client restarts OAuth. |
| 403 `INSUFFICIENT_SCOPE` | The token lacks the `metamusic` scope; check the client's scopes and claims policy. |
| 403 `CONNECTOR_FORBIDDEN` | Client id not in `MM_OIDC_CLIENTS`, or `mm_user` is not the trusted user. |
| 403 `CONNECTOR_REVOKED` | The grant was revoked on `/human/connectors`; restore it there. |
| 503 `UNAVAILABLE` (`oidc`) | The app cannot reach the signing keys; `/human/connectors` shows `keys.last_error`. |

Every refusal logs one line with the status and problem code (never the token); refusals at the middleware write
no audit row.

## Proxy-mode step-up (`MM_STEPUP_OIDC_CLIENT`)

In `MM_AUTH=proxy` the second factor for `/human/` (caps, arming, publish, confirm) is normally the edge's
`two_factor` access rule alone. With `MM_STEPUP_OIDC_CLIENT` set, the app also requires it itself: every `/human/`
call needs a recent step-up, otherwise `/human/api/*` answers 403 `STEP_UP_REQUIRED` and pages redirect to
`/auth/step-up`. Unset, nothing changes, so the code can be deployed before the client exists. Agents, devices and
local mode are unaffected.

How it works: `/auth/step-up` starts an authorization-code flow with PKCE (S256), `state` and `nonce` at the issuer
(`MM_OIDC_ISSUER`) with `prompt=login` and `max_age=300`. The callback `/auth/step-up/callback` exchanges the code and
accepts the ID token only if the RS256 signature (issuer keys), `iss`, `aud`, `nonce`, `exp`/`iat`, `auth_time` (at most
5 minutes old), a second-factor `amr` (`mfa`, `otp`, `hwk` or `swk`) and `preferred_username` (equal to the edge's
`Remote-User`) all check out. It then sets an HttpOnly cookie bound to that user, valid for 10 minutes. The SPA's
second-tab flow (`whoami.step_up_until`, `/auth/step-up/done`, the `mm-stepup` channel) works as in local mode.

App configuration:

```yaml
MM_AUTH: proxy
MM_OIDC_ISSUER: https://auth.example.com       # already required for connectors
MM_OIDC_CLIENTS: metamusic-claude=Claude       # unchanged
MM_STEPUP_OIDC_CLIENT: metamusic-stepup
# secrets dir: stepup_client_secret (optional, 0600; absent = public PKCE client)
```

`metamusic config check` prints `step-up: oidc client ... (secret set|not set, public client)` or `step-up: edge only`.
Roll out in this order: register the client below, restart Authelia, create the secret file, set the variable, run
`config check`, restart the app, open `/human/` and complete the prompt.

Authelia client to register (also `deploy/authelia-stepup.example.yml`):

```yaml
identity_providers:
  oidc:
    claims_policies:
      metamusic_stepup:
        id_token: ['preferred_username']
    clients:
      - client_id: 'metamusic-stepup'
        client_name: 'metamusic step-up'
        client_secret: '$pbkdf2-sha512$...'   # digest of the value in the stepup_client_secret file
        public: false
        authorization_policy: 'two_factor'
        consent_mode: 'implicit'
        claims_policy: 'metamusic_stepup'
        require_pkce: true
        pkce_challenge_method: 'S256'
        redirect_uris:
          - 'https://mm.example.com/auth/step-up/callback'
        scopes: ['openid', 'profile']
        response_types: ['code']
        response_modes: ['query']
        grant_types: ['authorization_code']
        token_endpoint_auth_method: 'client_secret_basic'
        id_token_signed_response_alg: 'RS256'
```

Notes: `authorization_policy: 'two_factor'` makes Authelia demand both factors for this client (use the example's
`metamusic_owner` policy instead to also restrict it to the owner). The claims policy is needed because Authelia
keeps `preferred_username` out of the ID token by default; `amr` and `auth_time` are standard ID token claims
there. Without a secret file the client must be `public: true` with `token_endpoint_auth_method: 'none'` and no
`client_secret`. The app refuses the step-up (403, a warning with a short code in the log, never a token) when any
check fails; the provider must be reachable from the app container, like the connector keys.
