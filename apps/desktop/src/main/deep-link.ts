// 深链（gogosling://）在 app ready 之前到达时的暂存。
//
// macOS 冷启动时 open-url 在 applicationWillFinishLaunching 之后、ready 之前触发；监听必须尽早注册
// 才不会漏掉"把应用拉起来的那个 URL"，但此时还不能创建 BrowserWindow——直接处理会抛出
// "Cannot create BrowserWindow before app is ready"（XP-09）。所以 ready（且壳 init 完成）之前
// 只记下最近一个深链，就绪后再交给处理函数。

export class DeepLinkGate {
  private ready = false;
  private pending: string | null = null;

  constructor(private readonly handle: (url: string) => void) {}

  /** 收到深链：未就绪时暂存（只保留最近一个），已就绪时立即处理。 */
  offer(url: string): void {
    if (!this.ready) {
      this.pending = url;
      return;
    }
    this.handle(url);
  }

  /**
   * 标记就绪并处理暂存的深链；没有暂存时处理 fallback（例如 Windows/Linux 冷启动时 argv 里的深链）。
   * 重复调用无副作用。
   */
  markReady(fallback: string | null = null): void {
    if (this.ready) return;
    this.ready = true;
    const link = this.pending ?? fallback;
    this.pending = null;
    if (link) this.handle(link);
  }

  get isReady(): boolean {
    return this.ready;
  }
}
