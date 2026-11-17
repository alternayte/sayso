import { createFileRoute } from "@tanstack/react-router";
import { PageHead } from "@/components/app-ui";
import { ReviewsList, validateReviewsSearch } from "@/components/reviews";
import { t } from "@/lib/copy";

export const Route = createFileRoute("/_app/reviews/")({ validateSearch: validateReviewsSearch, component: Reviews });

function Reviews() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <>
      <PageHead title={t("reviews.title")} hint={t("home.hint")} />
      <ReviewsList search={search} onSearch={(s) => void navigate({ search: s, replace: true })} />
    </>
  );
}
