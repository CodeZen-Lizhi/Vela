/** 设置分类统一供侧栏、面包屑与内容面板使用。 */
export const settingsSections = [
  { id: "general", name: "常规" },
  { id: "connection", name: "网关与网络" },
  { id: "refresh", name: "刷新与同步" },
  { id: "attention", name: "提醒" },
  { id: "records", name: "数据管理" },
] as const;

/** 分类身份只表示界面位置，不改变各表单独立保存的边界。 */
export type SettingsSection = (typeof settingsSections)[number]["id"];

/** 设置页向应用外壳发出的轻量反馈，例如保存或复制成功。 */
export type Notify = (message: string) => void;

/** 把调用错误转成可展示的文字，保留 Go 返回的原因。 */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
