/** shadcn/Tailwind 类名合并工具；解决条件类名与冲突 utility 的组合。 */
import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
