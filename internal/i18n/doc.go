// Package i18n 提供内核侧面向用户的文案（中文、英文与法文）。
//
// 界面文案由前端自行管理；凡是由内核产生并展示给用户的文字
// （错误信息、IM 状态提示、斜杠命令回复、工具显示名、命令行输出）都从这里取。
//
// 用法：
//
//	msg := i18n.T(locale, "approval.requested", map[string]any{"tool": "run_command"})
//	err := i18n.ErrorMessage(locale, string(code), nil)
//	loc := i18n.ResolveLocale(userPref, r.Header.Get("Accept-Language"), string(channel.Kind))
//
// 语言包：locales/zh.json、locales/en.json 与 locales/fr.json 随二进制嵌入，是嵌套的 JSON 对象，
// 叶子必须是非空字符串；查找键为点分路径（errors.not_found）。
// 各语言包的键树必须完全一致（由测试保证）。插值占位符写作 {name}，
// 缺少对应参数时原样保留，不会 panic。
//
// 键的命名约定（顶层命名空间）：
//   - errors.<code>：与 model.Code 一一对应（ErrorCodes 镜像了全部取值）；
//   - common.*：通用词汇（是 / 否 / 已启用 …）；
//   - tools.<name>：内置工具的显示名；
//   - run.status.<status>、approval.*：运行与审批状态提示；
//   - run.error.<常量>：运行错误机器常量（Run.Error 里的 "daemon restarted"、"worker crashed" 等，
//     空格换成下划线即为键名）的本地化文案；RunErrorText / RunErrorTexts 按这一约定查找，
//     server、网关与 CLI 共用，不再各自维护映射表。新增运行错误常量时只需在各语言包加键。
//
// 语言判定：
//   - EffectiveLocale 是内核"实际生效语言"的唯一解析：设置项 > LC_ALL / LC_MESSAGES / LANG（FromEnv）
//     > DefaultLocale。组合根据此决定引擎、网关、技能提示的语言，并经 server.Options.DefaultLocale
//     作为 GET /settings 的 effective_locale 与请求缺省语言；CLI 用同一函数选择终端语言。
//   - ResolveLocale 用于单个请求：按"用户偏好 > Accept-Language（含 q 值）> 通道提示 > en"的顺序取
//     第一个受支持的语言。两者都会跳过不受支持的值（如 ja、C），而不是误判为默认语言。
//
// 边界：本包是叶子包，只依赖标准库；领域包不得硬编码面向用户的文字。
// 因为不能导入 model，ErrorCodes 是 model.Code 的镜像列表，新增错误码时两边都要改。
package i18n
