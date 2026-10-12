---
name: git-commit-message
description: >-
  按 Conventional Commits 规范撰写 git 提交信息。当用户要求写提交信息、总结一次改动、
  或在提交前整理变更说明时使用。
version: 1.0.0
allowed-tools: read_file run_command
---

# 撰写 git 提交信息

## 何时使用

- 用户让你"写提交信息"、"总结这次改动"、"帮我 commit"。
- 用户贴出 `git diff` / `git status` 的输出并询问如何描述。

## 步骤

1. 先看改动本身：用 `run_command` 执行 `git status --porcelain` 与 `git diff --staged --stat`；
   没有暂存内容时改看 `git diff --stat`。需要时再读具体文件。
2. 判断类型（type）：
   - `feat` 新功能、`fix` 修复缺陷、`refactor` 重构（不改行为）、`docs` 文档、
     `test` 测试、`chore` 杂项、`build` 构建或依赖、`ci` 持续集成。
3. 判断范围（scope）：改动集中的模块或目录名，例如 `store`、`web`、`worker`；跨多个模块时可以省略。
4. 写一行摘要：`<type>(<scope>): <摘要>`，摘要用祈使语气，不超过 72 个字符，不以句号结尾。
5. 需要解释"为什么"时再写正文：空一行后用短段落或列表说明动机与取舍，不复述 diff。
6. 有破坏性变更时在正文末尾加 `BREAKING CHANGE: <说明>`；关联的 issue 写成 `Refs #123` 或 `Fixes #123`。

## 格式

```
<type>(<scope>): <摘要>

<可选正文：为什么这么改、有什么取舍>

<可选页脚：BREAKING CHANGE / Refs>
```

## 注意

- 一次提交只做一件事；发现 diff 混杂了无关改动时，建议用户拆分而不是硬写一条模糊的摘要。
- 摘要语言跟随仓库惯例（看 `git log --oneline -10`）；仓库没有惯例时跟随用户的语言。
- 不要编造 diff 里没有的内容；不确定改动意图时先问。
- 不自动添加个人资料或工具联名页脚。
