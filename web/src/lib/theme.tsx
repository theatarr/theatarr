// Theme: auto (follow the system), light or dark. Dark is the default look; the choice is kept per browser.
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

export type ThemeChoice = "auto" | "light" | "dark";
const KEY = "theatarr.theme";

export function readTheme(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "auto") return v;
  } catch {
    // storage blocked: auto
  }
  return "auto";
}

function systemDark(): boolean {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? true;
}

export function applyTheme(choice: ThemeChoice) {
  const dark = choice === "dark" || (choice === "auto" && systemDark());
  const root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.dataset.theme = choice;
  root.style.colorScheme = dark ? "dark" : "light";
}

const ThemeContext = createContext<[ThemeChoice, (t: ThemeChoice) => void]>(["auto", () => {}]);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoice] = useState<ThemeChoice>(readTheme);
  useEffect(() => {
    applyTheme(choice);
    if (choice !== "auto") return;
    const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
    const on = () => applyTheme("auto");
    mq?.addEventListener("change", on);
    return () => mq?.removeEventListener("change", on);
  }, [choice]);
  const set = (t: ThemeChoice) => {
    try {
      window.localStorage.setItem(KEY, t);
    } catch {
      // not kept; still applied for this page
    }
    setChoice(t);
  };
  return <ThemeContext.Provider value={[choice, set]}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  return useContext(ThemeContext);
}
