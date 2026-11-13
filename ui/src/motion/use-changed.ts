import { useEffect, useRef, useState } from "react";

/** useChanged is true for one animation after the value changes, not on the first render. */
export function useChanged(value: unknown, ms = 600) {
  const prev = useRef(value);
  const [changed, setChanged] = useState(false);
  useEffect(() => {
    if (Object.is(prev.current, value)) return;
    prev.current = value;
    setChanged(true);
    const t = setTimeout(() => setChanged(false), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return changed;
}
