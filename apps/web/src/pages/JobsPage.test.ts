import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, Job } from "../api/types";
import { queryKeys } from "../state/queryKeys";
import { JobsPage, buildJobInput, initialJobAgentId, validJobForm, type JobFormState } from "./JobsPage";

const mocks = vi.hoisted(() => ({ api: {
  agents: { list: vi.fn() },
  jobs: { list: vi.fn(), create: vi.fn(), update: vi.fn(), runNow: vi.fn(), remove: vi.fn() },
} }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => mocks.api }));
vi.mock("../state/useSettings", () => ({ useTimeZone: () => "Asia/Shanghai", useSettingsQuery: () => ({ data: { timezone: "Asia/Shanghai" } }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: "zh" } }) }));

const enabled: Agent = { id: "enabled", name: "EnabledAgent", model: { provider_id: "p1", model: "m" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "enabled", enabled: true, created_at: "", updated_at: "" };
const disabled: Agent = { ...enabled, id: "disabled", name: "DisabledAgent", enabled: false };
const agents = [disabled, enabled];
const form: JobFormState = { agent_id: enabled.id, name: " Reminder ", schedule: " @daily ", timezone: " Asia/Shanghai ", kind: "run_agent", input: " Hello\nworld ", session_key: " ", missed_policy: "skip", enabled: true };

describe("定时任务表单规则", () => {
  it("空筛选或失效筛选回落到已启用智能体，有效筛选优先", () => {
    expect(initialJobAgentId(agents, "")).toBe(enabled.id);
    expect(initialJobAgentId(agents, "missing")).toBe(enabled.id);
    expect(initialJobAgentId(agents, disabled.id)).toBe(enabled.id);
    expect(initialJobAgentId([enabled, { ...enabled, id: "second" }], "second")).toBe("second");
    expect(initialJobAgentId([], "")).toBe("");
  });
  it("拒绝空白、超长输入和无效时区，并保留有意义的内部换行", () => {
    expect(buildJobInput(form).input).toBe("Hello\nworld");
    expect(validJobForm(form, agents)).toBe(true);
    expect(validJobForm({ ...form, input: " \n " }, agents)).toBe(false);
    expect(validJobForm({ ...form, input: "😀".repeat(4001) }, agents)).toBe(false);
    expect(validJobForm({ ...form, timezone: "Local" }, agents)).toBe(false);
    expect(validJobForm({ ...form, timezone: "Not/AZone" }, agents)).toBe(false);
    expect(validJobForm({ ...form, timezone: "" }, agents)).toBe(true);
  });
  it("停用智能体仍可推送文本或保存已停用任务，但不能启用模型任务", () => {
    expect(validJobForm({ ...form, agent_id: disabled.id }, agents)).toBe(false);
    expect(validJobForm({ ...form, agent_id: disabled.id, kind: "push_text" }, agents)).toBe(true);
    expect(validJobForm({ ...form, agent_id: disabled.id, enabled: false }, agents)).toBe(true);
  });
});

describe("定时任务创建交互", () => {
  let root: Root;
  let container: HTMLDivElement;
  let client: QueryClient;
  beforeEach(() => {
    vi.clearAllMocks();
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    HTMLDialogElement.prototype.showModal = function () { this.open = true; };
    HTMLDialogElement.prototype.close = function () { this.open = false; };
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    client.setQueryData(queryKeys.agents, agents);
    client.setQueryData(queryKeys.jobs(), []);
    mocks.api.agents.list.mockResolvedValue(agents);
    mocks.api.jobs.list.mockResolvedValue([]);
    mocks.api.jobs.create.mockResolvedValue({ id: "j1", ...buildJobInput(form) });
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    container.remove();
  });
  function button(text: string): HTMLButtonElement {
    return [...container.querySelectorAll("button")].find((element) => element.textContent === text)!;
  }
  async function renderNew() {
    await act(async () => root.render(createElement(QueryClientProvider, { client }, createElement(JobsPage))));
    await act(async () => button("jobs.create").click());
  }
  async function change(label: string, value: string) {
    const input = container.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[aria-label="${label}"]`)!;
    await act(async () => {
      const prototype = input.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("未选择列表筛选时创建任务会实际发送默认智能体 ID，空白内容不能提交", async () => {
    await renderNew();
    expect(container.querySelector<HTMLSelectElement>('[aria-label="jobs.agent"]')!.value).toBe(enabled.id);
    await change("jobs.name", "Reminder");
    await change("jobs.input", " \n ");
    expect(button("common.save").disabled).toBe(true);
    await act(async () => container.querySelector("#job-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.jobs.create).not.toHaveBeenCalled();
    await change("jobs.input", "Hello");
    await act(async () => container.querySelector("#job-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.jobs.create).toHaveBeenCalledWith(expect.objectContaining({ agent_id: enabled.id, input: "Hello", schedule: "@daily" }));
  });

  it("新建定时任务不列出未启用的助手，推送文本仍使用已启用的助手", async () => {
    await renderNew();
    const select = container.querySelector<HTMLSelectElement>('[aria-label="jobs.agent"]')!;
    expect([...select.options].some((option) => option.value === disabled.id)).toBe(false);
    expect(select.value).toBe(enabled.id);
    await change("jobs.name", "Push");
    await change("jobs.input", "Hello");
    const push = [...container.querySelectorAll("dialog label.check")].find((element) => element.textContent === "jobs.kind_push_text")!.querySelector<HTMLInputElement>("input")!;
    await act(async () => push.click());
    expect(button("common.save").disabled).toBe(false);
    await act(async () => container.querySelector("#job-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.jobs.create).toHaveBeenCalledWith(expect.objectContaining({ agent_id: enabled.id, kind: "push_text" }));
  });

  it("修改基本周期生成现有表达式，清空执行时间后禁止保存", async () => {
    await renderNew();
    await change("jobs.name", "Weekday reminder");
    await change("jobs.input", "Summarize the selected notes");
    await act(async () => {
      const select = container.querySelector<HTMLSelectElement>('[aria-label="scheduleBuilder.frequency"]')!;
      select.value = "weekdays"; select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await change("scheduleBuilder.time", "");
    expect(button("common.save").disabled).toBe(true);
    await change("scheduleBuilder.time", "09:30");
    await change("jobs.timezone", "America/Toronto");
    await act(async () => container.querySelector("#job-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.jobs.create).toHaveBeenCalledWith(expect.objectContaining({ schedule: "30 9 * * 1-5", timezone: "America/Toronto" }));
    expect(mocks.api.jobs.runNow).not.toHaveBeenCalled();
  });

  it.each(["*/17 3-7 * * 1,3", "@midnight"])("编辑只改名称保留原始表达式 %s 及其时区", async (schedule) => {
    const job: Job = { id: "job-existing", agent_id: enabled.id, name: "Existing", schedule, timezone: "America/Toronto", kind: "run_agent", input: "Review logs", session_key: "existing-key", missed_policy: "run_once", enabled: true, created_at: "", updated_at: "" };
    client.setQueryData(queryKeys.jobs(), [job]);
    mocks.api.jobs.list.mockResolvedValue([job]);
    mocks.api.jobs.update.mockResolvedValue(job);
    await act(async () => root.render(createElement(QueryClientProvider, { client }, createElement(JobsPage))));
    await act(async () => button("common.edit").click());
    expect(container.querySelector<HTMLSelectElement>('[aria-label="scheduleBuilder.frequency"]')!.value).toBe(schedule === "@midnight" ? "daily" : "custom");
    await change("jobs.name", "Renamed");
    await act(async () => container.querySelector("#job-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.jobs.update).toHaveBeenCalledWith(job.id, expect.objectContaining({ name: "Renamed", schedule, timezone: "America/Toronto", session_key: "existing-key", missed_policy: "run_once" }));
    expect(mocks.api.jobs.runNow).not.toHaveBeenCalled();
  });
});
