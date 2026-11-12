import { createFileRoute } from "@tanstack/react-router";
import { PageHead } from "@/components/app-ui";

import { t } from "@/lib/copy";
import { Card } from "@/components/ui/card";

export const Route = createFileRoute("/_app/")({ component: Home });

function Home() {
  return (
    <>
      <PageHead title={t("home.title")} />
      <Card className="p-6">
        <p className="t-hint">{t("home.empty")}</p>
      </Card>
    </>
  );
}
