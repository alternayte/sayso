import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { listRepositoriesOptions } from "@/client/@tanstack/react-query.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ModeBadge } from "@/lib/mode";
import { t } from "@/lib/copy";

export const Route = createFileRoute("/_app/repositories/")({ component: Repositories });

function Repositories() {
  const list = useQuery(listRepositoriesOptions());
  return (
    <>
      <PageHead title={t("repos.title")} hint={t("repos.hint")} />
      {list.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : list.error ? (
        <ErrorAlert error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.length === 0 ? (
        <Card className="p-6">
          <p className="t-hint">{t("repos.empty")}</p>
        </Card>
      ) : (
        <Card className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("repos.name")}</TableHead>
                <TableHead>{t("repos.mode")}</TableHead>
                <TableHead>{t("repos.branch")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.data.map((r) => {
                const [owner = "", name = ""] = r.full_name.split("/");
                return (
                  <TableRow key={r.id}>
                    <TableCell>
                      <Link to="/repositories/$owner/$name" params={{ owner, name }} className="link t-value">
                        {r.full_name}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <ModeBadge mode={r.mode} />
                    </TableCell>
                    <TableCell className="t-value">{r.default_branch}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
    </>
  );
}
