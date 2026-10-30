import { expect, test } from "../../core/fixtures/base.fixture";

test.use({ skipAutoLogin: true });

const schedules = [
  { provider: "openai", timezone: "UTC", cutoff: "20:00", probe_model: "" },
  { provider: "anthropic", timezone: "UTC", cutoff: "20:00", probe_model: "" },
];

test.beforeEach(async ({ page }) => {
  let value = "sk-bf-fixture-current-key-with-at-least-32-characters";
  await page.route("**/api/session/is-auth-enabled", (route) =>
    route.fulfill({
      json: {
        inference_auth_enforced: true,
        is_auth_enabled: false,
        setup_required: false,
      },
    }),
  );
  await page.route("**/api/governance/virtual-keys**", async (route) => {
    if (route.request().method() === "POST")
      value =
        route.request().postDataJSON().value ||
        "sk-bf-fixture-rotated-key-with-at-least-32-characters";
    const key = {
      id: "client-key",
      name: "ltsp-lan-clients",
      value,
      is_active: true,
    };
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
        provider_schedules: schedules,
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
      await route.fulfill({
        json: { recovered: false, result: "上游明确报告余额不足" },
      });
    });
  });
  await page.goto("/ltsp/accounts");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号组名称").fill("Trial");
  await page.getByLabel("OpenAI 探测模型", { exact: true }).fill("gpt-6.1-sol");
  await page.getByRole("button", { name: "已到账，立即重试" }).click();
  await probeStarted;
  await expect(page.getByRole("button", { name: "正在探测…" })).toBeDisabled();
  finishProbe?.();
  await expect(page.locator("#message")).toHaveText("上游明确报告余额不足");
  await expect(page.getByRole("button", { name: "已到账，立即重试" })).toBeEnabled();
  await page.reload();
  await expect(page.getByLabel("账号组名称")).toHaveValue("Trial");
});

test("LTSP home explains client setup and keeps the account form open", async ({
  page,
  context,
}) => {
  await page.route("**/api/ltsp/accounts", async (route) => {
    await route.fulfill({
      json: {
        accounts: [],
        keys: {},
        provider_schedules: schedules,
        access_key_name: "ltsp-lan-clients",
      },
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
  await page.getByLabel("账号组名称").fill("Unsaved account");
  const popup = context.waitForEvent("page");
  await page.getByRole("link", { name: "调用统计" }).click();
  const stats = await popup;
  await stats.waitForURL("**/workspace/dashboard");
  await expect(page).toHaveURL(origin + "/");
  await expect(page.getByLabel("账号组名称")).toHaveValue("Unsaved account");
  await stats.close();
  const links = await page.locator("a[href]").evaluateAll((nodes) =>
    nodes.map((node) => ({
      target: node.getAttribute("target"),
      rel: node.getAttribute("rel"),
    })),
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
  await page.setViewportSize({ width: 390, height: 844 });
  expect((await page.locator("#downstream-key").boundingBox())?.width).toBeGreaterThan(240);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
  ).toBeTruthy();
});

test("LTSP home rotates and replaces the client key without discarding the account form", async ({
  page,
  context,
}) => {
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: {
        accounts: [],
        keys: {},
        provider_schedules: schedules,
        access_key_name: "ltsp-lan-clients",
      },
    }),
  );
  await page.goto("/");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号组名称").fill("Keep this form");
  await page.getByRole("button", { name: "生成并轮换", exact: true }).click();
  await expect(page.locator("#downstream-key")).toHaveValue(
    "sk-bf-fixture-rotated-key-with-at-least-32-characters",
  );
  await expect(page.getByLabel("账号组名称")).toHaveValue("Keep this form");
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

test("LTSP tables assign native accounts only to groups of the same provider", async ({ page }) => {
  let accounts = [
    {
      id: "o-group",
      name: "OpenAI group",
      provider: "openai",
      key_ids: [] as string[],
      refresh_date: "2026-10-30",
      cycle: "monthly",
      balance: "unknown",
    },
    {
      id: "c-group",
      name: "Claude group",
      provider: "anthropic",
      key_ids: [] as string[],
      refresh_date: "2026-10-30",
      cycle: "monthly",
      balance: "unknown",
    },
  ];
  let providerSchedules = structuredClone(schedules);
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      accounts = body.accounts;
      providerSchedules = body.provider_schedules;
    }
    await route.fulfill({
      json: {
        accounts,
        provider_schedules: providerSchedules,
        access_key_name: "ltsp-lan-clients",
        keys: {
          openai: [{ id: "o-key", name: "OpenAI account", enabled: true }],
          anthropic: [{ id: "c-key", name: "Claude account", enabled: true }],
        },
      },
    });
  });
  await page.goto("/");
  const groups = page.getByRole("table", { name: "账号组", exact: true });
  await expect(groups.locator(".balance")).toHaveText(["未知", "未知"]);
  await expect(page.getByText("真实余额：未知", { exact: true })).toHaveCount(0);
  const openai = page.getByLabel("所属账号组：OpenAI account", { exact: true });
  await expect(openai).toHaveValue("");
  await expect(openai.locator("option")).toHaveText(["", "OpenAI group"]);
  await expect(page.getByLabel("所属账号组：Claude account").locator("option")).toHaveText([
    "",
    "Claude group",
  ]);
  await openai.selectOption("o-group");
  await expect(groups.locator('[data-id="o-group"] select[name=provider]')).toBeDisabled();
  await page.getByLabel("OpenAI 时区", { exact: true }).fill("America/Los_Angeles");
  await page.getByLabel("OpenAI 刷新截止时间", { exact: true }).fill("20:30");
  await page.getByLabel("OpenAI 探测模型", { exact: true }).fill("preferred-model");
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(page.locator("#message")).toHaveText("配置已保存。");
  expect(accounts[0].key_ids).toEqual(["o-key"]);
  expect(accounts[1].key_ids).toEqual([]);
  expect(providerSchedules[0]).toMatchObject({
    timezone: "America/Los_Angeles",
    cutoff: "20:30",
    probe_model: "preferred-model",
  });
  await page.getByLabel("所属账号组：OpenAI account").selectOption("");
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(page.locator("#message")).toHaveText("配置已保存。");
  expect(accounts[0].key_ids).toEqual([]);
  await expect(groups.locator('[data-id="o-group"] select[name=provider]')).toBeEnabled();
});

test("LTSP probe refreshes the shared provider model discovered by the gateway", async ({
  page,
}) => {
  let providerSchedules = structuredClone(schedules);
  const accounts = [
    {
      id: "o-group",
      name: "OpenAI group",
      provider: "openai",
      key_ids: ["o-key"],
      refresh_date: "2026-10-30",
      cycle: "monthly",
    },
  ];
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT")
      providerSchedules = route.request().postDataJSON().provider_schedules;
    await route.fulfill({
      json: {
        accounts,
        provider_schedules: providerSchedules,
        access_key_name: "ltsp-lan-clients",
        keys: {
          openai: [{ id: "o-key", name: "OpenAI account", enabled: true }],
        },
      },
    });
  });
  await page.route("**/api/ltsp/accounts/*/probe", async (route) => {
    providerSchedules[0].probe_model = "working-catalog-model";
    await route.fulfill({
      json: { recovered: true, result: "调用成功，额度可用" },
    });
  });
  await page.goto("/");
  await expect(page.getByLabel("OpenAI 探测模型", { exact: true })).toHaveValue("");
  await page.getByRole("button", { name: "已到账，立即重试", exact: true }).click();
  await expect(page.getByLabel("OpenAI 探测模型", { exact: true })).toHaveValue(
    "working-catalog-model",
  );
  await expect(page.getByRole("button", { name: "添加账号组", exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: "保存配置", exact: true })).toBeEnabled();
});

test("LTSP updates preserve DOM identity, focus, selection, expansion and scroll", async ({
  page,
}) => {
  let accounts = [
    {
      id: "o-group",
      name: "OpenAI group",
      provider: "openai",
      key_ids: [] as string[],
      refresh_date: "2026-10-30",
      cycle: "monthly",
    },
  ];
  let releaseSave: (() => void) | undefined;
  let saveStarted: (() => void) | undefined;
  const started = new Promise<void>((resolve) => {
    saveStarted = resolve;
  });
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") {
      accounts = route.request().postDataJSON().accounts;
      saveStarted?.();
      await new Promise<void>((resolve) => {
        releaseSave = resolve;
      });
    }
    await route.fulfill({
      json: {
        accounts,
        provider_schedules: schedules,
        access_key_name: "ltsp-lan-clients",
        keys: { openai: [{ id: "o-key", name: "OpenAI account", enabled: true }] },
      },
    });
  });
  await page.setViewportSize({ width: 900, height: 700 });
  await page.goto("/");
  const assignment = page.getByLabel("所属账号组：OpenAI account", { exact: true });
  await assignment.focus();
  const stable = await page.evaluateHandle(() => {
    const input = document.querySelector<HTMLInputElement>("#accounts input[name=name]")!;
    input.setSelectionRange(1, 7);
    const wrapper = document.querySelector<HTMLElement>("#accounts")!;
    wrapper.scrollLeft = 70;
    const details = [...document.querySelectorAll("details")].find((el) =>
      el.textContent?.includes("高级接入设置与管理边界"),
    )!;
    details.open = true;
    const changes: string[] = [];
    const observer = new MutationObserver((records) => {
      for (const record of records)
        if (record.addedNodes.length || record.removedNodes.length)
          changes.push(record.target.nodeName);
    });
    for (const el of document.querySelectorAll("#accounts, #key-assignments, #provider-settings"))
      observer.observe(el, { childList: true, subtree: true });
    return {
      input,
      wrapper,
      details,
      summary: details.querySelector("summary")!,
      group: input.closest("tr")!,
      select: document.querySelector<HTMLSelectElement>("#key-assignments select")!,
      providerInput: document.querySelector("#provider-settings input"),
      scroll: wrapper.scrollLeft,
      scrollY: window.scrollY,
      changes,
      observer,
    };
  });
  await assignment.selectOption("o-group");
  expect(
    await stable.evaluate(
      (saved) => document.activeElement === saved.select && saved.select.isConnected,
    ),
  ).toBeTruthy();
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await started;
  await stable.evaluate((saved) => {
    saved.summary.focus({ preventScroll: true });
    saved.scrollY = window.scrollY;
  });
  releaseSave?.();
  await expect(page.locator("#message")).toHaveText("配置已保存。");
  const result = await stable.evaluate((saved) => ({
    sameInput: saved.input === document.querySelector("#accounts input[name=name]"),
    sameGroup: saved.group === document.querySelector('#accounts tr[data-id="o-group"]'),
    sameSelect: saved.select === document.querySelector("#key-assignments select"),
    sameProvider: saved.providerInput === document.querySelector("#provider-settings input"),
    focused: document.activeElement === saved.summary,
    selection: [saved.input.selectionStart, saved.input.selectionEnd],
    expanded: saved.details.open,
    scroll: saved.wrapper.scrollLeft === saved.scroll && window.scrollY === saved.scrollY,
    structuralChanges: saved.changes,
  }));
  expect(result).toEqual({
    sameInput: true,
    sameGroup: true,
    sameSelect: true,
    sameProvider: true,
    focused: true,
    selection: [1, 7],
    expanded: true,
    scroll: true,
    structuralChanges: [],
  });
  await stable.evaluate((saved) => saved.observer.disconnect());
});