import type { AdminPlugin } from "@/api/client";

function nonEmptyString(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  return normalized || undefined;
}

function manifest(plugin: AdminPlugin): Record<string, unknown> {
  return plugin.manifest && typeof plugin.manifest === "object" ? plugin.manifest : {};
}

export function adminPluginID(plugin: AdminPlugin): string {
  return nonEmptyString(plugin.id)
    ?? nonEmptyString(plugin.plugin_id)
    ?? nonEmptyString(manifest(plugin).id)
    ?? "";
}

export function adminPluginDisplayName(plugin: AdminPlugin): string {
  return nonEmptyString(manifest(plugin).name)
    ?? nonEmptyString(plugin.name)
    ?? (adminPluginID(plugin) || "알 수 없는 플러그인");
}

export function adminPluginDescription(plugin: AdminPlugin): string {
  return nonEmptyString(manifest(plugin).description)
    ?? nonEmptyString(plugin.description)
    ?? "이 플러그인이 제공하는 관리자 설정입니다.";
}

export function adminPluginVersion(plugin: AdminPlugin): string {
  return nonEmptyString(plugin.version)
    ?? nonEmptyString(manifest(plugin).version)
    ?? "dev";
}

export function sortAdminPlugins(plugins: readonly AdminPlugin[]): AdminPlugin[] {
  return [...plugins].sort((left, right) => {
    const byName = adminPluginDisplayName(left).localeCompare(adminPluginDisplayName(right), "ko", {
      sensitivity: "base",
    });
    if (byName !== 0) return byName;
    return adminPluginID(left).localeCompare(adminPluginID(right));
  });
}
