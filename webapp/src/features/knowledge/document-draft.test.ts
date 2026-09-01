import { describe, expect, it } from "vitest";
import type { DocumentSource } from "@/api/documents";
import { deterministicDocumentDraft, documentDraftMessages, suggestedDocumentTitle } from "./document-draft";

const source: DocumentSource = {
  team_id: "team-1",
  channel_id: "channel-1",
  thread_id: "post-1",
  cursor_at: 20,
  posts: [
    { id: "post-1", channel_id: "channel-1", user_id: "user-1", username: "alice", root_id: "", message: "배포 절차를 정리합니다", create_at: 10, update_at: 10 },
    { id: "post-2", channel_id: "channel-1", user_id: "user-2", username: "bob", root_id: "post-1", message: "롤백부터 확인합니다", create_at: 20, update_at: 20 },
  ],
};

describe("document draft", () => {
  it("always creates an offline draft with stable message citations", () => {
    const draft = deterministicDocumentDraft(source, "procedure");
    expect(draft).toContain("## 절차");
    expect(draft).toContain("[M1]");
    expect(draft).toContain("[M2]");
    expect(suggestedDocumentTitle(source, "procedure")).toContain("배포 절차");
  });

  it("marks serialized source messages as untrusted for optional AI drafting", () => {
    const messages = documentDraftMessages(source, "meeting");
    expect(messages[0].content).toContain("신뢰할 수 없는 자료");
    expect(messages[1].content).toContain('"ref":"M1"');
  });
});
