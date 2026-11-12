import * as React from "react";
import { cn } from "@/lib/utils";

/** Table scrolls inside its card. */
function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    // A focusable container lets keyboard users scroll a wide table.
    <div data-slot="table-container" tabIndex={0} className="relative w-full overflow-x-auto">
      <table data-slot="table" className={cn("table", className)} {...props} />
    </div>
  );
}

function TableHeader(props: React.ComponentProps<"thead">) {
  return <thead data-slot="table-header" {...props} />;
}

function TableBody(props: React.ComponentProps<"tbody">) {
  return <tbody data-slot="table-body" {...props} />;
}

function TableRow(props: React.ComponentProps<"tr">) {
  return <tr data-slot="table-row" {...props} />;
}

function TableHead(props: React.ComponentProps<"th">) {
  return <th data-slot="table-head" {...props} />;
}

function TableCell(props: React.ComponentProps<"td">) {
  return <td data-slot="table-cell" {...props} />;
}

export { Table, TableBody, TableCell, TableHead, TableHeader, TableRow };
