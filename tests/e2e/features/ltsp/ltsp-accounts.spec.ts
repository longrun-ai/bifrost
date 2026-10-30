import { expect, test } from "../../core/fixtures/base.fixture";

test.use({ skipAutoLogin: true });

test.beforeEach(async ({ page }) => {
  let value = "sk-bf-fixture-current-key-with-at-least-32-characters";
  await page.route("**/api/session/is-auth-enabled", (route) =>
    route.fulfill({
      json: { inference_auth_enforced: true, is_auth_enabled: false, setup_required: false },
    }),
  );
  await page.route("**/api/governance/virtual-keys**", async (route) => {
    if (route.request().method() === "POST")
      value =
        route.request().postDataJSON().value ||
        "sk-bf-fixture-rotated-key-with-at-least-32-characters";
    const key = { id: "client-key", name: "ltsp-lan-clients", value, is_active: true };
    await route.fulfill({
      json: route.request().method() === "POST" ? { virtual_key: key } : { virtual_keys: [key] },
    });
  });
});

test("LTSP account editor reports the immediate funding probe", async ({ page }) => {
  let accounts: unknown[] = [];
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") accounts = route.request().postDataJSON().accounts;
    await route.fulfill({
      json: {
        accounts,
        keys: { openai: [], anthropic: [] },
        timezone: "UTC",
        access_key_name: "ltsp-lan-clients",
      },
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
  await expect(page.locator("#message")).toHaveText("上游明确报告余额不足");
  await expect(page.getByRole("button", { name: "已到账，立即重试" })).toBeEnabled();
  await page.reload();
  await expect(page.getByLabel("账号名称")).toHaveValue("Trial");
});

test("LTSP home explains client setup and keeps the account form open", async ({
  page,
  context,
}) => {
  await page.route("**/api/ltsp/accounts", async (route) => {
    await route.fulfill({
      json: { accounts: [], keys: {}, timezone: "UTC", access_key_name: "ltsp-lan-clients" },
    });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "客户端接入" })).toBeVisible();
  const origin = new URL(page.url()).origin;
  await expect(page.locator("#openai-url")).toHaveText(origin + "/openai");
  await expect(page.locator("#access-status")).toHaveText("下游鉴权已启用");
  await expect(page.locator("#openai-example")).not.toContainText("sk-bf-fixture-current");
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

test("LTSP home rotates and replaces the client key without discarding the account form", async ({
  page,
  context,
}) => {
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: { accounts: [], keys: {}, timezone: "UTC", access_key_name: "ltsp-lan-clients" },
    }),
  );
  await page.goto("/");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号名称").fill("Keep this form");
  await page.getByRole("button", { name: "生成并轮换", exact: true }).click();
  await expect(page.locator("#downstream-key")).toHaveValue(
    "sk-bf-fixture-rotated-key-with-at-least-32-characters",
  );
  await expect(page.getByLabel("账号名称")).toHaveValue("Keep this form");
  await page.getByText("手动设置新的接入 Key", { exact: true }).click();
  await page.locator("#custom-access").fill("dummy-api-key");
  await page.getByRole("button", { name: "保存并替换旧 Key" }).click();
  await expect(page.locator("#access-message")).toContainText("32–256");
  const custom = "sk-bf-fixture-manually-chosen-key-with-32-characters";
  await page.locator("#custom-access").fill(custom);
  await page.getByRole("button", { name: "保存并替换旧 Key" }).click();
  await expect(page.locator("#downstream-key")).toHaveValue(custom);
  await page.getByRole("button", { name: "显示", exact: true }).click();
  await expect(page.locator("#openai-example")).toContainText(custom);
  const secure = await page.evaluate(() => window.isSecureContext);
  if (secure) await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.locator('[data-copy="openai-example"]').click();
  await expect(page.locator('[data-copy="openai-example"]')).toHaveText("已复制");
  if (secure) expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(custom);
});