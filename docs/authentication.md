# Authentication

Autotaggerr authenticates in three ways, all of which end at the same place: a signed session
token minted by `auth.IssueToken`. Middleware, handlers and the SPA never care how you logged in.

| Method | Used by | Credential |
|---|---|---|
| Password | The UI login form | Username + bcrypt hash on `models.User` |
| OIDC | The UI "Continue with …" buttons | An external identity provider |
| API key | Scripts, automation | `X-Api-Key` header, per user |

**Password login always stays available.** There is no way to disable it, so a broken OIDC
provider can never lock you out of your own instance.

## Architecture

`auth/` deliberately separates *authentication* ("who is this?") from *session issuance* ("prove
it later"):

- `authenticateLocal` (password) and `CompleteLogin` + `ResolveUser` (OIDC) each resolve a
  `models.User`.
- `IssueToken` mints the JWT. Adding another method means adding a resolver, not touching
  middleware, the SPA, or the token format.

## OIDC setup

Providers are configured in the UI under **Login providers**, or via `/api/v1/auth-providers`.
Configuration lives in the database, not `config.json`.

1. At your identity provider, create an OAuth2/OIDC **confidential client** (authorization code
   flow). Note the issuer URL, client ID and client secret.
2. Register the callback URL:
   `https://<your-autotaggerr>/api/v1/auth/oidc/<provider-id>/callback`
   The provider ID is generated on save, so add the provider first, then copy the callback shown
   in the edit form. Set **Redirect URL override** only if a proxy rewrites the path.
3. Fill in the issuer — the base URL, *without* `/.well-known/openid-configuration`. It must be
   `https` (localhost excepted). Discovery is cached in memory and reset whenever a provider is
   saved or deleted.
4. Choose how accounts are matched (below), then enable the provider. The login page picks it up
   with no restart.

Scopes default to `openid profile email`.

### Account matching

On each login, `auth.ResolveUser` tries, in order:

1. **The `(provider, subject)` link.** OIDC `sub` is immutable at the IdP, so a linked account is
   still found after the user changes their email or username there.
2. **A verified email address.** An existing local account with the same email is adopted and the
   link recorded, so later logins take path 1.
3. **Signup**, if *Create an account on first sign-in* is enabled for that provider.

If none match, the login is refused.

**Unverified emails never match an existing account.** If the IdP does not assert
`email_verified`, matching on the address would let anyone who can set their own email at the
provider take over a local account. Turn signup off (the default) on a private instance: only
people who already have an account, or whose verified email matches one, get in.

### What the login flow verifies

`GET /auth/oidc/:id/start` issues a short-lived (10 min), signed, `HttpOnly`, `SameSite=Lax`
cookie holding the state, nonce and PKCE verifier, then redirects to the provider. Nothing is
stored server-side, so a restart mid-login fails closed rather than half-succeeding.

`GET /auth/oidc/:id/callback` refuses the login unless *all* of these hold:

- the flow cookie is present, correctly signed, and unexpired;
- it was issued for this provider;
- the returned `state` matches the cookie (this is the CSRF defence);
- the code exchange succeeds, with the PKCE verifier;
- the response contains an ID token;
- the ID token's signature, issuer, audience and expiry verify against the provider's JWKS;
- the ID token's nonce matches the one this flow issued.

Failures redirect to `/login?error=…` with a deliberately vague message; the specific reason is
logged server-side only.

On success the session token comes back in the URL **fragment** (`/login#token=…`). Fragments are
never sent to servers, so the token stays out of access logs and `Referer` headers; the SPA
consumes it and strips it from the address bar immediately (`consumeTokenFromUrl`).

## Known gaps

- **No refresh tokens.** Sessions last `auth.DefaultTokenTTL` (7 days) and then require a fresh
  login. The provider's refresh token is discarded after the exchange.
- **No single logout.** Logging out clears the local token only; the IdP session is untouched, so
  "Continue with …" may sign you straight back in.
- **Roles are not mapped from claims.** Auto-created users get the provider's default role;
  group/role claims are ignored.
