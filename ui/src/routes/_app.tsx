import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { AppShell, AppShellSkeleton } from "@/app/shell";
import { meQuery } from "@/lib/session";

export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(meQuery);
    if (!me) throw redirect({ to: "/signin" });
    return { me };
  },
  pendingComponent: AppShellSkeleton,
  component: Layout,
});

function Layout() {
  const { data: me } = useQuery(meQuery);
  if (!me) return <AppShellSkeleton />;
  return (
    <AppShell me={me}>
      <Outlet />
    </AppShell>
  );
}
