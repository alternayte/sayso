import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Activity, Cpu, FolderGit2, Plug, Home, LogOut, ScrollText, Settings2, Users, Webhook } from "lucide-react";
import { useId, type ReactNode } from "react";
import { Brand } from "@/app/brand";
import { Button, ErrorAlert } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { t, type CopyKey } from "@/lib/copy";
import { atLeast, signOut, type Me } from "@/lib/session";
import { usePress } from "@/motion";

type NavItem = { to: string; label: CopyKey; Icon: typeof Home; min: Me["role"] };

const nav: NavItem[] = [
  { to: "/", label: "nav.home", Icon: Home, min: "viewer" },
  { to: "/repositories", label: "nav.repositories", Icon: FolderGit2, min: "viewer" },
  { to: "/settings/github", label: "nav.github", Icon: Webhook, min: "admin" },
  { to: "/settings/models", label: "nav.models", Icon: Cpu, min: "admin" },
  { to: "/settings/mcp", label: "nav.mcp", Icon: Plug, min: "admin" },
  { to: "/settings/general", label: "nav.general", Icon: Settings2, min: "admin" },
  { to: "/settings/access", label: "nav.access", Icon: Users, min: "admin" },
  { to: "/audit", label: "nav.audit", Icon: ScrollText, min: "admin" },
  { to: "/diagnostics", label: "nav.diagnostics", Icon: Activity, min: "admin" },
];

/**
 * Frame is the page layout: a 200px sidebar with the brand and the navigation, and the main
 * area. Below 640px the navigation becomes a horizontal scroll row under the brand.
 */
export function Frame({ nav: items, account, children }: { nav: ReactNode; account?: ReactNode; children: ReactNode }) {
  return (
    <div className="min-h-screen bg-canvas sm:grid sm:grid-cols-[200px_minmax(0,1fr)]">
      <aside className="vt-sidebar grid grid-cols-[minmax(0,1fr)_auto] items-center border-b border-hairline bg-sidebar px-3 pt-3.5 pb-2.5 sm:sticky sm:top-0 sm:flex sm:h-screen sm:flex-col sm:items-stretch sm:border-r sm:border-b-0 sm:py-4">
        <Brand className="mx-1 sm:mb-[18px]" />
        {account ? <div className="sm:order-last sm:mt-auto">{account}</div> : null}
        <nav
          aria-label={t("nav.main")}
          className="relative col-span-2 -mx-3 mt-3 overflow-x-auto px-3 [scrollbar-width:none] sm:mx-0 sm:mt-0 sm:overflow-visible sm:px-0"
        >
          {items}
        </nav>
      </aside>
      <main className="vt-main min-w-0 px-5 py-5 sm:px-8 sm:py-7">
        <div className="mx-auto w-full max-w-[1440px] min-w-0">{children}</div>
      </main>
    </div>
  );
}

function NavLink({ item }: { item: NavItem }) {
  const press = usePress<HTMLAnchorElement>();
  return (
    <Link to={item.to} className="nav-link press-row" activeOptions={{ exact: item.to === "/" }} {...press}>
      <item.Icon className="h-4 w-4 shrink-0" aria-hidden />
      {t(item.label)}
    </Link>
  );
}

export function AppShell({ me, children }: { me: Me; children: ReactNode }) {
  return (
    <Frame
      account={<UserMenu me={me} />}
      nav={
        <div className="flex gap-0.5 sm:flex-col">
          {nav
            .filter((i) => atLeast(me.role, i.min))
            .map((i) => (
              <NavLink key={i.to} item={i} />
            ))}
        </div>
      }
    >
      {children}
    </Frame>
  );
}

export function AppShellSkeleton() {
  return (
    <Frame
      nav={
        <div className="flex gap-0.5 sm:flex-col">
          {nav.slice(0, 3).map((i) => (
            <span key={i.to} className="nav-link">
              <Skeleton className="h-4 w-4" />
              <Skeleton className="h-3 w-20" />
            </span>
          ))}
        </div>
      }
    >
      <div role="status" aria-label={t("loading.account")}>
        <Skeleton className="mb-3 h-5 w-48" />
        <Skeleton className="mb-6 h-3 w-80 max-w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    </Frame>
  );
}

/** UserMenu opens with the HTML popover attribute, which needs no script for focus and dismissal. */
function UserMenu({ me }: { me: Me }) {
  const id = useId();
  const menuId = `${id}-menu`;
  const qc = useQueryClient();
  const navigate = useNavigate();
  const out = useMutation({
    mutationFn: signOut,
    onSuccess: async () => {
      qc.clear();
      await navigate({ to: "/signin" });
    },
  });
  return (
    <div className="sm:border-t sm:border-hairline sm:pt-3">
      <button
        type="button"
        popoverTarget={menuId}
        className="flex w-full items-center gap-2 rounded-card p-1 text-left hover:bg-raised sm:px-1.5 sm:py-1.5"
        aria-label={t("account.menu", { login: me.login })}
      >
        <span
          aria-hidden
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-card border border-hairline-strong bg-raised font-mono text-[11px] font-medium text-ink"
        >
          {me.login.slice(0, 2).toUpperCase()}
        </span>
        <span className="hidden min-w-0 sm:block">
          <span className="block truncate text-[13px] text-ink">{me.name || me.login}</span>
          <span className="block truncate font-mono text-[11px] text-mute">{me.login}</span>
        </span>
      </button>
      <div id={menuId} popover="auto" className="popover enter top-14 right-3 w-64 sm:top-auto sm:right-auto sm:bottom-16 sm:left-3">
        <p className="text-sm text-ink">{me.name || me.login}</p>
        <p className="t-hint">{t("account.role", { role: me.role })}</p>
        <ErrorAlert error={out.error} className="mt-3" />
        <Button className="mt-3 w-full" mutation={out} onClick={() => out.mutate()}>
          <LogOut className="h-3.5 w-3.5" aria-hidden />
          {t("account.signout")}
        </Button>
      </div>
    </div>
  );
}
