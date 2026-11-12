import { createFileRoute, Link } from "@tanstack/react-router";
import { CenteredPage } from "@/app/brand";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { t, type CopyKey } from "@/lib/copy";
import { Card } from "@/components/ui/card";

type Search = { error?: string };

export const Route = createFileRoute("/signin")({
  validateSearch: (s: Record<string, unknown>): Search => (typeof s.error === "string" ? { error: s.error } : {}),
  component: SignIn,
});

const errors = ["not_member", "app_missing", "bad_state", "github", "internal"];

function SignIn() {
  const { error } = Route.useSearch();
  return (
    <CenteredPage>
      <Card className="p-6">
        <h1 className="t-title">{t("signin.title")}</h1>
        <p className="mt-2">{t("signin.body")}</p>
        {error ? (
          <Alert tone="danger" className="mt-4">
            {t((errors.includes(error) ? `signin.error.${error}` : "signin.error.internal") as CopyKey)}
          </Alert>
        ) : null}
        <Button asChild variant="primary" className="mt-5 w-full">
          <a href="/auth/github/login">{t("signin.button")}</a>
        </Button>
      </Card>
      <Link to="/setup" className="link t-hint mt-4 inline-block">
        {t("signin.setup")}
      </Link>
    </CenteredPage>
  );
}
