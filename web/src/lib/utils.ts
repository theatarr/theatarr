// From Scryer (github.com/scryer-media/scryer) apps/scryer-web/lib/utils.ts @ 2ef7757c, GPL-3.0 (the shadcn helper).
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
