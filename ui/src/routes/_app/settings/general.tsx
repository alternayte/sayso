import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { getSettingHistoryOptions, listSettingsOptions, putSettingMutation } from "@/client/@tanstack/react-query.gen";
import type { SettingBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Card } from "@/components/ui/card";

export const Route = createFileRoute("/_app/settings/general")({ beforeLoad: requireRole("admin"), component: General });

function General() {
  const list = useQuery(listSettingsOptions());
  return (
    <>
      <PageHead title={t("general.title")} hint={t("general.hint")} />
      <ErrorAlert error={list.error} onRetry={() => list.refetch()} />
      <Card className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("general.key")}</TableHead>
              <TableHead>{t("general.value")}</TableHead>
              <TableHead>{t("general.revision")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.isPending
              ? [0, 1, 2].map((i) => (
                  <TableRow key={i}>
                    <TableCell colSpan={3}>
                      <Skeleton className="h-4 w-full" />
                    </TableCell>
                  </TableRow>
                ))
              : (list.data ?? []).map((s) => <Row key={s.key} setting={s} />)}
          </TableBody>
        </Table>
      </Card>
    </>
  );
}

function Row({ setting }: { setting: SettingBody }) {
  const qc = useQueryClient();
  const initial = JSON.stringify(setting.value);
  const [value, setValue] = useState(initial);
  const [invalid, setInvalid] = useState(false);
  const [open, setOpen] = useState(false);
  const save = useMutation({
    ...putSettingMutation(),
    onSuccess: () => qc.invalidateQueries({ queryKey: listSettingsOptions().queryKey }),
  });
  const history = useQuery({ ...getSettingHistoryOptions({ path: { key: setting.key } }), enabled: open });
  const submit = () => {
    try {
      const parsed: unknown = JSON.parse(value);
      setInvalid(false);
      save.mutate({ path: { key: setting.key }, body: { value: parsed } });
    } catch {
      setInvalid(true);
    }
  };
  return (
    <TableRow>
      <TableCell className="t-value text-ink">{setting.key}</TableCell>
      <TableCell>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input
            mono
            aria-label={setting.key}
            className="max-w-[320px]"
            value={value}
            aria-invalid={invalid || undefined}
            onChange={(e) => setValue(e.target.value)}
          />
          <Button type="submit" size="sm" mutation={save} disabled={value === initial}>
            {t("general.save")}
          </Button>
        </form>
        {invalid ? <p className="mt-1 text-[12px] text-danger">{t("general.invalid")}</p> : null}
        <ErrorAlert error={save.error} className="mt-2" />
        {open && history.data ? (
          <ol className="mt-2 space-y-1">
            {history.data.map((h) => (
              <li key={h.revision} className="t-hint">
                <span className="t-value">r{h.revision}</span> {h.updated_by} · {new Date(h.updated_at).toLocaleString()} ·{" "}
                <span className="t-value">{JSON.stringify(h.old_value)}</span> →{" "}
                <span className="t-value">{JSON.stringify(h.new_value)}</span>
              </li>
            ))}
          </ol>
        ) : null}
      </TableCell>
      <TableCell>
        <button type="button" className="link t-value" onClick={() => setOpen(!open)} aria-expanded={open}>
          r{setting.revision} · {t("general.history")}
        </button>
      </TableCell>
    </TableRow>
  );
}
