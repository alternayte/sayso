import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setNonce } from "get-nonce";
import { ApiError } from "@/lib/api";
import { routeTree } from "./routeTree.gen";
import "./index.css";

// The server allows style elements only with the nonce of this page. Libraries that insert one,
// such as the scroll lock of dialogs, read it from get-nonce.
const nonce = document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content;
if (nonce) setNonce(nonce);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A 4xx answer does not change on a retry. A network error or a 5xx gets two more tries.
      retry: (count, error) => !(error instanceof ApiError && error.status < 500 && error.status > 0) && count < 2,
      staleTime: 10_000,
      refetchOnWindowFocus: false,
    },
  },
});

const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPreloadDelay: 50,
  defaultViewTransition: true,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
