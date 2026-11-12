import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";

const alertVariants = cva("note", {
  variants: { tone: { neutral: "", success: "note-success", warning: "note-warning", danger: "note-danger" } },
  defaultVariants: { tone: "neutral" },
});

function Alert({ className, tone, ...props }: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  return <div data-slot="alert" role="alert" className={cn(alertVariants({ tone }), className)} {...props} />;
}

export { Alert, alertVariants };
