import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { getGithubOptions } from "@/client/@tanstack/react-query.gen";
import type { DeliveryBody } from "@/client/types.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/github")({ beforeLoad: requireRole("admin"), component: GitHubSettings });

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1">
      <dt className="t-label">{label}</dt>
      <dd className="t-value text-ink break-all">{children}</dd>
    </div>
  );
}

function deliveryStatus(d: DeliveryBody) {
  if (d.error)
    return (
      <Badge dot tone="danger">
        {t("github.delivery.failed")}
      </Badge>
    );
  if (d.handled_at)
    return (
      <Badge dot tone="success">
        {t("github.delivery.ok")}
      </Badge>
    );
  return (
    <Badge dot tone="warning">
      {t("github.delivery.pending")}
    </Badge>
  );
}

function GitHubSettings() {
  const gh = useQuery(getGithubOptions());
  if (gh.isPending) return <Skeleton className="h-60 w-full" />;
  if (gh.error) return <ErrorAlert error={gh.error} onRetry={() => gh.refetch()} />;
  const d = gh.data;
  return (
    <>
      <PageHead title={t("github.title")} hint={t("github.hint")} />
      <div className="grid gap-5">
        <Card>
          <CardHeader>
            <CardTitle>{t("github.app")}</CardTitle>
          </CardHeader>
          {d.app ? (
            <div className="grid gap-4 p-4">
              <dl className="grid gap-4 sm:grid-cols-3">
                <Field label={t("github.app.slug")}>{d.app.slug}</Field>
                <Field label={t("github.app.id")}>{d.app.app_id}</Field>
                <Field label={t("github.app.org")}>{d.app.org}</Field>
              </dl>
              <Button asChild className="justify-self-start">
                <a href={d.app.install_url} target="_blank" rel="noreferrer">
                  {t("github.install")}
                  <ExternalLink className="h-3.5 w-3.5" aria-hidden />
                </a>
              </Button>
            </div>
          ) : (
            <p className="t-hint p-4">{t("github.app.none")}</p>
          )}
        </Card>
        <Card className="overflow-x-auto">
          <CardHeader>
            <CardTitle>{t("github.installations")}</CardTitle>
          </CardHeader>
          {d.installations.length === 0 ? (
            <p className="t-hint p-4">{t("github.installations.none")}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("github.account")}</TableHead>
                  <TableHead>{t("github.delivery.status")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.installations.map((i) => (
                  <TableRow key={i.id}>
                    <TableCell className="t-value text-ink">{i.account_login}</TableCell>
                    <TableCell>
                      <Badge dot tone={i.suspended_at ? "warning" : "success"}>
                        {i.suspended_at ? t("github.suspended") : t("github.active")}
                      </Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t("github.ingress")}</CardTitle>
          </CardHeader>
          <dl className="grid gap-4 p-4 sm:grid-cols-3">
            <Field label={t("github.ingress.mode")}>{d.ingress.mode}</Field>
            <Field label={t("github.ingress.url")}>{d.ingress.webhook_url}</Field>
            {d.ingress.mode === "relay" ? (
              <Field label={t("github.relay")}>
                <Badge dot tone={d.ingress.relay_connected ? "success" : "danger"}>
                  {d.ingress.relay_connected ? t("github.relay.connected") : t("github.relay.down")}
                </Badge>
              </Field>
            ) : null}
          </dl>
        </Card>
        <Card className="overflow-x-auto">
          <CardHeader>
            <CardTitle>{t("github.deliveries")}</CardTitle>
          </CardHeader>
          {d.deliveries.length === 0 ? (
            <p className="t-hint p-4">{t("github.deliveries.none")}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("github.delivery.event")}</TableHead>
                  <TableHead>{t("github.delivery.via")}</TableHead>
                  <TableHead>{t("github.delivery.when")}</TableHead>
                  <TableHead>{t("github.delivery.status")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.deliveries.map((x) => (
                  <TableRow key={x.delivery_id}>
                    <TableCell className="t-value text-ink">
                      {x.event}
                      {x.action ? `.${x.action}` : ""}
                    </TableCell>
                    <TableCell className="t-value">{x.received_via}</TableCell>
                    <TableCell className="t-hint">{new Date(x.received_at).toLocaleString()}</TableCell>
                    <TableCell>{deliveryStatus(x)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      </div>
    </>
  );
}
