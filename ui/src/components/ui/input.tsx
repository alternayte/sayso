import * as React from "react";
import { cn } from "@/lib/utils";

function Input({ className, mono, ...props }: React.ComponentProps<"input"> & { mono?: boolean }) {
  return <input data-slot="input" className={cn("control", mono && "control-mono", className)} {...props} />;
}

export { Input };
