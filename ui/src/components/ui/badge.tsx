import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";

const badgeVariants = cva("badge", {
  variants: {
    tone: { neutral: "", accent: "badge-accent", success: "badge-success", warning: "badge-warning", danger: "badge-danger" },
    dot: { true: "badge-dot", false: "" },
  },
  defaultVariants: { tone: "neutral", dot: false },
});

/** Badge is a pill. A status badge has a dot, so status never depends on colour only. */
function Badge({ className, tone, dot, ...props }: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span data-slot="badge" className={cn(badgeVariants({ tone, dot }), className)} {...props} />;
}

export { Badge, badgeVariants };
