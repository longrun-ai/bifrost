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

test("LTSP home explains client setup and keeps the account form open", async ({
  page,
  context,
}) => {
  await page.route("**/api/ltsp/accounts", async (route) => {
    await route.fulfill({ json: { accounts: [], keys: {}, timezone: "UTC" } });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "客户端接入" })).toBeVisible();
  const origin = new URL(page.url()).origin;
  await expect(page.locator("#openai-url")).toHaveText(origin + "/openai");
  await expect(page.locator("#openai-example")).toContainText('api_key="dummy-api-key"');
  await expect(page.locator("#anthropic-url")).toHaveText(origin + "/anthropic");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号名称").fill("Unsaved account");
  const popup = context.waitForEvent("page");
  await page.getByRole("link", { name: "调用统计" }).click();
  const stats = await popup;
  await stats.waitForURL("**/workspace/dashboard");
  await expect(page).toHaveURL(origin + "/");
  await expect(page.getByLabel("账号名称")).toHaveValue("Unsaved account");
  await stats.close();
  const links = await page
    .locator("a[href]")
    .evaluateAll((nodes) =>
      nodes.map((node) => ({ target: node.getAttribute("target"), rel: node.getAttribute("rel") })),
    );
  expect(
    links.every((link) => link.target === "_blank" && link.rel?.includes("noopener")),
  ).toBeTruthy();
  const secure = await page.evaluate(() => window.isSecureContext);
  if (secure) await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.locator('[data-copy="openai-example"]').click();
  await expect(page.locator('[data-copy="openai-example"]')).toHaveText("已复制");
  if (secure)
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(
      'base_url="' + origin + '/openai"',
    );
});