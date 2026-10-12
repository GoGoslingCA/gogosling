// 桌面退出流程：先让 Electron 完成窗口卸载，再停止守护进程；页面取消退出时保留服务。

interface QuitEvent {
  preventDefault(): void;
}

/** 由桌面壳注入系统操作，便于在不启动 Electron 的情况下验证退出顺序。 */
export interface QuitFlowOptions {
  stopDaemon(): Promise<void>;
  quit(): void;
  onStopError(error: unknown): void;
}

/** 退出状态机；Electron 主进程单线程使用，重复退出不会重复停止守护进程。 */
export class QuitFlow {
  private state: "idle" | "closing" | "stopping" | "ready" = "idle";

  constructor(private readonly options: QuitFlowOptions) {}

  /** 退出期间不重新创建窗口，也不弹出守护进程失败提示。 */
  get isQuitting(): boolean {
    return this.state !== "idle";
  }

  /** 允许 Electron 正常关闭窗口，触发页面的 beforeunload；此时仍保持服务。 */
  beforeQuit(): void {
    if (this.state === "idle") this.state = "closing";
  }

  /** 用户明确允许才忽略 beforeunload；取消时恢复正常使用。 */
  preventedUnload(event: QuitEvent, allowUnload: boolean): void {
    if (allowUnload) {
      event.preventDefault();
    } else if (this.state === "closing") {
      this.state = "idle";
    }
  }

  /** will-quit 只在所有窗口已关闭后发生；等待停止完成再发出最终退出。 */
  willQuit(event: QuitEvent): void {
    if (this.state === "ready") return;
    event.preventDefault();
    if (this.state === "stopping") return;
    this.state = "stopping";
    void this.stopAndQuit();
  }

  private async stopAndQuit(): Promise<void> {
    try {
      await this.options.stopDaemon();
    } catch (error) {
      this.options.onStopError(error);
    } finally {
      this.state = "ready";
      this.options.quit();
    }
  }
}
