import type { ApprovalRequest } from "@/api/client";

const REDACTED_VALUE = "[보호된 값]";
const OMITTED_VALUE = "[이하 생략]";
const MAX_VISIBLE_MESSAGE_LENGTH = 4_000;
const MAX_MESSAGE_SCAN_LENGTH = 8_192;

type KnownApprovalAction = "create_post" | "reply_to_thread";

export type ApprovalPreviewChange = {
  label: string;
  value: string;
};

export type ApprovalPreview = {
  title: string;
  actor: string;
  target?: string;
  impact: string;
  riskLevel: string;
  policyName: string;
  policyReason: string;
  changes: ApprovalPreviewChange[];
  supported: boolean;
  notice: string;
};

type ApprovalActionMetadata = {
  title: string;
  impact: string;
  changeLabel: string;
};

function approvalAction(actionType: string): KnownApprovalAction | null {
  switch (actionType) {
    case "mcp.create_post":
    case "create_post":
      return "create_post";
    case "mcp.reply_to_thread":
    case "reply_to_thread":
      return "reply_to_thread";
    default:
      return null;
  }
}

function approvalActionMetadata(action: KnownApprovalAction | null): ApprovalActionMetadata | null {
  if (action === "create_post") {
    return { title: "채널 메시지 작성", impact: "채널에 새 메시지 생성", changeLabel: "작성할 메시지" };
  }
  if (action === "reply_to_thread") {
    return { title: "스레드 답글 작성", impact: "스레드에 새 답글 생성", changeLabel: "작성할 답글" };
  }
  return null;
}

function plainObject(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) return null;
  return value as Record<string, unknown>;
}

function replaceSensitiveValue(
  source: string,
  pattern: RegExp,
  replacement: string | ((substring: string, ...args: string[]) => string),
): { value: string; redacted: boolean } {
  const value = source.replace(pattern, (substring: string, ...args: unknown[]) => {
    return typeof replacement === "string" ? replacement : replacement(substring, ...args.map(String));
  });
  return { value, redacted: value !== source };
}

/**
 * Redacts credentials embedded in an otherwise displayable message body.
 * This complements the payload field allowlist: a secret can still be placed
 * under the legitimate `message` key, so key-name filtering alone is unsafe.
 */
export function redactApprovalMessage(input: string): { text: string; redacted: boolean; truncated: boolean } {
  const truncated = input.length > MAX_VISIBLE_MESSAGE_LENGTH;
  let value = input.slice(0, MAX_MESSAGE_SCAN_LENGTH);
  let redacted = false;

  const apply = (
    pattern: RegExp,
    replacement: string | ((substring: string, ...args: string[]) => string),
  ) => {
    const result = replaceSensitiveValue(value, pattern, replacement);
    value = result.value;
    redacted ||= result.redacted;
  };

  apply(
    /-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----[\s\S]*?-----END(?: [A-Z0-9]+)* PRIVATE KEY-----/gi,
    REDACTED_VALUE,
  );
  apply(/-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----[\s\S]*/gi, REDACTED_VALUE);
  apply(
    /\b([a-z][a-z0-9+.-]*:\/\/)[^\s/@:]+:[^\s/@]+@/gi,
    (_match, scheme) => `${scheme}${REDACTED_VALUE}@`,
  );
  apply(
    /(["']?)\b(password|passwd|pwd|secret|api[ _-]?key|apikey|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|session[ _-]?token|private[ _-]?key|signing[ _-]?key|webhook[ _-]?secret|authorization|credential|token)\1(\s*(?:=|:)\s*)(?:\[보호된 값\]|Bearer\s+[A-Za-z0-9._~+/=-]{8,}|Basic\s+[A-Za-z0-9+/=]{8,}|"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]]+)/gi,
    (_match, quote, key, separator) => `${quote}${key}${quote}${separator}${REDACTED_VALUE}`,
  );
  apply(/\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}/gi, REDACTED_VALUE);
  apply(/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b/g, REDACTED_VALUE);
  apply(/\b(?:moyro_|mdp_|gh[pousr]_|xox[baprs]-)[A-Za-z0-9_-]{8,}\b/gi, REDACTED_VALUE);
  apply(/\b(?:glpat-|AIza)[A-Za-z0-9_-]{12,}\b/g, REDACTED_VALUE);
  apply(/\b(?:sk|pk)-[A-Za-z0-9_-]{12,}\b/g, REDACTED_VALUE);
  apply(/\bAKIA[A-Z0-9]{16}\b/g, REDACTED_VALUE);

  if (truncated) {
    const visibleLength = MAX_VISIBLE_MESSAGE_LENGTH - OMITTED_VALUE.length - 1;
    value = `${value.slice(0, visibleLength).replace(/[\r\n]+$/, "")}\n${OMITTED_VALUE}`;
  }

  return { text: value, redacted, truncated };
}

function displayText(value: unknown, fallback: string, maxLength = 160): string {
  if (typeof value !== "string") return fallback;
  const normalized = value.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
  if (!normalized) return fallback;
  return normalized.slice(0, maxLength);
}

function riskLevelLabel(value: unknown): string {
  switch (value) {
    case "low": return "낮음";
    case "medium": return "보통";
    case "high": return "높음";
    default: return "확인 필요";
  }
}

function previewFromServer(request: Pick<ApprovalRequest, "action_type" | "preview">): ApprovalPreview | null {
  const source = plainObject(request.preview);
  if (!source || source.secrets_redacted !== true) return null;

  const action = approvalAction(request.action_type);
  const metadata = approvalActionMetadata(action);
  const actor = plainObject(source.actor);
  const target = plainObject(source.target);
  const policy = plainObject(source.policy);
  const changesSource = metadata && Array.isArray(source.changes) ? source.changes : [];
  let additionallyRedacted = false;
  const changes = changesSource.slice(0, 1).flatMap((item, index) => {
    const change = plainObject(item);
    if (!change || typeof change.after !== "string" || !change.after.trim()) return [];
    const value = redactApprovalMessage(change.after);
    additionallyRedacted ||= value.redacted;
    return [{
      label: displayText(change.label, metadata?.changeLabel ?? `변경 ${index + 1}`, 80),
      value: value.text,
    }];
  });
  const supported = metadata !== null && changes.length > 0;

  return {
    title: displayText(source.title, metadata?.title ?? "승인 요청"),
    actor: displayText(actor?.display_name, metadata ? "MCP 자동화" : "자동화 요청"),
    target: displayText(target?.display_name, "", 160) || undefined,
    impact: metadata?.impact ?? "보호된 작업 실행",
    riskLevel: riskLevelLabel(source.risk_level),
    policyName: displayText(policy?.name, "관리자 승인 정책"),
    policyReason: displayText(policy?.reason, "이 작업은 관리자 승인 정책의 보호 대상입니다.", 320),
    changes,
    supported,
    notice: supported
      ? additionallyRedacted
        ? "표시 과정에서 인증 정보로 보이는 값을 추가로 가렸습니다. 내부 식별자와 인증 정보는 표시하지 않습니다."
        : "비밀정보와 내부 식별자를 제거한 안전한 요청 미리보기입니다."
      : "이 작업은 구조화된 변경 내용을 제공하지 않아 요청 상세를 표시하지 않습니다.",
  };
}

export function buildApprovalPreview(request: Pick<ApprovalRequest, "action_type" | "payload" | "preview">): ApprovalPreview {
  const serverPreview = previewFromServer(request);
  if (serverPreview) return serverPreview;

  const action = approvalAction(request.action_type);
  const metadata = approvalActionMetadata(action);
  const payload = plainObject(request.payload);
  if (!metadata || !payload) {
    return {
      title: "승인 요청",
      actor: "자동화 요청",
      impact: "보호된 작업 실행",
      riskLevel: "확인 필요",
      policyName: "관리자 승인 정책",
      policyReason: "이 작업은 관리자 승인 정책의 보호 대상입니다.",
      changes: [],
      supported: false,
      notice: "이 작업은 구조화된 미리보기를 지원하지 않아 요청 상세를 표시하지 않습니다.",
    };
  }

  const rawMessage = typeof payload.message === "string" ? payload.message : "";
  if (!rawMessage.trim()) {
    return {
      title: metadata.title,
      actor: "MCP 자동화",
      impact: metadata.impact,
      riskLevel: "보통",
      policyName: "관리자 승인 정책",
      policyReason: "MCP 메시지 작성은 관리자 승인 정책의 보호 대상입니다.",
      changes: [],
      supported: false,
      notice: "메시지 내용을 안전하게 확인할 수 없어 요청 상세를 표시하지 않습니다.",
    };
  }

  const message = redactApprovalMessage(rawMessage);
  const notice = message.redacted
    ? "메시지에서 인증 정보로 보이는 값을 가렸습니다. 내부 식별자와 인증 정보는 표시하지 않습니다."
    : message.truncated
      ? "긴 메시지의 일부만 표시합니다. 내부 식별자와 인증 정보는 표시하지 않습니다."
      : "내부 식별자와 인증 정보는 표시하지 않습니다.";

  return {
    title: metadata.title,
    actor: "MCP 자동화",
    impact: metadata.impact,
    riskLevel: "보통",
    policyName: "관리자 승인 정책",
    policyReason: "MCP 메시지 작성은 관리자 승인 정책의 보호 대상입니다.",
    changes: [{ label: metadata.changeLabel, value: message.text }],
    supported: true,
    notice,
  };
}
