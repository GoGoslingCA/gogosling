// 设置入口的唯一映射；保留旧书签和上下文链接，查询参数不会被重定向丢弃。
export const settingsSections = ["general", "appearance", "shortcuts", "resources", "agents", "models", "extensions", "channels", "files", "audit"] as const;
export type SettingsSection = typeof settingsSections[number];
export const legacySettingsRoutes = { "/agents": "agents", "/providers": "models", "/extensions": "extensions", "/channels": "channels", "/audit": "audit" } as const;

export function settingsSection(search: string): SettingsSection {
  const value = new URLSearchParams(search).get("section");
  return settingsSections.find(section => section === value) ?? "general";
}

export function settingsHref(section: SettingsSection, search = ""): string {
  const params = new URLSearchParams(search);
  params.set("section", section);
  return `/settings?${params}`;
}

/** 返回工作台只接受本 App 的工作路由，避免设置分组循环或外链跳转。 */
export function settingsReturnTarget(state: unknown, lastSessionId: string | null): string {
  const target = state && typeof state === "object" && "returnTo" in state ? state.returnTo : null;
  if (typeof target === "string" && /^\/(chat|projects|jobs)(\/|\?|$)/.test(target)) return target;
  return lastSessionId ? `/chat/${lastSessionId}` : "/chat";
}
