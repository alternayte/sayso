import { createFileRoute } from "@tanstack/react-router";
import { PageHead } from "@/components/app-ui";
import { ChecklistGate } from "@/components/checklist";
import { ReviewsList, validateReviewsSearch } from "@/components/reviews";
import { t } from "@/lib/copy";

export const Route = createFileRoute("/_app/")({ validateSearch: validateReviewsSearch, component: Home });

function Home() {
  const { me } = Route.useRouteContext();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <ChecklistGate admin={me.role === "admin"}>
      <PageHead title={t("home.title")} hint={t("home.hint")} />
      <ReviewsList search={search} onSearch={(s) => void navigate({ search: s, replace: true })} />
    </ChecklistGate>
  );
}
