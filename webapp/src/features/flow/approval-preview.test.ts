import { describe, expect, it } from "vitest";
import { buildApprovalPreview, redactApprovalMessage } from "./approval-preview";

describe("approval preview", () => {
  it("builds a structured preview for MCP message creation from allowlisted fields only", () => {
    const preview = buildApprovalPreview({
      action_type: "mcp.create_post",
      payload: {
        channel_id: "channel-internal-id",
        message: "내일 오전 운영 점검 결과를 공유합니다.",
        _moyro_credential_id: "credential-internal-secret",
        unexpected: { password: "nested-secret" },
      },
    });

    expect(preview.title).toBe("채널 메시지 작성");
    expect(preview.actor).toBe("MCP 자동화");
    expect(preview.impact).toBe("채널에 새 메시지 생성");
    expect(preview.changes).toEqual([
      { label: "작성할 메시지", value: "내일 오전 운영 점검 결과를 공유합니다." },
    ]);
    const visible = JSON.stringify(preview);
    expect(visible).not.toContain("channel-internal-id");
    expect(visible).not.toContain("credential-internal-secret");
    expect(visible).not.toContain("nested-secret");
    expect(visible).not.toContain("_moyro_credential_id");
  });

  it("labels MCP thread replies without exposing the root or credential identifiers", () => {
    const preview = buildApprovalPreview({
      action_type: "mcp.reply_to_thread",
      payload: {
        channel_id: "channel-id",
        root_id: "root-post-id",
        message: "조치 완료했습니다.",
        _moyro_credential_id: "credential-id",
      },
    });

    expect(preview.title).toBe("스레드 답글 작성");
    expect(preview.impact).toBe("스레드에 새 답글 생성");
    expect(preview.changes[0]).toEqual({ label: "작성할 답글", value: "조치 완료했습니다." });
    expect(JSON.stringify(preview)).not.toMatch(/root-post-id|credential-id|channel-id/);
  });

  it("redacts secret-like values embedded under the legitimate message field", () => {
    const secrets = {
      apiKey: "api-key-value-123456789",
      bearer: "bearer-token-value-123456789",
      jwt: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzZWNyZXQifQ.signaturevalue",
      pat: "moyro_superSecretToken123",
      password: "database-password-123",
      urlPassword: "url-password-123",
      privateKey: "private-key-material",
    };
    const message = [
      "배포 설정을 검토해 주세요.",
      `{"API key":"${secrets.apiKey}"}`,
      `Authorization: Bearer ${secrets.bearer}`,
      secrets.jwt,
      secrets.pat,
      `password=${secrets.password}`,
      `https://operator:${secrets.urlPassword}@internal.example.test/path`,
      `-----BEGIN PRIVATE KEY-----\n${secrets.privateKey}\n-----END PRIVATE KEY-----`,
    ].join("\n");

    const preview = buildApprovalPreview({ action_type: "mcp.create_post", payload: { message } });
    const visible = JSON.stringify(preview);

    expect(preview.changes[0]?.value).toContain("배포 설정을 검토해 주세요.");
    expect(preview.changes[0]?.value).toContain("[보호된 값]");
    for (const secret of Object.values(secrets)) {
      expect(visible).not.toContain(secret);
    }
    expect(preview.notice).toContain("값을 가렸습니다");
  });

  it("prefers the server preview and re-redacts its allowlisted message", () => {
    const serverSecret = "server-preview-secret-123456789";
    const fallbackSecret = "legacy-payload-must-not-win";
    const preview = buildApprovalPreview({
      action_type: "mcp.create_post",
      preview: {
        title: "운영 공지 메시지 작성",
        risk_level: "medium",
        actor: { type: "mcp_key", display_name: "릴리스 자동화" },
        target: { type: "channel", display_name: "운영 공지" },
        changes: [{ label: "작성할 메시지", after: `점검 결과입니다. api_key=${serverSecret}` }],
        policy: { name: "팀장 검토", reason: "MCP 쓰기는 검토가 필요합니다." },
        secrets_redacted: true,
      },
      payload: { message: fallbackSecret },
    });

    expect(preview.title).toBe("운영 공지 메시지 작성");
    expect(preview.actor).toBe("릴리스 자동화");
    expect(preview.target).toBe("운영 공지");
    expect(preview.riskLevel).toBe("보통");
    expect(preview.policyName).toBe("팀장 검토");
    expect(preview.changes[0]?.value).toContain("점검 결과입니다.");
    expect(preview.changes[0]?.value).toContain("[보호된 값]");
    expect(JSON.stringify(preview)).not.toContain(serverSecret);
    expect(JSON.stringify(preview)).not.toContain(fallbackSecret);
  });

  it("does not expose any payload details for unsupported or malformed actions", () => {
    const unsupported = buildApprovalPreview({
      action_type: "plugin.execute_external_action",
      preview: {
        title: "승인 요청",
        risk_level: "unknown",
        actor: { type: "automation", display_name: "자동화 요청" },
        target: { type: "resource", display_name: "보호된 작업 대상" },
        changes: [{ label: "unsafe", after: "server-preview-must-stay-hidden" }],
        policy: { name: "관리자 승인 정책", reason: "승인 대상" },
        secrets_redacted: true,
      },
      payload: { description: "apparently harmless", token: "unknown-secret-value" },
    });
    const malformed = buildApprovalPreview({
      action_type: "mcp.create_post",
      payload: { message: 123, api_key: "another-secret-value" },
    });

    expect(unsupported.supported).toBe(false);
    expect(unsupported.title).toBe("승인 요청");
    expect(unsupported.changes).toEqual([]);
    expect(JSON.stringify(unsupported)).not.toMatch(/apparently harmless|unknown-secret-value|plugin\.execute|server-preview-must-stay-hidden/);
    expect(malformed.supported).toBe(false);
    expect(malformed.changes).toEqual([]);
    expect(JSON.stringify(malformed)).not.toContain("another-secret-value");
  });

  it("limits the displayed message size", () => {
    const result = redactApprovalMessage("가".repeat(4_100));
    expect(result.truncated).toBe(true);
    expect(result.text).toHaveLength(4_000);
    expect(result.text.endsWith("[이하 생략]")).toBe(true);
  });

  it("keeps an already-redacted server value stable", () => {
    const result = redactApprovalMessage("API key: [보호된 값]");
    expect(result.text).toBe("API key: [보호된 값]");
    expect(result.redacted).toBe(false);
  });
});
