export type SSOCallbackCredential =
  | { kind: "code"; value: string }
  | { kind: "legacy-token"; value: string }
  | { kind: "invalid"; value: "" };

const SSO_CODE_PREFIX = "#sso_code=";
const LEGACY_TOKEN_PREFIX = "#token=";
const MAX_CALLBACK_CREDENTIAL_LENGTH = 8 * 1024;

export function isSSOCallbackFragment(hash: string): boolean {
  return hash.startsWith(SSO_CODE_PREFIX) || hash.startsWith(LEGACY_TOKEN_PREFIX);
}

export function parseSSOCallbackFragment(hash: string): SSOCallbackCredential | null {
  let kind: "code" | "legacy-token";
  let encoded: string;
  if (hash.startsWith(SSO_CODE_PREFIX)) {
    kind = "code";
    encoded = hash.slice(SSO_CODE_PREFIX.length);
  } else if (hash.startsWith(LEGACY_TOKEN_PREFIX)) {
    kind = "legacy-token";
    encoded = hash.slice(LEGACY_TOKEN_PREFIX.length);
  } else {
    return null;
  }

  if (encoded.length === 0 || encoded.length > MAX_CALLBACK_CREDENTIAL_LENGTH) {
    return { kind: "invalid", value: "" };
  }
  try {
    const value = decodeURIComponent(encoded);
    return value ? { kind, value } : { kind: "invalid", value: "" };
  } catch {
    return { kind: "invalid", value: "" };
  }
}
