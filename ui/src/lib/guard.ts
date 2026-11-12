import { notFound } from "@tanstack/react-router";
import { atLeast, type Me } from "@/lib/session";

/** requireRole hides a route from a user whose role is too low (Appendix D). */
export function requireRole(min: Me["role"]) {
  return ({ context }: { context: { me: Me } }) => {
    if (!atLeast(context.me.role, min)) throw notFound();
  };
}
