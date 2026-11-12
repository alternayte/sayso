import { useCallback, type KeyboardEvent, type PointerEvent } from "react";

/**
 * usePress sets data-pressed on pointerdown, before click, and clears it on release. Enter and
 * Space set the same state for keyboard users. Spread the handlers on the element with the
 * "press" class.
 */
export function usePress<T extends HTMLElement>() {
  const down = useCallback((e: PointerEvent<T> | KeyboardEvent<T>) => {
    if ("key" in e && e.key !== "Enter" && e.key !== " ") return;
    e.currentTarget.setAttribute("data-pressed", "");
  }, []);
  const up = useCallback((e: PointerEvent<T> | KeyboardEvent<T>) => {
    e.currentTarget.removeAttribute("data-pressed");
  }, []);
  return { onPointerDown: down, onPointerUp: up, onPointerLeave: up, onPointerCancel: up, onKeyDown: down, onKeyUp: up };
}
