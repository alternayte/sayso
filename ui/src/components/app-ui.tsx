import type { UseMutationResult } from "@tanstack/react-query";
import { Check, Loader2 } from "lucide-react";
import { useEffect, useState, type ComponentProps, type ReactNode } from "react";
import { Alert } from "@/components/ui/alert";
import { Button as BaseButton } from "@/components/ui/button";
import { t } from "@/lib/copy";
import { usePress } from "@/motion";

type AsyncButtonProps = ComponentProps<typeof BaseButton> & {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  mutation?: UseMutationResult<any, any, any, any>;
};

/**
 * Button is the shadcn button with press feedback on pointerdown and the busy and done states of
 * a mutation. The label keeps the button width while the spinner or the check sits on top of it.
 */
export function Button({ mutation, children, disabled, type = "button", ...rest }: AsyncButtonProps) {
  const press = usePress<HTMLButtonElement>();
  const busy = mutation?.isPending ?? false;
  const [done, setDone] = useState(false);
  useEffect(() => {
    if (!mutation?.isSuccess) return;
    setDone(true);
    const id = setTimeout(() => setDone(false), 1200);
    return () => clearTimeout(id);
  }, [mutation?.isSuccess, mutation?.submittedAt]);
  return (
    <BaseButton type={type} {...press} {...rest} disabled={disabled || busy} aria-busy={busy || undefined} data-done={done || undefined}>
      <span className="btn-label" data-hidden={busy || done || undefined}>
        {children}
      </span>
      {busy ? (
        <span className="btn-overlay">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
        </span>
      ) : done ? (
        <span className="btn-overlay">
          <Check className="h-4 w-4" aria-hidden />
        </span>
      ) : null}
    </BaseButton>
  );
}

export function ErrorAlert({ error, className, onRetry }: { error: unknown; className?: string; onRetry?: () => void }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : t("error.generic");
  return (
    <Alert tone="danger" className={`items-center justify-between ${className ?? ""}`}>
      <span>{message}</span>
      {onRetry ? (
        <Button size="sm" onClick={onRetry}>
          {t("error.retry")}
        </Button>
      ) : null}
    </Alert>
  );
}

/** PageHead is the title and hint of a page. */
export function PageHead({ title, hint, actions }: { title: string; hint?: string; actions?: ReactNode }) {
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h1 className="t-title">{title}</h1>
        {hint ? <p className="t-hint mt-1.5 max-w-[640px]">{hint}</p> : null}
      </div>
      {actions}
    </header>
  );
}

type SegmentedProps<T extends string> = {
  label: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
  disabled?: boolean;
  className?: string;
};

/**
 * Segmented is a radio group in the segmented style. Arrow keys move the choice, as in a native
 * radio group.
 */
export function Segmented<T extends string>({ label, options, value, onChange, disabled, className }: SegmentedProps<T>) {
  const press = usePress<HTMLButtonElement>();
  const move = (delta: number) => {
    const i = options.findIndex((o) => o.value === value);
    const next = options[(i + delta + options.length) % options.length];
    if (next) onChange(next.value);
  };
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={`segmented ${className ?? ""}`}
      onKeyDown={(e) => {
        if (disabled) return;
        if (e.key === "ArrowRight" || e.key === "ArrowDown") {
          e.preventDefault();
          move(1);
        } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
          e.preventDefault();
          move(-1);
        }
      }}
    >
      {options.map((o) => {
        const on = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            disabled={disabled}
            className={`segment press ${on ? "segment-on" : ""}`}
            onClick={() => !on && onChange(o.value)}
            {...press}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
