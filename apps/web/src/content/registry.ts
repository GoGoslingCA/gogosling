import type { ComponentType } from "react";
import { asyncViewer } from "./asyncViewer";
import type { ViewerProps } from "./types";
// 只有用户打开对应格式时才下载展示模块；新增 kind 只在此注册，不改聊天页面。
const text = asyncViewer(() => import("./viewers/TextViewer"));
const document = asyncViewer(() => import("./viewers/DocumentViewer"));
const sheet = asyncViewer(() => import("./viewers/SheetViewer"));
const slides = asyncViewer(() => import("./viewers/SlidesViewer"));
const html = asyncViewer(() => import("./viewers/HTMLViewer"));
const media = asyncViewer(() => import("./viewers/MediaViewer"));
const pdf = asyncViewer(() => import("./viewers/PDFViewer"));
const archive = asyncViewer(() => import("./viewers/ArchiveViewer"));
const viewers: Partial<Record<string, ComponentType<ViewerProps>>> = {text,markdown:text,document,sheet,slides,html,image:media,audio:media,video:media,pdf,archive};
export function viewerFor(kind: string) { return viewers[kind]; }
