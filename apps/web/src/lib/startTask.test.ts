import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Session, Run } from "../api/types";
import { TaskStarter, StartOutcomeUnknown, taskTitle } from "./startTask";

const session: Session = { id: "s1", agent_id: "a1", key: "desktop:a1", origin: "desktop", status: "active", created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
function fixture() {
  return { create: vi.fn().mockResolvedValue(session), upload: vi.fn().mockImplementation(async (_id: string, file: File) => ({ name: file.name, path: `inbound/${file.name}`, size: file.size })), send: vi.fn().mockResolvedValue({ id: "r1", session_id: "s1" } as Run) };
}
describe("一次开始任务", () => {
  it("先创建再上传选中文件最后发送，重复回调不重复运行", async () => {
    const api = fixture(); const start = new TaskStarter(api); const files = [new File(["requirements"], "requirements.md")]; const stage = vi.fn();
    const input = { agentId: "a1", text: "生成计划", files };
    await start.start(input, stage); await start.start(input, stage);
    expect(api.create).toHaveBeenCalledTimes(1); expect(api.upload).toHaveBeenCalledTimes(1); expect(api.send).toHaveBeenCalledTimes(1);
    expect(stage.mock.calls.flat()).toEqual(["creating", "uploading", "sending"]);
    expect(api.send).toHaveBeenCalledWith("s1", { text: "生成计划", attachments: [{ name: "requirements.md", path: "inbound/requirements.md", size: 12 }] });
  });
  it("第二个上传失败后重试复用任务与第一个已上传文件", async () => {
    const api = fixture(); const start = new TaskStarter(api); const files = [new File(["a"], "a.md"), new File(["b"], "b.md")]; const input = { agentId: "a1", text: "整理", files };
    api.upload.mockResolvedValueOnce({ name: "a.md", path: "inbound/a.md", size: 1 }).mockRejectedValueOnce(new ApiError("network", ""));
    await expect(start.start(input, vi.fn())).rejects.toBeInstanceOf(ApiError);
    expect(api.send).not.toHaveBeenCalled(); await start.start(input, vi.fn());
    expect(api.create).toHaveBeenCalledTimes(1); expect(api.upload).toHaveBeenCalledTimes(3); expect(api.upload.mock.calls.map((call) => call[1].name)).toEqual(["a.md", "b.md", "b.md"]);
  });
  it("发送被明确拒绝时保留上传并允许修正输入后继续", async () => {
    const api = fixture(); const start = new TaskStarter(api); api.send.mockRejectedValueOnce(new ApiError("invalid", "", { status: 400 }));
    await expect(start.start({ agentId: "a1", text: "旧目标", files: [] }, vi.fn())).rejects.toBeInstanceOf(ApiError);
    await start.start({ agentId: "a1", text: "已修正目标", files: [] }, vi.fn());
    expect(api.create).toHaveBeenCalledTimes(1); expect(api.send).toHaveBeenLastCalledWith("s1", { text: "已修正目标", attachments: undefined });
  });
  it("发送超时后不自动重发，避免重复执行文件操作", async () => {
    const api = fixture(); const start = new TaskStarter(api); api.send.mockRejectedValue(new ApiError("timeout", "")); const input = { agentId: "a1", text: "写入文件", files: [] };
    await expect(start.start(input, vi.fn())).rejects.toBeInstanceOf(StartOutcomeUnknown);
    await expect(start.start(input, vi.fn())).rejects.toBeInstanceOf(StartOutcomeUnknown);
    expect(start.createdSession).toEqual(session); expect(api.send).toHaveBeenCalledTimes(1);
  });
  it("创建返回未知结果时不盲目再创建任务", async () => {
    const api = fixture(); api.create.mockRejectedValue(new ApiError("network", "")); const start = new TaskStarter(api); const input = { agentId: "a1", text: "整理", files: [] };
    await expect(start.start(input, vi.fn())).rejects.toBeInstanceOf(StartOutcomeUnknown);
    await expect(start.start(input, vi.fn())).rejects.toBeInstanceOf(StartOutcomeUnknown);
    expect(api.create).toHaveBeenCalledTimes(1);
  });
  it("空白新建只创建任务；已有任务不能切换助手后混用附件", async () => {
    const api = fixture(); const start = new TaskStarter(api);
    expect(await start.start({ agentId: "a1", text: "", files: [] }, vi.fn())).toEqual({ session, sent: false });
    expect(api.send).not.toHaveBeenCalled();
    await expect(start.start({ agentId: "a2", text: "另一任务", files: [] }, vi.fn())).rejects.toThrow("start_agent_changed");
  });
  it("任务标题只取第一行且不截断 Unicode 字符", () => {
    expect(taskTitle("\n  简短目标\n完整说明")).toBe("简短目标"); expect(Array.from(taskTitle("🌱".repeat(60)))).toHaveLength(40);
  });
});
