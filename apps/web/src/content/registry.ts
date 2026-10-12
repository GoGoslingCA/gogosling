import { lazy, type ComponentType, type LazyExoticComponent } from "react";
import type { ViewerProps } from "./types";
// 只有用户打开对应格式时才下载展示模块；新增 kind 只在此注册，不改聊天页面。
const text = lazy(() => import("./viewers/TextViewer"));
const document = lazy(() => import("./viewers/DocumentViewer"));
const sheet = lazy(() => import("./viewers/SheetViewer"));
const slides = lazy(() => import("./viewers/SlidesViewer"));
const html = lazy(() => import("./viewers/HTMLViewer"));
const media = lazy(() => import("./viewers/MediaViewer"));
const pdf = lazy(() => import("./viewers/PDFViewer"));
const archive = lazy(() => import("./viewers/ArchiveViewer"));
const viewers: Partial<Record<string, LazyExoticComponent<ComponentType<ViewerProps>>>> = {text,markdown:text,document,sheet,slides,html,image:media,audio:media,video:media,pdf,archive};
export function viewerFor(kind: string) { return viewers[kind]; }
