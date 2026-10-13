// 网页关闭检查与 Electron before-quit 解耦；原生回调退出后才重发退出请求。

/** 返回 true 后，调用方才进入已有的窗口/守护进程退出流程。 */
export class WorkspaceBrowserQuitGate {
  private pending = false;
  private ready = false;

  constructor(private readonly options: { closeBrowser(): Promise<boolean>; quit(): void }) {}

  /** 重复退出共用一次检查；用户取消或异常后仍能重试。 */
  beforeQuit(event: { preventDefault(): void }): boolean {
    if (this.ready) { this.ready = false; return true; }
    event.preventDefault();
    if (this.pending) return false;
    this.pending = true;
    void Promise.resolve().then(() => this.options.closeBrowser()).then(allowed => {
      if (!allowed) { this.pending = false; return; }
      setImmediate(() => {
        this.pending = false;
        this.ready = true;
        this.options.quit();
      });
    }, () => { this.pending = false; });
    return false;
  }
}
