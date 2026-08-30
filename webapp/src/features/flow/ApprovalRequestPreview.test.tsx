// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApprovalRequestPreview } from "./ApprovalRequestPreview";
import { buildApprovalPreview } from "./approval-preview";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("ApprovalRequestPreview", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it("renders structured facts and a redacted change instead of raw JSON", async () => {
    const secret = "do-not-render-this-secret";
    const preview = buildApprovalPreview({
      action_type: "mcp.create_post",
      payload: {
        channel_id: "channel-id-must-stay-hidden",
        message: `운영 공지\napi_key=${secret}`,
        _moyro_credential_id: "credential-id-must-stay-hidden",
      },
    });

    await act(async () => {
      root.render(
        <ApprovalRequestPreview
          preview={preview}
          requesterLabel="@requester"
          teamLabel="운영팀"
          targetLabel="운영 공지"
        />,
      );
    });

    expect(container.querySelector("pre")).toBeNull();
    expect(container.querySelector("dl")).not.toBeNull();
    expect(container.textContent).toContain("실행 주체");
    expect(container.textContent).toContain("MCP 자동화");
    expect(container.textContent).toContain("위험도");
    expect(container.textContent).toContain("보통");
    expect(container.textContent).toContain("변경 내용");
    expect(container.textContent).toContain("운영 공지");
    expect(container.textContent).toContain("[보호된 값]");
    expect(container.textContent).not.toContain(secret);
    expect(container.textContent).not.toContain("channel-id-must-stay-hidden");
    expect(container.textContent).not.toContain("credential-id-must-stay-hidden");
  });
});
