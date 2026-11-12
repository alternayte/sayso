import { queryOptions } from "@tanstack/react-query";
import { getMe } from "@/client/sdk.gen";
import { isStatus } from "@/lib/api";

export type Me = { login: string; name: string; role: "admin" | "maintainer" | "viewer" };

/** meQuery loads the signed-in user, or null when nobody is signed in. */
export const meQuery = queryOptions({
  queryKey: ["me"],
  queryFn: async (): Promise<Me | null> => {
    try {
      const { data } = await getMe();
      return data as Me;
    } catch (e) {
      if (isStatus(e, 401)) return null;
      throw e;
    }
  },
  staleTime: 60_000,
});

const rank = { viewer: 1, maintainer: 2, admin: 3 } as const;

/** atLeast reports whether a role has the rights of min. */
export function atLeast(role: Me["role"], min: Me["role"]) {
  return rank[role] >= rank[min];
}

export async function signOut() {
  const res = await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  if (!res.ok && res.status !== 204) throw new Error(String(res.status));
}
