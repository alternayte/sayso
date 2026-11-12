import { Eye, EyeOff } from "lucide-react";
import { useId, useState } from "react";
import type { Field } from "@/client/types.gen";
import { Input } from "@/components/ui/input";
import { t } from "@/lib/copy";

/** SecretValue is what a form sends for a secret: a new value, an environment variable, or nothing to keep it. */
export type SecretValue = { value?: string; env?: string } | undefined;

type Props = {
  label: string;
  field?: Field;
  value: SecretValue;
  onChange: (v: SecretValue) => void;
};

/**
 * SecretField is a write-only secret input (§16.2). Before save, the eye button shows the typed
 * value. After save, it shows the last 4 characters, the save date and a Replace button. It can
 * take an environment variable name instead of a value.
 */
export function SecretField({ label, field, value, onChange }: Props) {
  const id = useId();
  const [editing, setEditing] = useState(!field?.set);
  const [shown, setShown] = useState(false);
  const [useEnv, setUseEnv] = useState(field?.kind === "env_ref");
  if (!editing && field?.set) {
    return (
      <div>
        <span className="t-label">{label}</span>
        <div className="mt-1.5 flex flex-wrap items-center gap-3">
          {field.kind === "env_ref" ? (
            <>
              <span className="t-value text-ink">{field.env_name}</span>
              <span className={field.status === "set" ? "t-hint" : "t-hint text-danger"}>
                {field.status === "set" ? t("secret.env.set") : t("secret.env.unset")}
              </span>
            </>
          ) : (
            <span className="t-value text-ink" data-testid="secret-last4">
              •••• {field.last4}
            </span>
          )}
          {field.updated_at ? (
            <span className="t-hint">{t("secret.saved", { date: new Date(field.updated_at).toLocaleDateString() })}</span>
          ) : null}
          <button type="button" className="btn btn-sm" onClick={() => setEditing(true)}>
            {t("secret.replace")}
          </button>
        </div>
      </div>
    );
  }
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <label className="t-label" htmlFor={id}>
          {label}
        </label>
        <label className="t-hint flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={useEnv}
            onChange={(e) => {
              setUseEnv(e.target.checked);
              onChange(undefined);
            }}
          />
          {t("secret.env")}
        </label>
      </div>
      {useEnv ? (
        <Input
          mono
          id={id}
          className="mt-1.5"
          autoComplete="off"
          value={value?.env ?? ""}
          onChange={(e) => onChange({ env: e.target.value.trim() })}
        />
      ) : (
        <div className="relative mt-1.5">
          <Input
            mono
            id={id}
            className={`pr-10 ${shown ? "reveal" : ""}`}
            key={shown ? "shown" : "hidden"}
            type={shown ? "text" : "password"}
            autoComplete="off"
            spellCheck={false}
            value={value?.value ?? ""}
            onChange={(e) => onChange({ value: e.target.value })}
          />
          <button
            type="button"
            className="btn-ghost absolute top-1/2 right-1 flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded-control"
            aria-label={shown ? t("secret.hide") : t("secret.show")}
            aria-pressed={shown}
            onClick={() => setShown((s) => !s)}
          >
            <span key={shown ? "off" : "on"} className="icon-swap inline-flex">
              {shown ? <EyeOff className="h-4 w-4" aria-hidden /> : <Eye className="h-4 w-4" aria-hidden />}
            </span>
          </button>
        </div>
      )}
    </div>
  );
}
