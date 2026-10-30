import { expect, test } from "../../core/fixtures/base.fixture";

test.use({ skipAutoLogin: true });

test("LTSP account editor reports the immediate funding probe", async ({ page }) => {
  let accounts: unknown[] = [];
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") accounts = route.request().postDataJSON().accounts;
    await route.fulfill({
      json: { accounts, keys: { openai: [], anthropic: [] }, timezone: "UTC" },
    });
  });
  let finishProbe: (() => void) | undefined;
  const probeStarted = new Promise<void>((resolve) => {
    page.route("**/api/ltsp/accounts/*/probe", async (route) => {
      resolve();
      await new Promise<void>((finish) => {
        finishProbe = finish;
      });
      await route.fulfill({ json: { recovered: false, result: "上游明确报告余额不足" } });
    });
  });
  await page.goto("/ltsp/accounts");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号名称").fill("Trial");
  await page.getByLabel("探测模型").fill("gpt-6.1-sol");
  await page.getByRole("button", { name: "已到账，立即重试" }).click();
  await probeStarted;
  await expect(page.getByRole("button", { name: "正在探测…" })).toBeDisabled();
  finishProbe?.();
  await expect(page.getByRole("status")).toHaveText("上游明确报告余额不足");
  await expect(page.getByRole("button", { name: "已到账，立即重试" })).toBeEnabled();
  await page.reload();
  await expect(page.getByLabel("账号名称")).toHaveValue("Trial");
});