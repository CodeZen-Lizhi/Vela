/** 设置分类统一供侧栏、面包屑与内容面板使用。 */
export const settingsSections = [
  { id: "general", name: "常规" },
  { id: "connection", name: "接入与访问" },
  { id: "account", name: "账户" },
  { id: "network", name: "网络代理" },
  { id: "attention", name: "总览提醒" },
  { id: "records", name: "记录管理" },
] as const;

/** 分类身份只表示界面位置，不改变各表单独立保存的边界。 */
export type SettingsSection = (typeof settingsSections)[number]["id"];

/** 设置页向应用外壳发出的轻量反馈，例如保存或复制成功。 */
export type Notify = (message: string) => void;

/** 把调用错误转成可展示的文字，保留 Go 返回的原因。 */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
