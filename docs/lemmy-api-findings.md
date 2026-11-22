# Lemmy API v3 and v4 findings

Status of each finding is marked **Confirmed** (from the generated types in
[lemmy-js-client](https://github.com/LemmyNet/lemmy-js-client), which are produced from the
Lemmy server source) or **Unverified** (expected from Lemmy's behavior, but not yet observed on a
real instance). Verify the unverified rows against real instances before 08b and 10
depend on them, then update this file.

## Shapes used by the client

The [v4 upgrade guide](https://join-lemmy.org/docs/contributors/09-api-v4.html)
identifies API v3 with Lemmy 0.19 and API v4 with Lemmy 1.0. The client
probes v4's site endpoint first and falls back to v3 only on HTTP 404 or 405.
It checks the returned version before sending credentials. Versions below 0.19
are not supported.

| Purpose | v3 (0.19) | v4 (1.0+) |
|---|---|---|
| Version | `GET /api/v3/site`, top-level `version` | `GET /api/v4/site`, top-level `version` |
| Login | `POST /api/v3/user/login` | `POST /api/v4/account/auth/login` |
| Follows | `GET /api/v3/site`, `my_user.follows` | `GET /api/v4/account`, `follows` |
| Community | `GET /api/v3/community?name=` | `GET /api/v4/community?name=` |

Both logins accept `username_or_email` and `password`, and return `jwt`.
Authenticated requests use a bearer token. Follows contain `community` objects;
the qualified `name@instance` uses the host of v3's `actor_id` or v4's `ap_id`.
Community lookup returns `community_view.community.id`; both `name` and
`name@instance` are documented by the
[v3 source](https://github.com/LemmyNet/lemmy/blob/0.19.12/crates/api_common/src/community.rs).
See the v3 [site](https://github.com/LemmyNet/lemmy/blob/0.19.12/crates/api_common/src/site.rs),
[login](https://github.com/LemmyNet/lemmy/blob/0.19.12/crates/api_common/src/person.rs),
and [community](https://github.com/LemmyNet/lemmy/blob/0.19.12/crates/db_schema/src/source/community.rs)
types. A public, unauthenticated probe of lemmy.world on 2026-11-20 returned
404 for `/api/v4/site` and version `0.19.21-8-g081ada9cf` from `/api/v3/site`.
The user reported a successful login and community suggestions on their
pre-1.0 instance. The exact version and authenticated error behavior were not
reported and remain to be verified.

## Behavior to confirm on a real instance

The client maps these as follows. Error responses are JSON `{"error": "<code>"}`; the client keeps
only the code, and only if it is lowercase letters, digits and underscores.

| Situation | Expected | Client result | Status |
|---|---|---|---|
| Wrong password | `incorrect_login` (HTTP 400 or 401) | `ErrBadCredentials` | Unverified |
| TOTP-required account | `missing_totp_token` (or `incorrect_totp_token`) | `ErrTwoFactorUnsupported` | Unverified |
| Unverified email, pending registration, banned or deleted account | an error code such as `email_not_verified` or `registration_application_is_pending`, or no `jwt` | `ErrAccountUnusable` | Unverified (exact codes) |
| Unsupported instance | Both site endpoints are absent, or the reported version is unsupported for the selected API | `ErrUnsupportedVersion` | Tested with fake instances |
| Unknown community | 404 or `couldnt_find_community` | `ErrCommunityNotFound` | Unverified |
| Rate limiting | HTTP 429, possibly with `Retry-After` | `RateLimitedError` (delay parsed from `Retry-After` in seconds, else 0) | Unverified (headers) |
| Expired or invalid token | HTTP 401, or code `not_logged_in` | re-login once, retry once | Unverified |
| Timeout | none; the client gives up after 10 seconds | `ErrUnreachable` | By design |

Handoffs 08b (publishing) and 10 (image upload) should record here how `POST /post` and
`POST /image` report their errors, such as a locked community, a banned user, a rate limit and an
oversized image.

## Native scheduling

Lemmy v4 `CreatePost` has a `scheduled_publish_time_at` field. This project deliberately does not
use it (see the roadmap decision); nothing in the research above changes that.
