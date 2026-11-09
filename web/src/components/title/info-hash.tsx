// A torrent's info hash on a copy card (docs/design/ui.md, Title page): for admins a link to Provider inventory, for
// everyone else who sees files (managers) plain selectable text with a copy button, since the inventory is an admin
// page they cannot open. The copy falls back to selecting the text where the browser has no clipboard (plain HTTP).
import { Check, Copy } from "lucide-react";
import { useRef, useState } from "react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import { adminLink, inventoryLink } from "@/lib/admin-links";

export function InfoHash({ hash, admin }: { hash: string; admin: boolean }) {
  const text = useRef<HTMLSpanElement>(null);
  const [copied, setCopied] = useState(false);
  const to = adminLink(admin, inventoryLink(hash));
  if (to) {
    return (
      <Link to={to} className="break-all font-mono text-[12.5px] text-primary hover:underline" data-testid="info-hash-link">
        {hash}
      </Link>
    );
  }
  const select = () => {
    const el = text.current;
    const sel = window.getSelection();
    if (!el || !sel) return;
    const r = document.createRange();
    r.selectNodeContents(el);
    sel.removeAllRanges();
    sel.addRange(r);
  };
  const copy = async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
      await navigator.clipboard.writeText(hash);
      setCopied(true);
    } catch {
      select();
    }
  };
  return (
    <span className="inline-flex max-w-full items-start gap-1.5" data-testid="info-hash-text">
      <span ref={text} className="select-all break-all font-mono text-[12.5px]">{hash}</span>
      <Button type="button" variant="ghost" size="icon-xs" aria-label={copied ? "Copied" : "Copy the hash"} title={copied ? "Copied" : "Copy the hash"} onClick={() => void copy()}>
        {copied ? <Check /> : <Copy />}
      </Button>
    </span>
  );
}
