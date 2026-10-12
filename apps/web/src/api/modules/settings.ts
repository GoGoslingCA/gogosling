import type { ApiClient } from "../client";
import type { Settings } from "../types";

/** /api/v1/settings */
export function settingsApi(client: ApiClient) {
  return {
    get: (): Promise<Settings> => client.get<Settings>("/api/v1/settings"),
    update: (input: Settings): Promise<Settings> => client.patch<Settings>("/api/v1/settings", input),
  };
}

export type SettingsApi = ReturnType<typeof settingsApi>;
