// The account menu: who is signed in, the theme (auto, light, dark), the profile and
// sign out.
import { ChevronsUpDown, LogOut, Monitor, Moon, Sun, UserRound } from "lucide-react";
import { Link } from "react-router";
import { useAuth, useLogout } from "@/api/auth";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useTheme, type ThemeChoice } from "@/lib/theme";
import { cn } from "@/lib/utils";

const THEMES: { id: ThemeChoice; label: string; icon: typeof Sun }[] = [
  { id: "auto", label: "Auto", icon: Monitor },
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon },
];

export function ThemeSwitch({ className }: { className?: string }) {
  const [theme, setTheme] = useTheme();
  return (
    <div role="radiogroup" aria-label="Theme" className={cn("flex gap-0.5 rounded-[5px] border border-[var(--line)] bg-[var(--bg)] p-0.5", className)}>
      {THEMES.map((t) => (
        <button
          key={t.id}
          type="button"
          role="radio"
          aria-checked={theme === t.id}
          data-theme-choice={t.id}
          onClick={() => setTheme(t.id)}
          className={cn(
            "flex h-[26px] flex-1 items-center justify-center gap-1.5 rounded-[3px] px-2 text-xs text-[var(--ink-3)] transition-colors hover:text-[var(--ink)]",
            theme === t.id && "bg-[var(--surface)] text-[var(--ink)] shadow-[inset_0_0_0_1px_var(--line)]",
          )}
        >
          <t.icon className="size-3.5" />
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function AccountMenu() {
  const me = useAuth();
  const logout = useLogout();
  const name = me.data?.display_name || me.data?.username || "";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Account"
          data-testid="account-menu"
          className="flex h-8 shrink-0 items-center gap-2 rounded-[5px] p-0.5 text-[13px] font-medium text-[var(--ink-2)] outline-none transition-colors hover:bg-[var(--hover)] hover:text-[var(--ink)] focus-visible:ring-2 focus-visible:ring-[var(--line-focus)] sm:pr-2"
        >
          <span className="grid size-7 place-items-center rounded-full border border-[var(--line)] bg-[var(--sunken)] text-[12px] font-semibold uppercase text-[var(--ink-2)]">
            {name.slice(0, 1) || "?"}
          </span>
          <span className="hidden max-w-[10rem] truncate sm:inline">{name}</span>
          <ChevronsUpDown className="hidden size-3.5 text-[var(--ink-3)] sm:inline" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuLabel className="flex items-center justify-between gap-2">
          <span className="truncate">{name}</span>
          <Badge tone="accent" data-testid="account-role">{me.data?.role ?? ""}</Badge>
        </DropdownMenuLabel>
        <div className="px-2.5 pb-2 pt-1">
          <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-[0.09em] text-[var(--ink-3)]">Theme</div>
          <ThemeSwitch />
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/me" data-testid="profile-link">
            <UserRound />
            Profile
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem variant="destructive" onSelect={() => void logout()} data-testid="sign-out">
          <LogOut />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
