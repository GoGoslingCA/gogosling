// 壳自身的少量文案（托盘菜单、对话框）。主界面文案在 apps/web/src/locales，不在这里。
//
// 按系统语言选择，不支持的语言回落英文；缺键返回键名而不是抛错。

export type ShellLocale = "zh" | "en" | "fr";

const MESSAGES = {
  zh: {
    "tray.tooltip": "gogosling — {status}",
    "tray.open": "打开 gogosling",
    "tray.restartDaemon": "重启守护进程",
    "tray.autostart": "开机自启动",
    "tray.quit": "退出",
    "daemon.state.idle": "未启动",
    "daemon.state.attaching": "正在查找守护进程",
    "daemon.state.locating": "正在定位 gogoslingd",
    "daemon.state.starting": "守护进程启动中",
    "daemon.state.ready": "守护进程运行中",
    "daemon.state.restarting": "守护进程重启中（第 {attempt}/{max} 次）",
    "daemon.state.stopping": "正在停止守护进程",
    "daemon.state.stopped": "守护进程已停止",
    "daemon.state.failed": "守护进程不可用",
    "daemon.error.executable_not_found": "找不到 gogoslingd。请设置环境变量 GOGOSLING_DAEMON_PATH，或把它放到安装目录的 resources/bin 下。",
    "daemon.error.spawn_failed": "无法启动 gogoslingd（权限或架构不匹配）。",
    "daemon.error.ready_timeout": "gogoslingd 启动后在规定时间内没有就绪。",
    "daemon.error.exited": "gogoslingd 意外退出。",
    "daemon.error.stale_handle": "已有一个 gogoslingd 在运行，但 daemon.json 里的令牌已失效；请先停止它或删除 daemon.json。",
    "daemon.error.restart_limit": "gogoslingd 连续 {max} 次启动失败，已停止重试。",
    "dialog.daemonFailed.title": "守护进程不可用",
    "dialog.daemonFailed.logHint": "详细日志：{logPath}",
    "dialog.retry": "重试",
    "dialog.dismiss": "关闭",
    "dialog.ok": "确定",
    "dialog.unsavedChanges.title": "还有未保存的修改",
    "dialog.unsavedChanges.body": "离开当前页面会丢失尚未保存的修改。可以继续编辑，保存后再退出。",
    "dialog.unsavedChanges.keepEditing": "继续编辑",
    "dialog.unsavedChanges.discard": "放弃修改并离开",
    "dialog.restartNotOwned.title": "无法重启",
    "dialog.restartNotOwned.body": "当前连接的守护进程不是由本应用启动的，请在启动它的地方重启；本应用会自动重新连接。",
  },
  en: {
    "tray.tooltip": "gogosling — {status}",
    "tray.open": "Open gogosling",
    "tray.restartDaemon": "Restart daemon",
    "tray.autostart": "Launch at login",
    "tray.quit": "Quit",
    "daemon.state.idle": "not started",
    "daemon.state.attaching": "looking for a running daemon",
    "daemon.state.locating": "locating gogoslingd",
    "daemon.state.starting": "daemon starting",
    "daemon.state.ready": "daemon running",
    "daemon.state.restarting": "daemon restarting ({attempt}/{max})",
    "daemon.state.stopping": "stopping daemon",
    "daemon.state.stopped": "daemon stopped",
    "daemon.state.failed": "daemon unavailable",
    "daemon.error.executable_not_found": "gogoslingd was not found. Set GOGOSLING_DAEMON_PATH or place it under resources/bin of the installation.",
    "daemon.error.spawn_failed": "gogoslingd could not be started (permissions or architecture mismatch).",
    "daemon.error.ready_timeout": "gogoslingd did not become ready in time.",
    "daemon.error.exited": "gogoslingd exited unexpectedly.",
    "daemon.error.stale_handle": "A gogoslingd is already running but rejects the token in daemon.json; stop it or delete daemon.json first.",
    "daemon.error.restart_limit": "gogoslingd failed to start {max} times in a row; giving up.",
    "dialog.daemonFailed.title": "Daemon unavailable",
    "dialog.daemonFailed.logHint": "Log file: {logPath}",
    "dialog.retry": "Retry",
    "dialog.dismiss": "Dismiss",
    "dialog.ok": "OK",
    "dialog.unsavedChanges.title": "You have unsaved changes",
    "dialog.unsavedChanges.body": "Leaving this page will discard unsaved changes. You can keep editing and save before quitting.",
    "dialog.unsavedChanges.keepEditing": "Keep editing",
    "dialog.unsavedChanges.discard": "Discard changes and leave",
    "dialog.restartNotOwned.title": "Cannot restart",
    "dialog.restartNotOwned.body": "The connected daemon was not started by this app. Restart it where it was started; this app reconnects automatically.",
  },
  fr: {
    "tray.tooltip": "gogosling — {status}",
    "tray.open": "Ouvrir gogosling",
    "tray.restartDaemon": "Redémarrer le démon",
    "tray.autostart": "Lancer à l'ouverture de session",
    "tray.quit": "Quitter",
    "daemon.state.idle": "non démarré",
    "daemon.state.attaching": "recherche d'un démon en cours",
    "daemon.state.locating": "localisation de gogoslingd",
    "daemon.state.starting": "démarrage du démon",
    "daemon.state.ready": "démon en cours d'exécution",
    "daemon.state.restarting": "redémarrage du démon ({attempt}/{max})",
    "daemon.state.stopping": "arrêt du démon",
    "daemon.state.stopped": "démon arrêté",
    "daemon.state.failed": "démon indisponible",
    "daemon.error.executable_not_found": "gogoslingd est introuvable. Définissez GOGOSLING_DAEMON_PATH ou placez-le dans resources/bin de l'installation.",
    "daemon.error.spawn_failed": "Impossible de démarrer gogoslingd (droits ou architecture incompatibles).",
    "daemon.error.ready_timeout": "gogoslingd n'est pas devenu prêt à temps.",
    "daemon.error.exited": "gogoslingd s'est arrêté de façon inattendue.",
    "daemon.error.stale_handle": "Un gogoslingd est déjà en cours d'exécution, mais il refuse le jeton de daemon.json ; arrêtez-le ou supprimez daemon.json d'abord.",
    "daemon.error.restart_limit": "gogoslingd n'a pas démarré {max} fois de suite ; abandon.",
    "dialog.daemonFailed.title": "Démon indisponible",
    "dialog.daemonFailed.logHint": "Fichier journal : {logPath}",
    "dialog.retry": "Réessayer",
    "dialog.dismiss": "Fermer",
    "dialog.ok": "OK",
    "dialog.unsavedChanges.title": "Modifications non enregistrées",
    "dialog.unsavedChanges.body": "Quitter cette page abandonnera les modifications non enregistrées. Vous pouvez continuer à modifier et enregistrer avant de quitter.",
    "dialog.unsavedChanges.keepEditing": "Continuer à modifier",
    "dialog.unsavedChanges.discard": "Abandonner les modifications et quitter",
    "dialog.restartNotOwned.title": "Redémarrage impossible",
    "dialog.restartNotOwned.body": "Le démon connecté n'a pas été démarré par cette application. Redémarrez-le là où il a été lancé ; cette application se reconnecte automatiquement.",
  },
} as const satisfies Record<ShellLocale, Record<string, string>>;

/** 全部文案键。 */
export type MessageKey = keyof (typeof MESSAGES)["zh"];

/** 按系统语言标签选择壳语言：主子标签为 zh 用中文、fr 用法语，其余英文。 */
export function pickLocale(systemLocale: string | undefined): ShellLocale {
  const primary = (systemLocale ?? "").trim().toLowerCase().split(/[-_]/)[0];
  if (primary === "zh") return "zh";
  if (primary === "fr") return "fr";
  return "en";
}

/** 取文案并做 {name} 插值；缺键返回键本身，缺参数保留占位符原样。 */
export function t(locale: ShellLocale, key: MessageKey, params: Record<string, string | number> = {}): string {
  const table: Record<string, string> = MESSAGES[locale] ?? MESSAGES.zh;
  const template = table[key] ?? MESSAGES.zh[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (match, name: string) => (name in params ? String(params[name]) : match));
}

/** 供测试校验各语言键集一致。 */
export function messageKeys(locale: ShellLocale): string[] {
  return Object.keys(MESSAGES[locale]).sort();
}
