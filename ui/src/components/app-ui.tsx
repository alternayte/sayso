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
