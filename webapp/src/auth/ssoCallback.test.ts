import { describe, expect, it } from "vitest";

import { isSSOCallbackFragment, parseSSOCallbackFragment } from "./ssoCallback";

describe("SSO callback fragment", () => {
  it("parses the one-time SSO code without treating it as a session token", () => {
    expect(parseSSOCallbackFragment("#sso_code=short-lived_code-123")).toEqual({
      kind: "code",
      value: "short-lived_code-123",
    });
  });

  it("rejects the removed legacy bearer fragment", () => {
    expect(parseSSOCallbackFragment("#token=header.payload.signature")).toBeNull();
    expect(isSSOCallbackFragment("#token=header.payload.signature")).toBe(false);
  });

  it("fails closed instead of throwing on malformed or oversized fragments", () => {
    expect(parseSSOCallbackFragment("#sso_code=%zz")).toEqual({ kind: "invalid", value: "" });
    expect(parseSSOCallbackFragment(`#sso_code=${"a".repeat(8193)}`)).toEqual({ kind: "invalid", value: "" });
    expect(parseSSOCallbackFragment("#oauth_error=exchange_failed")).toBeNull();
    expect(isSSOCallbackFragment("#sso_code=")).toBe(true);
  });
});
