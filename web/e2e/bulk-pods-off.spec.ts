// matrix: chrome-only because it is a Make-tab generation flow of the Owner
// ops: list_pods, get_provider_status
// M6 bulk (m6-bulk C398.6) on the ordinary e2e server, which has no pod backend: production's state. The Owner is told plainly that pods are off
// (never a checkbox that would be refused), and the Jobs page still answers list_pods with an empty list.
import { expect, test } from "./fixtures";
import { draftsIntent } from "./bulkApi";

test("with no pod backend the Generate page says pods are off and offers no pod option", async ({ page, request }) => {
  await draftsIntent(page, request, 3, 10);
  await page.goto("/human/generate");
  await expect(page.getByTestId("estimate")).toContainText("30 takes");
  await expect(page.getByTestId("bulk-off")).toContainText("off until the operator has set up the pod backend");
  await expect(page.getByTestId("bulk-toggle")).toHaveCount(0);
  await expect(page.getByTestId("pod-estimate")).toHaveCount(0);
});

test("the Jobs page says pods are off and lists none", async ({ page }) => {
  await page.goto("/jobs");
  await expect(page.getByTestId("pods-off")).toContainText("stay off until the operator");
  await expect(page.getByTestId("pods-empty")).toBeVisible();
});

test("a small batch shows no pod text at all", async ({ page, request }) => {
  await draftsIntent(page, request, 1, 3);
  await page.goto("/human/generate");
  await expect(page.getByTestId("estimate")).toContainText("3 takes");
  await expect(page.getByTestId("bulk-option")).toHaveCount(0);
});
