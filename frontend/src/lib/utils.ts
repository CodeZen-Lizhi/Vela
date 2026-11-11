import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** 合并条件类名，并让后出现的 Tailwind 工具类覆盖冲突项。 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

/** 为组件属性补充可绑定的原生元素引用。 */
export type WithElementRef<
  T,
  U extends HTMLElement = HTMLElement,
> = T & {
  /** 指向组件渲染出的原生元素，未挂载时为空。 */
  ref?: U | null;
};

/** 移除组件属性中的 child 片段，供包装组件重新定义内容。 */
export type WithoutChild<T> = T extends { child?: unknown }
  ? Omit<T, "child">
  : T;

/** 移除组件属性中的 children 片段，供包装组件重新定义内容。 */
export type WithoutChildren<T> = T extends { children?: unknown }
  ? Omit<T, "children">
  : T;

/** 从覆盖层属性中移除 children 与 child，供生成的 shadcn-svelte 弹窗使用。 */
export type WithoutChildrenOrChild<T> = Omit<T, "children" | "child">;
