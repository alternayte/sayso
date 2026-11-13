import { useLayoutEffect, useRef, type RefObject } from "react";

/**
 * useIndicator moves one indicator element to the active item of a group with a FLIP transform.
 * The group element is the positioning parent. The active item has the data attribute that the
 * selector names, for example [aria-current="page"] or [aria-checked="true"].
 */
export function useIndicator<T extends HTMLElement>(group: RefObject<T | null>, selector: string, deps: unknown[]) {
  const indicator = useRef<HTMLSpanElement>(null);
  const first = useRef(true);
  useLayoutEffect(() => {
    const g = group.current;
    const ind = indicator.current;
    if (!g || !ind) return;
    const place = () => {
      const active = g.querySelector<HTMLElement>(selector);
      if (!active) {
        ind.style.opacity = "0";
        return;
      }
      const gr = g.getBoundingClientRect();
      const ar = active.getBoundingClientRect();
      if (first.current) ind.setAttribute("data-first", "");
      ind.style.opacity = "1";
      ind.style.width = `${ar.width}px`;
      ind.style.height = `${ar.height}px`;
      ind.style.transform = `translate(${ar.left - gr.left + g.scrollLeft}px, ${ar.top - gr.top + g.scrollTop}px)`;
      if (first.current) {
        void ind.offsetWidth;
        ind.removeAttribute("data-first");
        first.current = false;
      }
    };
    place();
    const ro = new ResizeObserver(place);
    ro.observe(g);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return indicator;
}
