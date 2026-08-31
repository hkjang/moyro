# moyro 구현·검증 체크리스트

이 문서는 [제품·기술 사양](moyro-product-spec.md)을 구현하기 위해 작성한 **계획 당시 체크리스트**이자 현재 진행 상태표다. 2026-08-29 공개한 `v0.1.1`을 다시 감사해, 소스 구현과 **최종 tag source 및 공개 자산에 대해** 자동·수동 검증이 확인된 항목만 체크한다. 이전 source의 Docker 이미지 기반 브라우저·격리망 결과는 현재 릴리스의 통과 증거로 재사용하지 않았다.

`v0.1.1`의 Go 전체 race·vet·reachable vulnerability 검사, Go Plugin SDK, web 의존성 감사·typecheck·production build, PostgreSQL 15·16 실 E2E가 통과했다. 최종 tag Docker 이미지와 GitHub Release에서 재다운로드한 이미지 모두 격리망 검증을 통과했으며, 보안 경계와 핵심 계약을 포함한 Chromium 27개 E2E도 통과했다. 공개 자산으로 25개 제품 화면을 다시 캡처했고 GitHub Pages 공개 파일까지 원본 해시와 대조했다.

## Phase 0 — 기준선과 이름

- [x] 기존 dirty worktree와 무관한 로컬 생성물을 보존하고 release 변경만 추적 후보로 분리한다.
- [x] Go module, binary, package, cookie, metric, plugin protocol, 문서 표시명을 `moyro`로 통일한다.
- [x] build info를 단일 package에서 제공하고 `Version`, `Commit`, `BuildDate`를 ldflags로 주입한다.
- [x] 동명 `.js`/`.ts(x)` source를 정리해 TypeScript를 단일 원본으로 만든다.
- [x] `go test ./...`, `npm run typecheck`, `npm run build` 기준선을 통과한다.

## Phase 1 — 안전한 부트와 설정

- [x] 애플리케이션 env를 네 개로 제한하고 누락·형식 오류 시 fail closed한다.
- [x] AES-256-GCM/HKDF secure store와 wrong-key sentinel test를 추가한다.
- [x] bootstrap admin을 advisory lock + transaction + marker로 생성한다.
- [x] 첫 사용자와 SSO 사용자의 자동 관리자 승격을 제거한다.
- [x] DB settings catalog, revision, secret redaction, 감사 로그를 구현한다.
- [x] 지원 대상 관리자 설정 API와 hot reload 가능한 service snapshot을 구현한다.

## Phase 2 — 인증·권한·키

- [x] session 존재와 active user를 HTTP/WebSocket 양쪽에서 검증한다.
- [x] DB role/permission과 공통 Authorizer를 구현한다.
- [x] scoped 개인 API/MCP key 생성·조회·폐기·회전·grace를 구현한다.
- [x] key 권한을 owner 현재 권한과 교집합으로 평가한다.
- [ ] 마지막 관리자와 자기 잠금 방지 test를 추가한다.

## Phase 3 — Keycloak OIDC

- [x] issuer 또는 discovery 문서 URL, client ID/client secret 기반 discovery와 callback URL 자동 구성을 구현한다.
- [x] bounded discovery/JWKS probe, cross-origin back-channel, HTTPS downgrade 방어를 구현한다.
- [x] Code + PKCE, state, nonce, ID token signature·issuer·audience/azp·at_hash 검증을 구현한다.
- [x] username/email claim mapping, 가입 정책, 내부 CA와 connection test를 제공한다.
- [x] 설정 변경을 재기동 없이 적용하고 secret을 redaction한다.
- [x] 로그인 화면은 활성 provider만 표시한다.

## Phase 4 — UI 구조와 접근성

- [x] MUI theme/provider와 React Router nested layout을 적용한다.
- [x] workspace, admin, personal settings를 독립 route/layout으로 분리한다.
- [x] 팀·채널·메뉴를 URL에서 복원하고 SPA fallback을 제공한다.
- [x] 로그인과 profile menu에 동일한 서버 version을 표시한다.
- [x] 최소 글꼴·hit target과 관리자/profile scrollbar 기준을 적용한다.
- [x] Keycloak, AI, key, role, MCP, approval 관리자 페이지를 실제 API에 연결한다.

## Phase 5 — AI

- [x] encrypted provider 설정과 OpenAI-compatible client를 구현한다.
- [x] SSE streaming 기본값, chunk별 flush와 client cancellation을 검증한다.
- [ ] 장시간 무응답 upstream을 위한 heartbeat 정책과 회귀 test를 추가한다.
- [x] context/output limit `1..262144`와 provider별 effective limit을 검증한다.
- [x] 개인 AI preference와 허용 model 범위를 구현한다.
- [x] prompt/response 평문을 감사 로그에 남기지 않는다.

## Phase 6 — 승인과 MCP

- [x] 정책 없음/비활성 direct-execution path를 먼저 고정한다.
- [x] 정책, request, decision, outbox와 reviewer permission을 구현한다.
- [x] approve/reject, 1인 quorum, 자기 승인 금지, 별도 team lead 검토와 idempotency test를 추가한다.
- [x] Streamable HTTP MCP initialize/resources/tools/call을 구현한다.
- [x] MCP와 native API가 동일 authz/approval/audit를 사용하게 한다.
- [x] MCP 설정과 개인 연결 정보를 UI에 제공한다.

## Phase 7 — 실시간 보안과 호환 회귀

- [x] WebSocket channel/team event를 membership 기준으로 filter한다.
- [x] private channel 교차 사용자 negative test를 추가한다.
- [x] logout 직후 HTTP 401과 기존 WebSocket 강제 종료를 실제 브라우저·서버에서 검증한다.
- [ ] 관리자 revoke와 inactive 전환을 한 E2E 안에서 HTTP·WebSocket 모두 검증한다.
- [x] 로그인→채널→게시→thread→검색과 주요 제품 route의 browser E2E를 추가한다.
- [x] 핵심 `/api/v4` route의 성공·잘못된 body·401·403·404·pagination 계약을 검증한다.

## Phase 8 — 오프라인 image와 release

- [x] React와 Go를 포함한 non-root multi-stage Docker image 정의를 만든다.
- [x] `/var/lib/moyro` data volume과 `/healthz` endpoint를 최종 source의 실제 image에서 검증한다.
- [x] 격리 network에서 외부 접근 없이 bootstrap/login/UI/restart를 최종 image로 검증한다.
- [x] 최종 source image를 `moyro:v<version>`으로 build한다.
- [x] `moyro-v<version>.tar.gz`로 save/gzip하고 다시 load해 검사한다.
- [x] tag workflow가 GitHub Release에 해당 asset 하나만 첨부한다.
- [x] backup/restore, upgrade/rollback, ENCRYPTION_KEY 보관 절차를 문서화한다.

## Phase 9 — v0.1.1 구조 안정화 릴리스

- [x] 전체 `schema.sql` 재실행을 checksum·적용 이력이 있는 번호형 migration으로 전환한다.
- [x] REST·MCP·예약·Incoming Webhook·Slash Command 메시지 쓰기를 공통 Post Command로 통합한다.
- [x] 예약 메시지에 lease·claim token·재시도 상태와 `scheduled_post_id` 기반 중복 방지를 추가한다.
- [x] session 조회를 JWT 원문 대신 HMAC 처리한 `jti` hash 우선 방식으로 전환하고 rolling upgrade 호환 경로를 둔다.
- [x] SMTP 미설정 시 digest worker를 시작하지 않고 capability로 실제 지원 상태를 노출한다.
- [x] Outgoing Webhook 작업을 PostgreSQL delivery outbox·재시도·dead 상태로 내구화한다.
- [x] 대형 HTTP handler를 분리하고 source size budget을 CI에서 검사한다.
- [x] 관리자·개인 설정 route를 지연 로딩하고 초기 web bundle을 분리한다.
- [x] 제품·API·가이드·홍보 페이지 표시 버전을 `v0.1.1`로 맞추고 OpenAPI 계약을 갱신한다.
- [x] 현재 최종 source로 25개 전체 화면 캡처를 다시 생성하고 27개 Chromium 제품 E2E를 통과한다.
- [x] PostgreSQL 15·16에서 migration·session·예약 lease·Webhook delivery 통합 테스트를 통과한다.
- [x] 최종 tag commit metadata로 `linux/amd64` non-root image와 단일 gzip archive를 만들고 내부 전용 network에서 재검증한다.
- [x] `main`과 `v0.1.1` tag를 push하고 GitHub CI·Release·Pages 배포 성공을 확인한다.
- [x] GitHub Release가 `moyro-v0.1.1.tar.gz` 자산 하나만 제공하는지 재다운로드해 검증한다.

## 최종 완료 조건

- [x] 현재 저장소가 제공하는 모든 자동 검증이 최종 local release-candidate run에서 통과한다.
- [x] 최종 image의 제품 E2E 대상 화면에 console error와 허용하지 않은 failed network request가 없다.
- [x] SMTP·S3·Redis·link preview·runtime plugin 등 문서화한 미지원 관리 mutation은 501 AppError를 반환하고 거짓 저장 성공을 내지 않는다.
- [x] 최종 local release candidate archive의 image label, tag, version API와 화면 version 일치를 검증한다.
- [x] GitHub remote, release 권한, 첫 release tag를 확인한 뒤에만 실제 배포한다.

## v0.1.1 배포 기록

- 소스: [커밋 `122bfc169013c215d3b8f049e77753f2e76c2440`](https://github.com/hkjang/moyro/commit/122bfc169013c215d3b8f049e77753f2e76c2440), `main`, tag `v0.1.1`
- CI: [전체 검증과 PostgreSQL 15·16](https://github.com/hkjang/moyro/actions/runs/33237576608) 성공
- 홍보·가이드·25개 화면: [GitHub Pages](https://hkjang.github.io/moyro/)
- 릴리스: [moyro v0.1.1](https://github.com/hkjang/moyro/releases/tag/v0.1.1)
- 사용자 지정 릴리스 자산: `moyro-v0.1.1.tar.gz` 1개, 16,319,898 bytes
- Docker 이미지 태그: `moyro:v0.1.1`
- SHA-256: `972f2af4b4ee63c5883ea51ddc06a5d0711bc3abff394baf68d0764d289cef68`
- 원격 자산을 재다운로드한 뒤 내부 전용 Docker network에서 기동, UI, 버전, bootstrap 로그인, 재시작을 검증했다.
- 재다운로드한 이미지로 Chromium 27개 E2E와 최종 제품 화면 25개를 다시 생성했고, `v0.1.1`과 build `122bfc16` 표시를 확인했다.

## v0.1.0 배포 기록

- 소스: [hkjang/moyro](https://github.com/hkjang/moyro), `main` 커밋 `4fdd5a1fa038c1d070934b648146a41705abb461`
- 홍보·가이드: [GitHub Pages](https://hkjang.github.io/moyro/)
- 릴리스: [moyro v0.1.0](https://github.com/hkjang/moyro/releases/tag/v0.1.0)
- 사용자 지정 릴리스 자산: `moyro-v0.1.0.tar.gz` 1개
- Docker 이미지 태그: `moyro:v0.1.0`
- SHA-256: `270208156bc12213cf37ca73d343b1c56cb70c34184044ec86dba0ec159d90d4`
- 원격 자산 재다운로드 후 격리 네트워크에서 기동, UI, 버전, bootstrap 로그인, 재시작을 검증했다.
