// 手写镜像 api/openapi/openapi.yaml 的 PreviewDocument；不承载可执行组件。
export interface PreviewBlock { kind: string; text?: string; level?: number; runs?: {text: string; bold?: boolean; italic?: boolean}[]; rows?: string[][]; image?: string }
export interface PreviewDocument {
  format: string; kind: string; name: string; size: number; mime: string; version: string;
  text?: string; blocks?: PreviewBlock[]; slides?: {name: string; blocks: PreviewBlock[]}[];
  sheets?: {name: string; rows: string[][]}[]; entries?: {name: string; size: number; directory: boolean}[]; warnings?: string[];
}
export interface FileEntry { name: string; path: string; size: number; is_dir: boolean; mod_time: string }
export interface ViewerProps { document: PreviewDocument; agentId: string; path: string; layout?: boolean }
