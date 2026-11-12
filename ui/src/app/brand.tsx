import type { ReactNode } from "react";
import { t } from "@/lib/copy";
import { cn } from "@/lib/utils";

/** BrandMark is the cyan square. It is one of the three things that glow. */
export function BrandMark({ className }: { className?: string }) {
  return (
    <span className={cn("brand-mark", className)} aria-hidden>
      <svg viewBox="0 0 32 32" className="h-3 w-3">
        <path
          d="M22 9h-9a3.5 3.5 0 0 0 0 7h6a3.5 3.5 0 0 1 0 7h-9"
          stroke="currentColor"
          strokeWidth="4"
          fill="none"
          strokeLinecap="round"
        />
      </svg>
    </span>
  );
}

export function Brand({ className }: { className?: string }) {
  return (
    <span className={cn("flex items-center gap-2", className)}>
      <BrandMark />
      <span className="font-mono text-[15px] font-semibold tracking-[-0.01em] text-ink">{t("brand")}</span>
    </span>
  );
}

/** CenteredPage holds the sign-in and setup screens. */
export function CenteredPage({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-canvas px-5 py-8 sm:py-14">
      <main className="vt-main mx-auto w-full max-w-[440px]">
        <Brand className="mb-8 px-1" />
        {children}
      </main>
    </div>
  );
}
