import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  deleteGrantMutation,
  deleteTeamRoleMutation,
  getAccessOptions,
  putGrantMutation,
  putTeamRoleMutation,
} from "@/client/@tanstack/react-query.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";

export const Route = createFileRoute("/_app/settings/access")({ beforeLoad: requireRole("admin"), component: Access });

type RoleName = "admin" | "maintainer" | "viewer";
const roles: RoleName[] = ["admin", "maintainer", "viewer"];

function Access() {
  const qc = useQueryClient();
  const access = useQuery(getAccessOptions());
  const refresh = () => qc.invalidateQueries({ queryKey: getAccessOptions().queryKey });
  const putTeam = useMutation({ ...putTeamRoleMutation(), onSuccess: refresh });
  const delTeam = useMutation({ ...deleteTeamRoleMutation(), onSuccess: refresh });
  const putGrant = useMutation({ ...putGrantMutation(), onSuccess: refresh });
  const delGrant = useMutation({ ...deleteGrantMutation(), onSuccess: refresh });
  const [team, setTeam] = useState({ name: "", role: "viewer" as RoleName });
  const [grant, setGrant] = useState({ name: "", role: "viewer" as RoleName });

  if (access.isPending) return <Skeleton className="h-60 w-full" />;
  if (access.error) return <ErrorAlert error={access.error} onRetry={() => access.refetch()} />;
  const data = access.data;
  return (
    <>
      <PageHead title={t("access.title")} hint={t("access.hint")} />
      <div className="grid gap-5">
        <Pair
          title={t("access.teams")}
          label={t("access.team")}
          rows={data.teams.map((x) => ({ key: x.team_slug, role: x.role }))}
          value={team}
          onChange={setTeam}
          add={putTeam}
          addLabel={t("access.add_team")}
          onAdd={() => putTeam.mutate({ path: { slug: team.name }, body: { role: team.role } })}
          onRemove={(k) => delTeam.mutate({ path: { slug: k } })}
        />
        <Pair
          title={t("access.grants")}
          label={t("access.login")}
          rows={data.grants.map((x) => ({ key: x.login, role: x.role }))}
          value={grant}
          onChange={setGrant}
          add={putGrant}
          addLabel={t("access.add_grant")}
          onAdd={() => putGrant.mutate({ path: { login: grant.name }, body: { role: grant.role } })}
          onRemove={(k) => delGrant.mutate({ path: { login: k } })}
        />
        <Card className="overflow-x-auto">
          <CardHeader>
            <CardTitle>{t("access.users")}</CardTitle>
          </CardHeader>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("access.login")}</TableHead>
                <TableHead>{t("access.role")}</TableHead>
                <TableHead>{t("access.source")}</TableHead>
                <TableHead>{t("access.status")}</TableHead>
                <TableHead>{t("access.last_login")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.users.map((u) => (
                <TableRow key={u.login}>
                  <TableCell className="t-value text-ink">{u.login}</TableCell>
                  <TableCell>
                    <span className="badge">{u.role}</span>
                  </TableCell>
                  <TableCell className="t-value">{u.role_source}</TableCell>
                  <TableCell>
                    <Badge dot tone={u.status === "active" ? "success" : "danger"}>
                      {u.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="t-hint">{u.last_login_at ? new Date(u.last_login_at).toLocaleString() : ""}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </div>
      <ErrorAlert error={delTeam.error ?? delGrant.error} className="mt-4" />
    </>
  );
}

type PairProps = {
  title: string;
  label: string;
  rows: { key: string; role: string }[];
  value: { name: string; role: RoleName };
  onChange: (v: { name: string; role: RoleName }) => void;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  add: any;
  addLabel: string;
  onAdd: () => void;
  onRemove: (key: string) => void;
};

function Pair({ title, label, rows, value, onChange, add, addLabel, onAdd, onRemove }: PairProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <ul className="divide-y divide-hairline">
        {rows.length === 0 ? <li className="t-hint px-4 py-3">{t("access.none")}</li> : null}
        {rows.map((r) => (
          <li key={r.key} className="flex items-center justify-between gap-3 px-4 py-2.5">
            <span className="t-value text-ink">{r.key}</span>
            <span className="flex items-center gap-3">
              <span className="badge">{r.role}</span>
              <Button size="sm" variant="ghost" onClick={() => onRemove(r.key)}>
                {t("access.remove")}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      <form
        className="flex flex-wrap items-end gap-2 border-t border-hairline p-4"
        onSubmit={(e) => {
          e.preventDefault();
          onAdd();
        }}
      >
        <label className="min-w-[180px] flex-1">
          <span className="t-label">{label}</span>
          <Input mono className="mt-1.5" value={value.name} onChange={(e) => onChange({ ...value, name: e.target.value.trim() })} />
        </label>
        <label>
          <span className="t-label">{t("access.role")}</span>
          <select className="control mt-1.5" value={value.role} onChange={(e) => onChange({ ...value, role: e.target.value as RoleName })}>
            {roles.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
        <Button type="submit" mutation={add} disabled={value.name === ""}>
          {addLabel}
        </Button>
      </form>
      <ErrorAlert error={add.error} className="mx-4 mb-4" />
    </Card>
  );
}
