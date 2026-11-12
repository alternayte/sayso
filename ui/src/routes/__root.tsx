import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import { CenteredPage } from "@/app/brand";
import { t } from "@/lib/copy";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Outlet,
  notFoundComponent: NotFound,
});

export function NotFound() {
  return (
    <CenteredPage>
      <h1 className="t-title">{t("notfound.title")}</h1>
      <p className="mt-2">{t("notfound.body")}</p>
      <Link to="/" className="link mt-4 inline-block">
        {t("notfound.home")}
      </Link>
    </CenteredPage>
  );
}
