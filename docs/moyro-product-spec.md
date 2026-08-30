# moyro 제품·기술 사양

> 상태: 구현 기준선
> 작성일: 2026-08-30
> 제품명: `moyro`

## 1. 제품 정의

moyro는 Mattermost의 공개 REST API v4, WebSocket 이벤트, 봇·웹훅·슬래시 명령과 익숙한 채널 중심 작업 흐름을 계승하면서, 내부는 Go·React·PostgreSQL 기반의 작은 모듈형 모놀리스로 유지하는 자체 운영형 협업 플랫폼이다. 제품 경험의 핵심 문장은 **“대화를 읽고, 결정하고, 실행하는 하나의 업무 공간”**이며, Mattermost 호환성은 진입 경계로 유지하고 사용자 화면은 Moyro Flow 정보 구조를 사용한다.

제품의 우선순위는 다음과 같다.

1. 조직 내부망에서 외부 네트워크 없이 설치·기동·업그레이드할 수 있어야 한다.
2. 인증, 비밀 설정, 사용자 키, 관리자 권한의 경계가 운영 환경에서 검증 가능해야 한다.
3. Mattermost 호환성은 경로 개수가 아니라 실제 사용자·클라이언트 workflow와 오류·권한 계약으로 판단한다.
4. 서비스 관리와 개인 설정을 분리하고, 화면 URL이 상태의 기준이 되도록 한다.
5. AI, MCP, 승인 workflow는 공통 권한·감사 체계를 사용한다.
6. 화면은 API가 실제로 반환한 데이터와 오류만 표시하고, 아직 영속 모델이 없는 작업·결정이나 운영 지표를 성공 상태처럼 만들지 않는다.

## 2. 배포 계약

### 2.1 애플리케이션 환경변수

moyro 프로세스가 직접 읽는 환경변수는 정확히 네 개다.

| 이름 | 필수 | 계약 |
| --- | --- | --- |
| `POSTGRES_DSN` | 예 | PostgreSQL 연결 DSN. 기본값을 제공하지 않는다. |
| `BOOTSTRAP_ADMIN` | 예 | 최초 시스템 관리자 이메일. 최초 bootstrap 이후에는 계정 암호를 덮어쓰지 않는다. |
| `BOOTSTRAP_ADMIN_PASSWORD` | 예 | 최초 관리자 생성에만 사용하며 로그·감사 payload·DB 평문에 남기지 않는다. |
| `ENCRYPTION_KEY` | 예 | 정확히 32바이트를 base64로 인코딩한 root key. JWT 서명과 DB 비밀 암호화용 하위 키를 분리 파생한다. |

HTTP listen 주소 `:8065`, 로컬 파일 저장소 `/var/lib/moyro/files`, 플러그인 디렉터리 `/var/lib/moyro/plugins`, 웹 정적 자산 위치는 이미지 계약으로 고정한다. v0.2.0에서 OIDC, AI, webhook outbound allowlist는 관리자 설정 전까지 비활성이다. SMTP, Redis, S3와 outbound link preview는 이 릴리스의 운영 범위에 포함하지 않으며 호환 API에서도 거짓 성공을 반환하지 않는다. 검토한 Mattermost 형식 `.tar.gz`의 runtime 업로드·교체·활성화·비활성화·설정·삭제는 지원한다.

v0.2.0의 지원 배포 단위는 PostgreSQL에 연결된 moyro 애플리케이션 컨테이너 **한 개**다. 관리자 설정의 DB commit과 live snapshot 전환은 이 프로세스 안에서 직렬화한다. 여러 애플리케이션 replica, Redis fan-out, HA 설정 전파는 후속 범위이며 이 릴리스에서 지원한다고 주장하지 않는다.

### 2.2 이미지와 릴리스

- 단일 Linux 서비스 이미지가 Go API와 React 정적 웹앱을 함께 제공한다.
- 컨테이너는 non-root 사용자로 실행한다.
- 이미지 태그는 `moyro:v<semver>` 형식이다.
- GitHub Release에 직접 첨부하는 산출물은 `moyro-v<semver>.tar.gz` 하나이며, 내용은 `docker save`로 만든 이미지다.
- 대상 내부망에는 별도 PostgreSQL이 준비되어 있어야 한다.
- release gate는 저장한 이미지를 다시 `docker load`한 뒤 격리된 Docker network에서 bootstrap, 로그인, UI, health, 재기동을 검증한다.

## 3. 설정과 비밀 관리

v0.2.0이 지원하는 운영 설정은 관리 페이지와 DB-backed API로 관리한다. 공개 설정, 일반 설정, 비밀 설정을 명시적으로 구분한다.

- 일반 설정은 JSON 값과 revision을 저장한다. 현재 role permission처럼
  revision을 입력받는 변경 표면은 optimistic concurrency를 강제하며,
  다른 설정 표면은 단일 애플리케이션 프로세스 안에서 변경을 직렬화한다.
- 비밀은 AES-256-GCM으로 암호화하고 API 응답에는 값 대신 `configured` 상태만 보낸다.
- 설정 section과 key로 구성한 row identity를 AEAD AAD에 결합해 다른 설정 row로 ciphertext를 옮겨도 복호화되지 않게 한다.
- provider의 공개 JSON과 새 비밀은 같은 PostgreSQL transaction으로 저장한 뒤 검증된 live snapshot으로 전환한다.
- 지원되는 설정 변경은 감사 이벤트를 남긴다.
- root `ENCRYPTION_KEY`는 관리 페이지에서 변경하지 않는다. v0.2.0에는 온라인 rewrap 절차가 없으므로 인스턴스 수명 동안 고정하고 별도로 백업한다.
- Site URL, 조직명, 가입 정책, outbound webhook host allowlist, Keycloak, AI, MCP, 키 정책, 승인 정책과 Trusted Native 플러그인 lifecycle을 관리자 설정 catalog에 포함한다. SMTP·S3·Redis는 v0.2.0 미지원이며 저장 가능한 것처럼 표시하지 않는다.

## 4. 인증과 권한

### 4.1 Bootstrap 관리자

- PostgreSQL advisory lock과 transaction으로 단 한 명만 생성한다.
- bootstrap marker가 기록된 뒤 재기동해도 비밀번호를 재설정하지 않는다.
- 첫 회원가입자·첫 로그인 사용자·첫 SSO 사용자의 자동 관리자 승격은 금지한다.
- bootstrap 관리자는 기본 팀과 채널에 가입한다.

### 4.2 세션

- JWT는 `ENCRYPTION_KEY`에서 용도별로 파생한 서명 키를 사용한다.
- HTTP와 WebSocket 요청 모두 JWT 유효성, DB session 존재, 만료, 사용자 활성 상태를 확인한다.
- logout 또는 관리자 session 폐기 후 같은 JWT를 즉시 거부한다.
- 장기적으로 DB에는 raw JWT 대신 JTI 또는 token hash만 보존한다.

### 4.3 Keycloak OIDC

관리자는 먼저 canonical Site URL을 저장한 뒤 최소 `Issuer URL`, `Client ID`, `Client Secret`을 입력한다. moyro는 discovery로 endpoint와 JWKS를 구성하며 callback URL을 Site URL에서 계산한다. 활성화된 동안 Site URL을 비울 수 없고, 저장된 검증 callback은 재기동 뒤에도 유지된다.

- Authorization Code + PKCE S256
- state와 nonce 검증
- ID token signature, issuer, audience/azp, expiry 검증
- 기본 scope `openid profile email`
- username과 email claim mapping
- 계정 자동 생성, 이메일 확인 정책, 내부 CA 설정
- 연결 테스트 후 활성화, 재기동 없는 설정 반영
- 서로 다른 issuer의 동일 subject가 충돌하지 않도록 identity key에 provider와 issuer를 포함

### 4.4 변경 가능한 RBAC

권한을 문자열 상수 비교가 아니라 DB 역할과 permission 연결로 평가한다. 기존 Mattermost 호환 role 문자열은 assignment source로 유지하되 각 role을 DB 정의에 resolve한다.

핵심 permission namespace는 `system.settings.*`, `oidc.*`, `ai.*`, `keys.*`, `approval.*`, `mcp.*`와 채팅 resource permission으로 구성한다. 역할 permission 변경은 다음 요청부터 반영하며, 기본 `system_admin` 역할에서는 복구 권한인 `manage_system`을 제거할 수 없다. 사용자별 system-admin assignment lifecycle은 v0.2.0의 native 관리 표면에 포함하지 않는다.

## 5. 사용자 키 관리

개인 API/MCP 키는 생성 시 256-bit secret을 한 번만 표시하고, 서버에는 HMAC-SHA-256 lookup hash만 저장한다.

- key prefix, 종류, 소유자, 만료, 상태, 최근 사용, rotation chain을 저장한다.
- 최종 권한은 `키 scope ∩ 소유자의 현재 권한`이다.
- 관리자가 role permission을 회수하면 기존 키 권한도 즉시 축소된다.
- 사용자 요청 회전은 새 키를 만들고 이전 키를 configurable grace 기간 동안 `retiring`으로 둔 뒤 폐기한다.
- 폐기된 키는 다시 활성화하지 않고 새 secret을 발급한다.
- 개인 페이지는 자기 키 생성·회전·폐기를, 관리자 페이지는 허용 scope·TTL·회전 유예와 역할별 권한을 관리한다. v0.2.0은 일정 기반 자동 회전을 실행하지 않는다.

## 6. AI

첫 provider protocol은 내부 vLLM, Ollama, LocalAI 등에도 연결하기 쉬운 OpenAI-compatible API다.

- 기본 응답 방식은 SSE streaming이다. `stream`을 생략하면 `true`로 처리한다.
- client disconnect가 upstream request를 취소한다.
- streaming route에는 일반 API의 짧은 timeout을 적용하지 않는다.
- `context_window_tokens`와 `max_output_tokens`를 구분하며 각각 `1..262144` 범위를 검증한다.
- 실제 출력 상한은 요청값, 관리자 설정, provider capability 중 가장 작은 값이다.
- provider base URL, 단일 기본 model, secret과 timeout을 관리 페이지에서 설정하고 연결 시험을 제공한다.
- 개인은 관리자가 구성한 provider/model 범위 안에서 기본 model과 생성 선호만 선택한다.
- prompt와 response 본문은 기본 감사 로그에 저장하지 않고 model, token count, 상태만 남긴다.
- 전역 AI 도우미는 `use_ai` 권한과 개인 설정을 확인한 뒤 현재 브라우저 세션의 대화만 SSE로 전송한다. 화면 이동 뒤 복원되는 대화 이력은 제공하지 않는다.
- 채널 컨텍스트의 AI 요약은 사용자가 버튼을 누를 때 현재 클라이언트에 로드된 최근 메시지만 전송한다. 입력 메시지 목록은 확인할 수 있지만, v0.2.0은 벡터 검색·RAG나 모든 문장에 대한 citation 정확성을 보장하지 않는다.

## 7. MCP와 native API

Mattermost 호환 endpoint는 `/api/v4`에 유지하고 moyro 기능은 `/api/moyro/v1`에 둔다. MCP는 Streamable HTTP endpoint `/mcp`로 제공하며 `kind=mcp` 개인 키를 기본 인증으로 사용한다.

초기 MCP surface:

- Resources: teams, channels, threads
- Read tools: `list_teams`, `list_channels`, `search_messages`, `get_thread`
- Write tools: `create_post`, `reply_to_thread`
- Review tools: `list_pending_approvals`, `approve_request`, `reject_request`

MCP와 REST는 같은 Principal, Authorizer, approval engine, audit service를 사용한다. 설정 secret, API key 평문, 관리자 전용 데이터는 MCP resource로 노출하지 않는다. 보호된 write는 입력의 idempotency key로 중복 승인 요청을 합치며, 승인된 side effect는 request ID로 중복 실행을 막는다. 정책이 적용되지 않는 직접 게시에는 v0.2.0에서 별도 idempotency 보장을 주장하지 않는다.

## 8. 선택적 팀장 검토·승인

승인 기능의 기본값은 비활성이다.

- 적용 가능한 action type, reviewer 역할, 자기 승인 허용 여부, 반려 사유 요구와 만료를 관리자가 설정한다. v0.2.0의 승인 quorum은 한 명으로 고정한다.
- 적용 정책이 없거나 비활성이면 승인 record를 만들지 않고 기존 동작을 즉시 수행한다.
- 활성 정책에 해당하면 MCP write 결과에 승인 대기 상태와 request ID를 반환하고 승인 후 outbox를 통해 한 번만 실행한다.
- 기본 reviewer role은 `team_lead`와 `system_admin`이며 관리자가 role permission mapping을 바꿀 수 있다.
- 일반 메시지 전송은 기본 승인 대상이 아니다.
- 승인 센터 진입점은 과거 요청 이력 확인을 위해 유지한다. 활성 정책이 없으면 새 보호 요청이 생성되지 않을 수 있음을 알리고, 검토 목록은 서버가 사용자·팀·resource scope로 필터링한 결과만 표시한다.

## 9. UI/UX

UI framework는 MUI Core와 MUI X Community DataGrid, navigation은 React Router를 사용한다. 외부 CDN 자산 없이 image에 번들한다.

주요 route:

```text
/login
/today
/inbox/:tab
/my-work/:tab
/approvals/:tab
/assistant
/search
/workspace/:teamId/channel/:channelId
/settings/profile
/settings/appearance
/settings/security/sessions
/settings/developer/keys
/settings/ai
/admin/overview
/admin/site
/admin/auth/keycloak
/admin/ai/providers
/admin/security/keys
/admin/integrations/mcp
/admin/integrations/plugins
/admin/workflows/review
/admin/operations
```

- 인증 사용자의 `/`는 `/today`로 replace redirect한다. 기존 `/workspace/:teamId/saved`, `/workspace/:teamId/scheduled`, `/settings/approvals/mine`, `/settings/approvals/review`도 대응하는 Flow route로 replace redirect한다.
- 데스크톱 글로벌 레일은 오늘, 알림함, 대화, 내 업무, 검색, 승인, 권한이 있을 때 AI를 연결한다. 모바일에서는 오늘·대화·알림·검색과 더보기 drawer로 같은 기능을 제공한다.
- 오늘 화면은 실제 채널별 unread·mention counter와 저장 글, 예약 메시지, 리마인더, 승인 요청 수를 모아 보여준다.
- 통합 알림함은 채널 단위 unread·mention, 승인, 리마인더를 조회한다. 영속 개별 이벤트 feed가 아니며 읽음 저장은 현재 채널 전체에만 적용된다.
- 내 업무는 저장 글, 예약 메시지, 리마인더의 실제 목록과 저장 해제·취소 동작만 제공한다. Task와 Decision Record는 영속 API가 없어 준비 상태로 명시한다.
- 승인 센터는 내 요청과 검토 대기를 분리하고, 검토 권한은 global permission 추정이 아니라 review API의 team/resource scope 결과를 따른다.
- 전역 검색은 사용자가 선택한 팀 범위에서 PostgreSQL 메시지 검색을 수행한다. 벡터·자연어 RAG 검색이나 파일 OCR 검색으로 표시하지 않는다.
- 채널 오른쪽 컨텍스트 패널은 스레드, 사용자가 실행하는 AI 요약, 현재 로드된 메시지의 파일, 채널 정보를 탭으로 묶는다.
- 관리자 운영 현황은 애플리케이션 ping, OIDC, 활성 AI provider, MCP, 범위 내 승인 대기, 이메일 capability와 설정 경고를 표시한다. PostgreSQL pool, worker queue, webhook dead-letter 수치는 해당 API가 없어 표시하지 않는다.
- 플러그인 관리 화면은 `manage_plugins` 권한 아래에서 검토한 Mattermost `.tar.gz`의 업로드·교체·활성화·비활성화·삭제와 schema/custom 설정을 제공한다. 실행 파일은 서명을 검증하지 않는 Trusted Native 코드이며 sandbox가 아니다. Botman 0.1.2, Chatdump 0.5.1, Langflow 0.1.20과 local release gate의 EchoSummary 0.6.5는 실제 PostgreSQL archive 기능 시나리오를 통과한다. 공개 CI는 checksum으로 고정한 EchoSummary 0.6.4 자산을 사용한다. 이 범위는 임의의 Mattermost 플러그인, 다른 버전이나 각 플러그인의 모든 기능 호환을 보장하지 않는다.
- URL을 현재 메뉴·팀·채널의 source of truth로 사용해 새로고침 후 같은 화면을 복원한다.
- `AdminLayout`과 `PersonalSettingsLayout`을 분리하고 프로필 메뉴에서 “내 설정”과 “서비스 관리”를 나눈다.
- 로그인 화면과 프로필 context menu에 서버가 제공한 `moyro v<version>`을 표시한다.
- HTML 기준 글꼴은 16px, 본문 16px, 보조 15px, 메뉴·표 14px 이상, caption 13px 이상이다.
- 주요 control의 hit target은 44px, 보조 control은 최소 40px다.
- 관리자 navigation과 프로필 메뉴 scrollbar는 제품 token을 사용하고 forced-colors 환경에서는 OS 기본 scrollbar를 유지한다.
- stream UI는 중지와 부분 응답 보존을 지원하며 screen reader 알림을 문장/상태 단위로 제한한다.
- Playwright 제품 화면 계약은 26개 인증 desktop route, public login, profile menu, desktop context panel, 세 개의 plugin compatibility 상태와 9개 430×932 mobile 상태를 합친 41개 실제 JPEG를 대상으로 한다. 각 routed 화면은 새로고침 뒤 URL·주요 heading 복원과 same-origin 오류 부재를 확인하며, context panel은 키보드·포커스·모바일 전체 화면 계약도 별도로 검증한다. 세 plugin 화면은 네 실제 archive의 관리자 상태, Langflow RHS와 EchoSummary 사용자 설정이라는 명시된 workflow의 release gate 증거다.

## 10. 호환성과 보안 검증 기준

다음은 production 수준의 호환성과 보안을 주장하기 위한 목표 기준이다.
v0.2.0 릴리스 검증은 태그 시점의 unit/build/browser/offline image gate로
재현하며, 이 문서 갱신만으로 임의 환경의 검증 완료를 주장하지
않는다. [구현·검증 체크리스트](moyro-build-checklist.md)는 공개된
v0.1.0/v0.1.1의 역사적 검증 기록으로 보존한다.

1. private channel/team WebSocket event가 비회원 socket에 전달되지 않는다.
2. logout·session revoke·비활성 사용자의 HTTP/WebSocket token이 즉시 거부된다.
3. secret API 응답, 로그, audit payload에 평문 비밀이 없다.
4. bootstrap은 동시 기동과 재기동에도 idempotent하다.
5. Keycloak state·nonce·PKCE·issuer·audience·expiry negative test가 통과한다.
6. API key plaintext 1회 표시, scope intersection, revoke/expire/rotation grace test가 통과한다.
7. AI는 기본 streaming, disconnect cancellation, 262144 허용·262145 거부를 검증한다.
8. 승인 비활성 시 direct execution 및 승인 row 0개, 활성 시 승인/반려/idempotent execution을 검증한다.
9. MCP read/write permission과 채널 membership negative test가 통과한다.
10. Go test, TypeScript typecheck, production build, browser smoke test, offline image load/start/restart test가 모두 통과한다.

## 11. 명시적 비목표

- Mattermost의 픽셀 단위 복제
- Mattermost 내부 마이크로서비스 구조나 plugin binary ABI의 그대로 재현
- PostgreSQL을 서비스 image 안에 포함
- 외부 CDN, SaaS 전용 dependency 또는 실행 시 package download
- 모든 Mattermost Enterprise·Cloud endpoint에 거짓 성공 응답 제공
- 정책이 없는 일반 메시지에 승인 workflow 강제
- API가 없는 Task·Decision record, RAG 검색, 영속 개별 알림 feed 또는 운영 dashboard의 dead-letter 지표를 제공하는 것처럼 표시
