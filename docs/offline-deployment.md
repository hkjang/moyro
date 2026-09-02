# moyro Offline Deployment Guide

This guide describes how to install the v0.2.11 moyro service image in a network
with no internet access after its release archive is published. The
archive contains the application and web UI. It does not contain PostgreSQL; a
reachable PostgreSQL service must already exist inside the target network.

## Release artifact contract

For the `v0.2.11` release tag:

- Docker image: `moyro:v0.2.11`
- Downloaded file: `moyro-v0.2.11.tar.gz`
- Supported platform for the initial release: `linux/amd64`

After publication, transfer the archive through the organization's approved
media and integrity process. Compare it with the SHA-256 in the release notes
before loading the image:

```bash
sha256sum moyro-v0.2.11.tar.gz
```

Load it without contacting a registry:

```bash
docker load --input moyro-v0.2.11.tar.gz
docker image inspect moyro:v0.2.11
```

## PostgreSQL preparation

Create a dedicated database and least-privilege login. PostgreSQL traffic must
remain inside the protected network. Back up the database before every moyro
upgrade; uploaded files require a separate volume backup.

The account needs permission to create and alter moyro's tables during schema
migration. Startup applies immutable, checksummed migrations under a PostgreSQL
advisory lock and records them in `schema_migrations`; checksum drift or an
unknown future version stops startup. Production deployments should eventually
use a separate migration role and a narrower runtime role.

## Required environment file

moyro reads exactly four application settings from the process environment:

```dotenv
POSTGRES_DSN=postgres://moyro:replace-me@postgres.internal:5432/moyro?sslmode=require
BOOTSTRAP_ADMIN=admin@example.internal
BOOTSTRAP_ADMIN_PASSWORD=replace-with-12-to-72-byte-secret
ENCRYPTION_KEY=replace-with-standard-base64-for-32-random-bytes
```

Generate the encryption key on a trusted administration workstation:

```bash
openssl rand -base64 32
```

Store the file outside the application data volume and restrict it to the
service operator:

```bash
chmod 600 /etc/moyro/moyro.env
```

`BOOTSTRAP_ADMIN_PASSWORD` must be 12–72 bytes and is consumed only when the
bootstrap administrator is first created. A restart must not reset an existing
password. Keep the environment file protected nevertheless: container
administrators can inspect runtime environment values.

`ENCRYPTION_KEY` protects persisted secrets and signing material. The current
release has no online root-key re-encryption workflow: changing or losing this
value makes encrypted settings and credentials unreadable. Keep it fixed for
the instance and back it up separately from PostgreSQL.

## Run the service

```bash
docker volume create moyro-data

docker run -d \
  --name moyro \
  --restart unless-stopped \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --env-file /etc/moyro/moyro.env \
  --mount type=volume,src=moyro-data,dst=/var/lib/moyro \
  --publish 8065:8065 \
  moyro:v0.2.11
```

Check startup and open `http://<server>:8065/`:

```bash
curl --fail http://127.0.0.1:8065/healthz
docker logs --tail 100 moyro
```

Terminate TLS at an approved internal reverse proxy. After the first local
login, configure the canonical site URL, Keycloak OIDC, AI provider, approval,
key, MCP, and browser-draft policy in the service administrator page. Drafts
can use bounded local retention, session-only storage, or no browser storage;
logout cleanup is controlled by the same site policy. Those values are stored
in PostgreSQL; provider secrets are encrypted before storage.

v0.2.11 ignores forwarding headers by default. Add only the reverse proxy's
explicit IP CIDRs to **Trusted proxy CIDRs** after configuring that proxy to
remove client-supplied `Forwarded` and `X-Forwarded-For` values. A request's
immediate TCP peer must match one of those CIDRs before moyro resolves the
original client from the chain from right to left or accepts forwarded
scheme/host information. Audit addresses and rate limits then use that resolved
client IP. Login start, provider callback, and browser session exchange have
separate limits so one stage cannot consume another stage's capacity.

Internal outgoing webhooks use an explicit host allow-list. Add only the
required callback hostname or IP in site settings; do not include a scheme,
port, or path in the allow-list entry.

The Mattermost-compatible `GET /api/v4/config` endpoint is an operational
snapshot, not a second configuration store. Its legacy `PUT /config`,
`PUT /config/patch`, and `POST /config/reload` mutations return a 501
Mattermost AppError in v0.2.11. SMTP, S3, Redis, and link-preview toggles
therefore cannot appear to save successfully. Runtime plugin lifecycle is a
separate supported native workflow: an administrator with `manage_plugins`
can upload, replace, enable, disable, configure, and delete reviewed
Mattermost-style `.tar.gz` archives from **Integrations → Plugins**. Use the
native administrator pages for the PostgreSQL-backed site, Keycloak, AI,
approval, key, MCP, and plugin settings that this release supports.
The application uses local file storage, no Redis fan-out, no SMTP delivery,
and no outbound link-preview fetching by default.

The native product API contract is published separately from compatibility
routes: [`openapi-moyro.yaml`](openapi-moyro.yaml) covers `/api/moyro/v1`, while
[`openapi-v4.yaml`](openapi-v4.yaml) covers the Mattermost-compatible `/api/v4`.

Run exactly one moyro application container in v0.2.11. PostgreSQL may be
managed separately, but multi-replica application deployment and cross-node
live-setting propagation are not supported until the HA/Redis work is
completed.

Any native executable uploaded at runtime or provisioned under
`/var/lib/moyro/plugins` is fully trusted code, not a sandboxed extension. It
shares the service UID, process namespace, volume, and network; install only
reviewed operator-approved archives. Moyro does not verify plugin signatures.
Archive validation and the minimal plugin command environment are hygiene and
do not provide secret isolation. Botman 0.1.2, Chatdump 0.5.1, Langflow
0.1.20, and EchoSummary 0.6.5 define the local release-gate boundary. Public
CI uses checksum-pinned public archives and currently exercises EchoSummary
0.6.4. Their functional tests cover only the scenarios documented in
[Plugin System](plugin-system.md); arbitrary Mattermost plugins, versions, and
unlisted workflows are not implied compatible.

## Keycloak in an offline network

The Keycloak discovery, token, and JWKS endpoints must be reachable from the
moyro container over the internal network, while a user's browser must be able
to reach the advertised authorization endpoint. Save the canonical public Site
URL first, then configure the realm issuer (or its exact
`.well-known/openid-configuration` URL), client ID, and client secret in the
administrator page. Discovery endpoints may use different HTTP(S) origins for
front-channel and back-channel traffic. An HTTPS issuer's HTTP token, JWKS, and
UserInfo endpoints are rejected by default. Enable **Allow HTTP back-channel on
a trusted private network** only when those endpoints are confined to an
isolated network: the client secret and authorization code can cross that HTTP
connection in plaintext, and traffic including JWKS can be intercepted or
modified. The browser-facing authorization endpoint remains HTTPS-only
regardless of this setting. The connection check fetches and parses both
discovery and the JWKS signing keys, but only a real test login can verify
the client secret and redirect registration. Add the redirect URI shown in
moyro to the Keycloak client. If Keycloak uses a private certificate authority,
install its CA through the supported administrator setting rather than
modifying the container by hand.

The provider callback returns a five-minute, browser-bound handoff code rather
than a reusable Moyro session token. Its exchange sets the reusable credential
only in an HttpOnly, SameSite browser cookie and returns the local user without
exposing a token to JavaScript. If a successful response is lost, the same
browser can retry for 60 seconds and receives the exact same session. The web
client automatically retries transient network and 5xx failures twice; a
terminal failure offers an explicit SSO restart instead of looping. A subsequent
`/api/v4/users/me` request uses the cookie, while API clients continue to use
Bearer tokens.

OIDC validation depends on accurate clocks. Provide internal NTP to both
Keycloak and the moyro host.

## Backup and restore

Back up both state locations as one recovery set:

1. PostgreSQL database, using the organization's `pg_dump` or physical backup
   procedure.
2. The `moyro-data` Docker volume, which contains local uploaded files and
   plugin material.
3. The protected `ENCRYPTION_KEY`, stored separately from the data backup.

Test restoration on an isolated host. A database restored without the matching
file volume can retain message metadata while losing attachments.

## Upgrade and rollback

1. Back up PostgreSQL, `moyro-data`, and the environment file.
2. Load the new `moyro-v<version>.tar.gz` archive.
3. Stop the existing container.
4. Start the new tag with the same environment file and volume.
5. Verify `/healthz`, the login page version, administrator login, file access,
   WebSocket delivery, durable activity state, task/decision source links, and
   Keycloak login.

v0.2.4 added migration `000011_login_handoffs`, which stores only SHA-256
digests of short-lived SSO codes and browser bindings. v0.2.5 added
`000012_sso_handoff_idempotency`, recording the completed session and exchange
time needed for the bounded response-loss recovery window. v0.2.11 adds
`000013_work_management`, `000014_knowledge_documents`, and
`000015_collaboration_controls` for durable automation/work lifecycles,
source-watermarked documents, inbox rules, and restricted guests. Startup
applies them through the checksummed migration ledger and transactions. Keep
the pre-upgrade database backup because an image-only rollback is not a
supported way to remove an applied migration.

Do not mix an earlier pre-moyro development build with a v0.2.11 node. Upgrade
the service as one coordinated operation and verify the matching database
backup before discarding the prior container.

Keep the previous image and backup until the new release is accepted. Database
schema changes may prevent a safe image-only rollback; use the release notes
and restore the matching database backup when a downgrade is not supported.

## Reproduce the release acceptance test

On a connected staging host, preload `postgres:16-alpine` and run the same
offline check used by the release workflow:

```bash
bash scripts/verify-release.sh moyro:v0.2.11 moyro-v0.2.11.tar.gz
```

The script loads the archive, creates an internal-only Docker network, starts
PostgreSQL and moyro with the four variables, checks the web UI and reported
version, logs in as the bootstrap administrator, restarts the container, and
logs in again. Temporary containers, network, and volume are removed on exit.

## Air-gap acceptance checklist

- The service starts on an internal-only Docker network with no registry or
  internet access.
- The root page and hashed React assets return successfully.
- `/api/v4/config/client` reports the expected release version.
- Bootstrap login works before and after a container restart.
- Only the four documented moyro application variables are present.
- Keycloak and AI remain inactive until an administrator configures them;
  outgoing webhook callbacks are denied while the host allow-list is empty.
- Site policy changes are reflected by the public draft capability, and local,
  session-only, disabled, expiry, and logout-cleanup behavior match that policy.
- Mention/DM/thread, approval, reminder, task-assignment, and supported plugin
  activity survives restart with owner and current-channel scope intact.
- Conversation-derived tasks and decisions survive restart, retain source links,
  and do not become visible outside creator/assignee/channel membership rules.
- SMTP delivery, S3 storage, Redis fan-out, and outbound link previews remain
  explicitly unsupported in v0.2.11 rather than reporting a false successful
  save. Runtime plugin upload and lifecycle management are supported only for
  reviewed Trusted Native archives through the native plugin page/API.
- Network controls independently restrict container egress to approved internal
  endpoints; do not infer a blanket no-egress guarantee from feature defaults.
- PostgreSQL and file-volume restore has been exercised.
