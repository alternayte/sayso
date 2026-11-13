import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useVirtualizer } from "@tanstack/react-virtual";
import { X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { getRunOptions, listRunEventsOptions, listRunEventsQueryKey } from "@/client/@tanstack/react-query.gen";
import type { EventBody } from "@/client/types.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { RoundStatus, sha7 } from "@/lib/review";
import { useTween } from "@/motion";

export const Route = createFileRoute("/_app/runs/$id")({
  loader: ({ context, params }) => {
    void context.queryClient.prefetchQuery(getRunOptions({ path: { id: params.id } }));
    void context.queryClient.prefetchQuery(listRunEventsOptions({ path: { id: params.id } }));
  },
  component: Run,
});

const rowHeight = 36;

function Tokens({ value }: { value: number }) {
  return <span className="t-value tabular">{Math.round(useTween(value)).toLocaleString()}</span>;
}

function Cost({ value }: { value: number }) {
  return <span className="t-value tabular">${useTween(value).toFixed(4)}</span>;
}

function Run() {
  const { id } = Route.useParams();
  const qc = useQueryClient();
  const run = useQuery({
    ...getRunOptions({ path: { id } }),
    refetchInterval: (q) => (q.state.data && (q.state.data.status === "running" || q.state.data.status === "queued") ? 3000 : false),
  });
  const events = useQuery(listRunEventsOptions({ path: { id } }));
  const [arrived, setArrived] = useState<Set<number>>(new Set());
  const live = run.data?.status === "running" || run.data?.status === "queued";

  // Live view: the stream sends events after the ones the page has (§15.3).
  useEffect(() => {
    if (!live || !events.data) return;
    const after = events.data.reduce((m, e) => Math.max(m, e.seq), 0);
    const src = new EventSource(`/api/v1/runs/${id}/stream?after=${after}`);
    src.addEventListener("event", (m) => {
      const e = JSON.parse((m as MessageEvent).data) as EventBody;
      qc.setQueryData<EventBody[]>(listRunEventsQueryKey({ path: { id } }), (old) =>
        old?.some((x) => x.seq === e.seq) ? old : [...(old ?? []), e],
      );
      setArrived((a) => new Set(a).add(e.seq));
    });
    src.addEventListener("end", () => {
      src.close();
      void run.refetch();
    });
    return () => src.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, live, events.data === undefined]);

  return (
    <>
      {run.data?.repository ? (
        <p className="t-crumb mb-2">
          {run.data.round_id ? (
            <Link to="/rounds/$id" params={{ id: run.data.round_id }} className="link">
              {run.data.repository}#{run.data.number} · {sha7(run.data.head_sha ?? "")}
            </Link>
          ) : (
            <span>
              {run.data.repository}#{run.data.number}
            </span>
          )}
        </p>
      ) : null}
      <PageHead title={t("run.title", { kind: run.data?.kind ?? "" })} />
      {run.isPending || events.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : run.error || events.error ? (
        <ErrorAlert error={run.error ?? events.error} onRetry={() => void run.refetch()} />
      ) : (
        <div className="grid gap-5">
          <Card className="flex flex-wrap items-center gap-4 p-4">
            <RoundStatus status={run.data.status} />
            <span className="t-hint">
              {t("run.tokens")} <Tokens value={run.data.tokens_in + run.data.tokens_out} />
            </span>
            <span className="t-hint">
              {t("run.cost")} <Cost value={run.data.cost_usd} />
            </span>
            <span className="t-hint">{t("run.events", { n: events.data.length })}</span>
            {run.data.exit_reason && run.data.exit_reason !== "completed" ? <span className="text-ink">{run.data.exit_reason}</span> : null}
            <span className="flex flex-wrap gap-3">
              {run.data.artifacts.map((a) => (
                <a key={a.name} className="link t-value" href={`/api/v1/runs/${id}/artifacts/${a.name}`}>
                  {a.name}
                </a>
              ))}
            </span>
          </Card>
          <Stages events={events.data} />
          <EventList runID={id} events={events.data} arrived={arrived} />
        </div>
      )}
    </>
  );
}

/** Stages is the timeline summary: each stage with its model turns, tool calls and findings. */
function Stages({ events }: { events: EventBody[] }) {
  const stages = useMemo(() => {
    const m = new Map<string, { turns: number; tools: number; mcp: number; findings: number; tokens: number }>();
    for (const e of events) {
      const k = e.stage || t("run.stage.setup");
      const s = m.get(k) ?? { turns: 0, tools: 0, mcp: 0, findings: 0, tokens: 0 };
      if (e.type === "model_turn") s.turns++;
      if (e.type === "tool_call") s.tools++;
      if (e.type === "mcp_call") s.mcp++;
      if (e.type === "finding") s.findings++;
      s.tokens += e.tokens_in + e.tokens_out;
      m.set(k, s);
    }
    return [...m.entries()];
  }, [events]);
  return (
    <ol aria-label={t("run.timeline")} className="grid grid-cols-[repeat(auto-fill,minmax(14rem,1fr))] gap-3">
      {stages.map(([name, s]) => (
        <li key={name} className="card grid gap-1 p-3 text-[13px]">
          <span className="t-value text-ink">{name}</span>
          <span className="t-hint">{t("run.stage.summary", { turns: s.turns, tools: s.tools, mcp: s.mcp, findings: s.findings })}</span>
          <span className="t-hint tabular">{t("run.stage.tokens", { n: s.tokens.toLocaleString() })}</span>
        </li>
      ))}
    </ol>
  );
}

const typeTone: Record<string, "accent" | "success" | "warning" | "danger" | undefined> = {
  model_turn: "accent",
  tool_call: undefined,
  mcp_call: undefined,
  finding: "warning",
  error: "danger",
  env: undefined,
  note: undefined,
  skill_load: "success",
  pkg_summary: undefined,
};

/** EventList is a virtualized list (NFR-007) that follows new events while the reader is at the bottom. */
function EventList({ runID, events, arrived }: { runID: string; events: EventBody[]; arrived: Set<number> }) {
  const parent = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [pending, setPending] = useState(0);
  const [open, setOpen] = useState<EventBody | null>(null);
  const v = useVirtualizer({ count: events.length, getScrollElement: () => parent.current, estimateSize: () => rowHeight, overscan: 12 });
  const count = useRef(events.length);
  useEffect(() => {
    const added = events.length - count.current;
    count.current = events.length;
    if (added <= 0) return;
    if (atBottom.current) v.scrollToIndex(events.length - 1, { align: "end", behavior: "smooth" });
    else setPending((p) => p + added);
  }, [events.length, v]);
  return (
    <Card className="relative">
      <div
        ref={parent}
        role="list"
        aria-label={t("run.events.label")}
        data-virtualized=""
        className="h-[60vh] overflow-auto"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < rowHeight * 2;
          if (atBottom.current) setPending(0);
        }}
      >
        <div className="relative w-full" ref={(el) => el?.style.setProperty("height", `${v.getTotalSize()}px`)}>
          {v.getVirtualItems().map((it) => {
            const e = events[it.index]!;
            return (
              <div
                key={e.seq}
                role="listitem"
                className={`press-row absolute left-0 flex w-full cursor-pointer items-center gap-3 border-b border-hairline px-4 text-[13px] ${arrived.has(e.seq) ? "arrive" : ""}`}
                ref={(el) => {
                  if (!el) return;
                  el.style.setProperty("height", `${rowHeight}px`);
                  el.style.setProperty("transform", `translateY(${it.start}px)`);
                }}
                onClick={() => setOpen(e)}
              >
                <span className="t-hint tabular w-20 shrink-0">{new Date(e.at).toLocaleTimeString()}</span>
                <span className="t-hint w-20 shrink-0 truncate">{e.stage}</span>
                <Badge tone={typeTone[e.type]}>{e.type}</Badge>
                <span className="t-value w-40 shrink-0 truncate text-ink">{e.name}</span>
                <span className="t-hint w-24 shrink-0 truncate">{e.status}</span>
                <span className="min-w-0 flex-1 truncate">{e.preview}</span>
              </div>
            );
          })}
        </div>
      </div>
      {pending > 0 ? (
        <button
          type="button"
          className="btn btn-sm pill-in absolute bottom-4 left-1/2 -translate-x-1/2"
          onClick={() => {
            v.scrollToIndex(events.length - 1, { align: "end", behavior: "smooth" });
            setPending(0);
          }}
        >
          {t("run.new_events", { n: pending })}
        </button>
      ) : null}
      {open ? <EventDrawer runID={runID} event={open} onClose={() => setOpen(null)} /> : null}
    </Card>
  );
}

function EventDrawer({ runID, event, onClose }: { runID: string; event: EventBody; onClose: () => void }) {
  const payload = useQuery({
    queryKey: ["run-event-payload", runID, event.seq],
    enabled: event.has_payload,
    queryFn: async () => {
      const r = await fetch(`/api/v1/runs/${runID}/events/${event.seq}/payload`);
      if (!r.ok) throw new Error(`${r.status}`);
      return JSON.stringify(await r.json(), null, 2);
    },
  });
  return (
    <aside
      aria-label={t("run.event")}
      className="drawer-in card absolute top-0 right-0 z-10 grid h-full w-full max-w-[36rem] grid-rows-[auto_1fr] gap-2 p-4"
    >
      <div className="flex items-center gap-3">
        <Badge tone={typeTone[event.type]}>{event.type}</Badge>
        <span className="t-value min-w-0 flex-1 truncate text-ink">{event.name}</span>
        <button type="button" className="btn btn-ghost btn-sm" aria-label={t("run.event.close")} onClick={onClose}>
          <X className="h-4 w-4" aria-hidden />
        </button>
      </div>
      <pre tabIndex={0} aria-label={t("run.event.payload")} className="control control-mono overflow-auto whitespace-pre-wrap">
        {event.has_payload ? (payload.data ?? (payload.error ? t("run.event.gone") : "…")) : event.preview}
      </pre>
    </aside>
  );
}
