// 从构建元数据读取版本；侧栏与安装包版本保持一致，不在翻译中复制版本号。
declare const __GOGOSLING_RELEASE__: string;

export const APP_RELEASE_VERSION = __GOGOSLING_RELEASE__;
export const APP_RELEASE_LABEL = APP_RELEASE_VERSION.replace(/^(\d+\.\d+\.\d+)-beta$/, "Beta $1");
