import { expect, test } from "../../core/fixtures/base.fixture";

test.use({ skipAutoLogin: true });

const schedules = [
  { provider: "openai", timezone: "UTC", cutoff: "20:00", probe_model: "" },
  { provider: "anthropic", timezone: "UTC", cutoff: "20:00", probe_model: "" },
];

test.beforeEach(async ({ page }) => {
  await page.route("**/api/logs/rankings?**", (route) => route.fulfill({ json: { rankings: [] } }));
  await page.route("**/api/models?**", (route) =>
    route.fulfill({ json: { models: [], total: 0 } }),
  );
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

test("LTSP tabs keep draft nodes and use refreshable REST paths", async ({ page }) => {
  let accounts: unknown[] = [];
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") accounts = route.request().postDataJSON().accounts;
    await route.fulfill({
      json: {
        accounts,
        keys: {},
        provider_schedules: schedules,
        access_key_name: "ltsp-lan-clients",
      },
    });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("LTSP LLM API 网关");
  await expect(page.getByRole("tab")).toHaveCount(4, { timeout: 1000 });
  await expect(page).toHaveURL(/\/ltsp\/groups$/);
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await page.locator("#openai-python summary").click();
  await page.locator("#client-config a[data-tab-link]").click();
  await expect(page).toHaveURL(/\/ltsp\/accounts$/);
  await expect(page.getByRole("tab", { name: "API Key", exact: true })).toBeFocused();
  await page.goBack();
  await expect(page.locator("#openai-python")).toHaveJSProperty("open", true);
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await expect(page.locator("#groups-empty a[data-tab-link]")).toHaveAttribute(
    "href",
    "/ltsp/accounts",
  );
  await page.locator("#add").click();
  const name = page.getByLabel("账号组名称");
  await name.fill("Tab draft");
  const node = await name.evaluateHandle((el) => el);
  await page.locator("#account-groups > p a[data-tab-link]").click();
  await page.getByLabel("OpenAI 扣款（额度刷新）截止时间", { exact: true }).fill("21:30");
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await expect(page.locator("#openai-python")).toHaveJSProperty("open", true);
  await page.goBack();
  await expect(page).toHaveURL(/\/ltsp\/upstreams$/);
  await expect(page.getByLabel("OpenAI 扣款（额度刷新）截止时间", { exact: true })).toHaveValue(
    "21:30",
  );
  await page.goBack();
  await expect(page).toHaveURL(/\/ltsp\/groups$/);
  await expect(name).toHaveValue("Tab draft");
  expect(
    await node.evaluate((el) => el === document.querySelector("#accounts input[name=name]")),
  ).toBeTruthy();
  await page.locator("#save").click();
  await expect(page.locator("#dirty-dot")).toBeHidden();
  await page.reload();
  await expect(page).toHaveURL(/\/ltsp\/groups$/);
  await expect(name).toHaveValue("Tab draft");
  for (const [path, label] of [
    ["client", "客户端接入"],
    ["groups", "账号组"],
    ["accounts", "API Key"],
    ["upstreams", "上游配置"],
  ]) {
    await page.goto("/ltsp/" + path);
    await expect(page.getByRole("tab", { name: label, exact: true })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await expect(page.getByRole("tabpanel")).toHaveCount(1);
    await expect(page).toHaveTitle(label + " · LTSP LLM API 网关");
  }
  const first = page.getByRole("tab", { name: "客户端接入", exact: true });
  await first.focus();
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(/\/ltsp\/groups$/);
  await expect(page.getByRole("tab", { name: "账号组", exact: true })).toBeFocused();
});

test("LTSP client model lists split enabled-key coverage and preserve model nodes", async ({
  page,
}) => {
  let revision = 0;
  const group = {
    id: "g",
    name: "Group",
    provider: "openai",
    key_ids: ["k1", "k2"],
    refresh_date: "2026-11-01",
    cycle: "monthly",
  };
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: {
        accounts: [group],
        keys: {
          openai: [
            { id: "k1", enabled: true },
            { id: "k2", enabled: true },
            { id: "disabled", enabled: false },
          ],
          anthropic: revision === 2 ? [] : [{ id: "a", enabled: true }],
        },
        provider_schedules: schedules,
      },
    }),
  );
  const offsets: number[] = [];
  await page.route("**/api/models?**", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (query.get("provider") === "anthropic") {
      expect(query.get("keys")).toBe("a");
      await route.fulfill({
        json: { models: [{ name: "common-a", accessible_by_keys: ["a"] }], total: 1 },
      });
    } else {
      expect(query.get("keys")).toBe("k1,k2");
      if (revision === 2)
        return route.fulfill({ status: 503, json: { error: "catalog unavailable" } });
      const offset = Number(query.get("offset"));
      offsets.push(offset);
      const models = [
        { name: "common-b", accessible_by_keys: ["k1", "k2", "disabled"] },
        { name: "partial", accessible_by_keys: revision ? ["k1", "k2"] : ["k1"] },
        { name: "common-a", accessible_by_keys: revision ? ["k1"] : ["k1", "k2"] },
        { name: "disabled-only", accessible_by_keys: ["disabled"] },
      ];
      await route.fulfill({
        json: { models: models.slice(offset, offset + 2), total: models.length },
      });
    }
  });
  await page.goto("/ltsp/client");
  const openai = page.locator('[data-model-provider="openai"]');
  const anthropic = page.locator('[data-model-provider="anthropic"]');
  await expect(openai.locator(".common-models li")).toHaveText(["common-a", "common-b"]);
  await expect(openai.locator(".partial-models li")).toHaveText(["partial"]);
  await expect(anthropic.locator(".common-models li")).toHaveText(["common-a"]);
  await expect(anthropic.locator(".partial-empty")).toBeVisible();
  expect(offsets).toEqual([0, 2]);
  const node = await openai.locator('[data-model="common-a"]').evaluateHandle((el) => el);
  await page.locator("#openai-python summary").click();
  revision = 1;
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByLabel("账号组名称").fill("Updated group");
  await page.locator("#save").click();
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await expect(openai.locator(".common-models li")).toHaveText(["common-b", "partial"]);
  await expect(openai.locator(".partial-models li")).toHaveText(["common-a"]);
  expect(
    await node.evaluate(
      (el) => el === document.querySelector('[data-model-provider="openai"] .partial-models li'),
    ),
  ).toBeTruthy();
  await expect(page.locator("#openai-python")).toHaveJSProperty("open", true);
  revision = 2;
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByLabel("账号组名称").fill("Another update");
  await page.locator("#save").click();
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await expect(openai.locator(".model-status")).toContainText("catalog unavailable");
  await expect(openai.locator("li")).toHaveCount(0);
  await expect(anthropic.locator(".model-status")).toHaveText("尚无已启用 Key。");
  await expect(anthropic.locator("li")).toHaveCount(0);
});

test("LTSP client keeps common models upfront and other models in a stable scroll region", async ({
  page,
}) => {
  let failRanking = false;
  const models = [
    "babbage-002",
    "gpt-4.1",
    "gpt-5.6-sol",
    "gpt-6-sol",
    "gpt-6.1-sol",
    "gpt-6.1-terra",
    "gpt-5.5",
    "gpt-5-mini",
    "gpt-5",
    "gpt-6.1-sol-2026-10-01",
    "whisper-1",
    ...Array.from({ length: 40 }, (_, i) => `text-embedding-fixture-${i}`),
  ];
  const group = {
    id: "g",
    name: "Group",
    provider: "openai",
    key_ids: ["k"],
    refresh_date: "2026-11-01",
    cycle: "monthly",
  };
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: {
        accounts: [group],
        keys: { openai: [{ id: "k", enabled: true }] },
        provider_schedules: schedules,
      },
    }),
  );
  await page.route("**/api/models?**", (route) =>
    route.fulfill({
      json: {
        models: models.map((name) => ({ name, accessible_by_keys: ["k"] })),
        total: models.length,
      },
    }),
  );
  await page.route("**/api/logs/rankings?**", (route) => {
    const q = new URL(route.request().url()).searchParams;
    expect(q.get("selected_key_ids")).toBe("k");
    expect(q.get("status")).toBe("success");
    expect(Date.parse(q.get("end_time")!) - Date.parse(q.get("start_time")!)).toBe(7 * 86400000);
    return route.fulfill(
      failRanking
        ? { status: 503, json: { error: "stats unavailable" } }
        : {
            json: {
              rankings: [
                { model: "local-alias", canonical_model_name: "gpt-5.6-sol", total_requests: 20 },
                { model: "whisper-1", total_requests: 3 },
              ],
            },
          },
    );
  });
  await page.setViewportSize({ width: 900, height: 700 });
  await page.goto("/ltsp/client");
  const container = page.locator('[data-model-provider="openai"]');
  await expect(container.locator(".common-models li")).toHaveCount(8);
  await expect(container.locator(".common-models li").nth(0)).toHaveText("gpt-5.6-sol");
  await expect(container.locator(".common-models li").nth(1)).toHaveText("whisper-1");
  await expect(container.locator(".common-count")).toHaveText(`（${models.length}）`);
  const details = container.locator(".more-models");
  await expect(details).not.toHaveAttribute("open");
  await expect(container.locator(".other-common-models")).toBeHidden();
  await details.locator("summary").click();
  const scroll = container.locator(".model-overflow");
  await expect(scroll).toBeVisible();
  const stable = await scroll.evaluateHandle((el) => {
    el.scrollTop = 120;
    return { el, row: el.querySelector('[data-model="babbage-002"]'), top: el.scrollTop };
  });
  expect(await scroll.evaluate((el) => el.scrollHeight > el.clientHeight)).toBeTruthy();
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByLabel("账号组名称").fill("Saved group");
  await page.locator("#save").click();
  await expect(page.locator("#dirty-dot")).toBeHidden();
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await expect(details).toHaveAttribute("open", "");
  expect(
    await stable.evaluate(
      ({ el, row, top }) =>
        el.isConnected &&
        el.querySelector('[data-model="babbage-002"]') === row &&
        el.scrollTop === top,
    ),
  ).toBeTruthy();
  failRanking = true;
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByLabel("账号组名称").fill("Fallback order");
  await page.locator("#save").click();
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await expect(container.locator(".common-order-note")).toHaveText(
    "通用、代码模型优先，日期版本与专用模型在后。",
  );
  await expect(container.locator(".common-models li").first()).toHaveText("gpt-6.1-terra");
  await expect(container.locator(".model-list li")).toHaveCount(models.length);
});

test("LTSP account editor reports the immediate funding probe", async ({ page }) => {
  let accounts: unknown[] = [];
  let providerSchedules = structuredClone(schedules);
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      if (body.accounts) accounts = body.accounts;
      if (body.provider_schedules) providerSchedules = body.provider_schedules;
    }
    await route.fulfill({
      json: {
        accounts,
        provider_schedules: providerSchedules,
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
  await page.goto("/ltsp/groups");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号组名称").fill("Trial");
  await page.getByRole("tab", { name: "上游配置", exact: true }).click();
  await page.getByLabel("OpenAI 探测模型", { exact: true }).fill("gpt-6.1-sol");
  await page.locator("#save-provider").click();
  await expect(page.locator("#provider-dirty-dot")).toBeHidden();
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.locator("#save").click();
  await expect(page.locator("#dirty-dot")).toBeHidden();
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
  await page.goto("/ltsp/client");
  await expect(page.getByRole("tab", { name: "客户端接入", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  const origin = new URL(page.url()).origin;
  await expect(page.locator("#openai-url")).toHaveText(origin + "/openai");
  await expect(page.locator("#access-status")).toHaveCount(0);
  await expect(page.locator("#openai-example")).not.toContainText("sk-bf-fixture-current");
  await expect(page.locator("#anthropic-url")).toHaveText(origin + "/anthropic");
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号组名称").fill("Unsaved account");
  const popup = context.waitForEvent("page");
  await page.getByRole("link", { name: "调用统计" }).click();
  const stats = await popup;
  await stats.waitForURL("**/workspace/dashboard");
  await expect(page).toHaveURL(origin + "/ltsp/groups");
  await expect(page.getByLabel("账号组名称")).toHaveValue("Unsaved account");
  await stats.close();
  const links = await page
    .locator('a[href]:not([role="tab"]):not([data-tab-link])')
    .evaluateAll((nodes) =>
      nodes.map((node) => ({
        target: node.getAttribute("target"),
        rel: node.getAttribute("rel"),
      })),
    );
  expect(
    links.every((link) => link.target === "_blank" && link.rel?.includes("noopener")),
  ).toBeTruthy();
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  const secure = await page.evaluate(() => window.isSecureContext);
  if (secure) await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  for (const [label, value] of [
    ["复制 Key", "sk-bf-fixture-current-key-with-at-least-32-characters"],
    ["复制 OpenAI 地址", origin + "/openai"],
  ]) {
    const button = page.getByRole("button", { name: label, exact: true });
    const icon = await button.locator("svg").evaluateHandle((el) => el);
    await button.click();
    await expect(button.locator(".copy-symbol")).toBeHidden();
    await expect(button.locator(".copied-symbol")).toBeVisible();
    await expect(button).toHaveAttribute("title", "已复制");
    if (secure) expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(value);
    await expect(button.locator(".copy-symbol")).toBeVisible();
    expect(await icon.evaluate((el) => el.isConnected)).toBeTruthy();
  }
  await expect(page.locator("#openai-example")).toBeHidden();
  await page.locator("#openai-python summary").click();
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

test("LTSP reports invalid inference-auth config while retaining the only client-key controls", async ({
  page,
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
  await page.route("**/api/session/is-auth-enabled", (route) =>
    route.fulfill({
      json: {
        inference_auth_enforced: false,
        is_auth_enabled: false,
      },
    }),
  );
  await page.goto("/ltsp/client");
  await expect(page.locator("#access-message")).toContainText("必须启用下游鉴权");
  await expect(page.locator("#access-message")).toHaveClass("error");
  await expect(page.locator("#downstream-key")).toBeVisible();
  await expect(page.locator("#rotate-access")).toBeVisible();
  await expect(page.locator("#access-status")).toHaveCount(0);
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
  await page.goto("/ltsp/groups");
  await page.getByRole("button", { name: "添加账号组" }).click();
  await page.getByLabel("账号组名称").fill("Keep this form");
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await page.getByRole("button", { name: "生成新 Key 并轮换", exact: true }).click();
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
  await page.getByRole("button", { name: "显示 Key", exact: true }).click();
  const eye = await page.locator("#show-access svg").evaluateHandle((el) => el);
  await expect(page.locator("#show-access .eye-slash")).toBeVisible();
  await page.getByRole("button", { name: "隐藏 Key", exact: true }).click();
  await expect(page.locator("#show-access .eye-slash")).toBeHidden();
  expect(
    await eye.evaluate((el) => el === document.querySelector("#show-access svg")),
  ).toBeTruthy();
  await page.getByRole("button", { name: "显示 Key", exact: true }).click();
  await expect(page.locator("#openai-example")).toContainText(custom);
  await page.locator("#openai-python summary").click();
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
      if (body.accounts) accounts = body.accounts;
      if (body.provider_schedules) providerSchedules = body.provider_schedules;
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
  await page.route("**/api/ltsp/assignments", async (route) => {
    const body = route.request().postDataJSON();
    for (const group of accounts) group.key_ids = group.key_ids.filter((id) => id !== body.key_id);
    accounts.find((g) => g.id === body.group_id)?.key_ids.push(body.key_id);
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
  await page.goto("/ltsp/accounts");
  const groups = page.locator("#account-groups table");
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
  await page.getByRole("tab", { name: "上游配置", exact: true }).click();
  await page.getByLabel("OpenAI 时区类型", { exact: true }).selectOption("region");
  await page.getByLabel("OpenAI 地区时区", { exact: true }).fill("America/Los_Angeles");
  await page.getByLabel("OpenAI 扣款（额度刷新）截止时间", { exact: true }).fill("20:30");
  await page.getByLabel("OpenAI 探测模型", { exact: true }).fill("preferred-model");
  await page.locator("#save-provider").click();
  await expect(page.locator("#message")).toHaveText("上游配置已保存。");
  expect(accounts[0].key_ids).toEqual(["o-key"]);
  expect(accounts[1].key_ids).toEqual([]);
  expect(providerSchedules[0]).toMatchObject({
    timezone: "America/Los_Angeles",
    cutoff: "20:30",
    probe_model: "preferred-model",
  });
  await page.getByRole("tab", { name: "API Key", exact: true }).click();
  await page.getByLabel("所属账号组：OpenAI account").selectOption("");
  await expect(page.locator("#account-message")).toHaveText("API Key 归组已保存。");
  await expect(page.getByLabel("所属账号组：OpenAI account")).toHaveValue("");
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
  await page.goto("/ltsp/groups");
  await expect(page.getByLabel("OpenAI 探测模型", { exact: true })).toHaveValue("");
  await page.getByRole("button", { name: "已到账，立即重试", exact: true }).click();
  await expect(page.getByLabel("OpenAI 探测模型", { exact: true })).toHaveValue(
    "working-catalog-model",
  );
  await expect(page.getByRole("button", { name: "添加账号组", exact: true })).toBeEnabled();
  await expect(page.locator("#save")).toBeDisabled();
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
      accounts = route
        .request()
        .postDataJSON()
        .accounts.map((a: (typeof accounts)[number]) => ({
          ...a,
          key_ids: accounts.find((old) => old.id === a.id)?.key_ids || [],
        }));
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
  await page.route("**/api/ltsp/assignments", async (route) => {
    const body = route.request().postDataJSON();
    accounts[0].key_ids = body.group_id ? [body.key_id] : [];
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
  await page.goto("/ltsp/groups");
  await page.getByLabel("账号组名称").fill("Edited group");
  await page.getByRole("tab", { name: "API Key", exact: true }).click();
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
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.locator("#save").click();
  await started;
  await page.getByRole("tab", { name: "客户端接入", exact: true }).click();
  await stable.evaluate((saved) => {
    saved.summary.focus({ preventScroll: true });
    saved.scrollY = window.scrollY;
  });
  releaseSave?.();
  await expect(page.locator("#message")).toHaveText("账号组配置已保存。");
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

test("LTSP compact controls keep examples folded, details floating and dirty state reversible", async ({
  page,
}) => {
  let group = {
    id: "g",
    name: "Group",
    provider: "openai",
    key_ids: [],
    refresh_date: "2026-10-30",
    cycle: "monthly",
    retry_start: "2026-10-30T20:00:00Z",
    retry_end: "2026-11-01T20:00:00Z",
  };
  let prefs = structuredClone(schedules),
    fail = false;
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") {
      if (fail) {
        await route.fulfill({ status: 500, json: { error: "fixture write failed" } });
        return;
      }
      const body = route.request().postDataJSON();
      if (body.accounts) group = { ...group, ...body.accounts[0] };
      if (body.provider_schedules) prefs = body.provider_schedules;
    }
    await route.fulfill({
      json: {
        accounts: [group],
        keys: {},
        provider_schedules: prefs,
        access_key_name: "ltsp-lan-clients",
      },
    });
  });
  await page.goto("/ltsp/client");
  await expect(page.getByText("OpenAI 兼容客户端 Base URL", { exact: true })).toBeVisible();
  await expect(page.locator("#openai-example")).toBeHidden();
  await expect(page.locator("#anthropic-example")).toBeHidden();
  await page.locator("#openai-python summary").click();
  await expect(page.locator("#openai-python")).toContainText("/chat/completions");
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  expect(
    await page
      .locator("#account-groups")
      .evaluate((el) => el.contains(document.querySelector("#save"))),
  ).toBeTruthy();
  expect(
    await page
      .locator("#native-accounts")
      .getByRole("button", { name: "保存配置", exact: true })
      .count(),
  ).toBe(0);
  expect(
    await page
      .locator("#add")
      .evaluate(
        (el) =>
          !!(
            document.querySelector("#accounts")!.compareDocumentPosition(el) &
            Node.DOCUMENT_POSITION_FOLLOWING
          ),
      ),
  ).toBeTruthy();
  const row = page.locator('#accounts tr[data-id="g"]');
  const height = (await row.boundingBox())!.height;
  await expect(row.getByRole("tooltip")).toBeHidden();
  await row.getByRole("button", { name: "查看重试详情" }).hover();
  await expect(row.getByRole("tooltip")).toBeVisible();
  await expect(row.getByRole("tooltip")).toContainText("2026/10/30");
  expect((await row.boundingBox())!.height).toBe(height);
  expect((await row.getByRole("button", { name: "删除账号组" }).innerText()).trim()).toBe("");
  await page.keyboard.press("Escape");
  await expect(page.locator("#dirty-dot")).toBeHidden();
  await page.getByLabel("账号组名称").fill("Draft");
  await expect(page.locator("#dirty-dot")).toBeVisible();
  await page.getByLabel("账号组名称").fill("Group");
  await expect(page.locator("#dirty-dot")).toBeHidden();
  await page.getByRole("tab", { name: "上游配置", exact: true }).click();
  await page.getByLabel("OpenAI GMT 小时", { exact: true }).selectOption("+05");
  await page.getByLabel("OpenAI GMT 分钟", { exact: true }).selectOption("30");
  await expect(page.locator("#provider-dirty-dot")).toBeVisible();
  await page.locator("#save-provider").click();
  await expect(page.locator("#provider-dirty-dot")).toBeHidden();
  expect(prefs[0].timezone).toBe("GMT+05:30");
  await expect(page.locator("#openai-python")).toHaveJSProperty("open", true);
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.getByLabel("账号组名称").fill("Write failure draft");
  fail = true;
  await page.locator("#save").click();
  await expect(page.locator("#message")).toContainText("fixture write failed");
  await expect(page.locator("#dirty-dot")).toBeVisible();
  await expect(page.getByLabel("账号组名称")).toHaveValue("Write failure draft");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => window.scrollTo(0, 0));
  const separate = await page.evaluate(() => {
    const add = document.querySelector("#add")!.getBoundingClientRect();
    const save = document.querySelector("#account-groups .save-toolbar")!.getBoundingClientRect();
    return (
      add.right <= save.left ||
      save.right <= add.left ||
      add.bottom <= save.top ||
      save.bottom <= add.top
    );
  });
  expect(
    separate,
    "Scoped save must not cover the add-group button on narrow screens",
  ).toBeTruthy();
  await page.locator("#add").click({ trial: true });
});

test("LTSP native account CRUD saves immediately while preserving policy drafts", async ({
  page,
  context,
}) => {
  type Key = {
    id: string;
    name: string;
    value: string;
    enabled: boolean;
    models: string[];
    weight: number;
  };
  let groups = [
    {
      id: "g",
      name: "Group",
      provider: "openai",
      key_ids: ["k"],
      refresh_date: "2026-10-30",
      cycle: "monthly",
    },
  ];
  let prefs = structuredClone(schedules),
    nextID = 1,
    failDelete = false;
  const keys: Record<string, Key[]> = {
    openai: [
      {
        id: "k",
        name: "Original account",
        value: "fixture-old-secret",
        enabled: true,
        models: ["restricted"],
        weight: 7,
      },
    ],
    anthropic: [],
  };
  const providers = new Set(["openai"]);
  const writes: Array<{ method: string; body: Record<string, unknown> }> = [];
  const view = () => ({
    accounts: groups,
    provider_schedules: prefs,
    keys: Object.fromEntries(
      Object.entries(keys).map(([p, list]) => [
        p,
        list.map(({ id, name, enabled }) => ({ id, name, enabled })),
      ]),
    ),
    access_key_name: "ltsp-lan-clients",
  });
  await page.route("**/api/ltsp/accounts", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      if (body.accounts)
        groups = body.accounts.map((a: (typeof groups)[number]) => ({
          ...a,
          key_ids: groups.find((g) => g.id === a.id)?.key_ids || [],
        }));
      if (body.provider_schedules) prefs = body.provider_schedules;
    }
    await route.fulfill({ json: view() });
  });
  await page.route("**/api/ltsp/keys/*/*/secret", async (route) => {
    const parts = new URL(route.request().url()).pathname.split("/");
    const key = keys[parts[4]]?.find((key) => key.id === parts[5]);
    await route.fulfill({ json: { value: key?.value }, headers: { "Cache-Control": "no-store" } });
  });
  await page.route("**/api/ltsp/assignments", async (route) => {
    const body = route.request().postDataJSON();
    for (const group of groups) group.key_ids = group.key_ids.filter((id) => id !== body.key_id);
    groups.find((g) => g.id === body.group_id)?.key_ids.push(body.key_id);
    await route.fulfill({ json: view() });
  });
  await page.route("**/api/providers", async (route) => {
    providers.add(route.request().postDataJSON().provider);
    await route.fulfill({ json: {} });
  });
  await page.route("**/api/providers/*/keys**", async (route) => {
    const parts = new URL(route.request().url()).pathname.split("/"),
      provider = parts[3],
      id = parts[5],
      method = route.request().method();
    if (!providers.has(provider)) {
      await route.fulfill({ status: 404, json: { error: { message: "Provider not found" } } });
      return;
    }
    if (method === "DELETE") {
      if (failDelete) {
        await route.fulfill({ status: 500, json: { error: { message: "delete failed" } } });
        return;
      }
      keys[provider] = keys[provider].filter((k) => k.id !== id);
    } else {
      const body = route.request().postDataJSON();
      writes.push({ method, body });
      if (method === "POST") keys[provider].push({ ...body, id: "new-" + nextID++ });
      else Object.assign(keys[provider].find((k) => k.id === id)!, body);
    }
    await route.fulfill({ json: {} });
  });
  await page.goto("/ltsp/groups");
  await page.getByLabel("账号组名称").fill("Group draft");
  await page.getByRole("tab", { name: "上游配置", exact: true }).click();
  await page.getByLabel("OpenAI 扣款（额度刷新）截止时间", { exact: true }).fill("21:00");
  await page.getByRole("tab", { name: "API Key", exact: true }).click();
  await page.locator('[data-key-id="k"] .edit-account').click();
  const secretInput = page.locator("#account-dialog").getByLabel("API Key", { exact: true });
  await expect(secretInput).toHaveValue("fixture-old-secret");
  await expect(secretInput).toHaveAttribute("type", "password");
  await expect(secretInput).toHaveAttribute("autocomplete", "off");
  const secretNode = await page.locator("#account-secret").evaluateHandle((el) => el);
  await page.getByRole("button", { name: "显示 API Key", exact: true }).click();
  await expect(page.locator("#account-secret")).toHaveAttribute("type", "text");
  await page.getByRole("button", { name: "隐藏 API Key", exact: true }).click();
  const secure = await page.evaluate(() => window.isSecureContext);
  if (secure) await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "复制 API Key", exact: true }).click();
  await expect(page.getByRole("button", { name: "复制 API Key", exact: true })).toHaveAttribute(
    "title",
    "已复制",
  );
  if (secure)
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("fixture-old-secret");
  expect(
    await secretNode.evaluate((el) => el === document.querySelector("#account-secret")),
  ).toBeTruthy();
  await page.getByLabel("API Key 名称", { exact: true }).fill("Renamed account");
  await page.getByLabel("启用", { exact: true }).uncheck();
  await page.getByRole("button", { name: "保存 API Key", exact: true }).click();
  await expect(page.locator("#account-dialog")).not.toBeVisible();
  await expect(page.locator("#account-secret")).toHaveValue("");
  expect(writes[0].body).not.toHaveProperty("value");
  expect(keys.openai[0]).toMatchObject({
    name: "Renamed account",
    value: "fixture-old-secret",
    enabled: false,
    models: ["restricted"],
    weight: 7,
  });
  await expect(page.getByLabel("账号组名称")).toHaveValue("Group draft");
  await expect(page.getByLabel("OpenAI 扣款（额度刷新）截止时间", { exact: true })).toHaveValue(
    "21:00",
  );
  await expect(page.locator("#dirty-dot")).not.toHaveAttribute("hidden");
  await expect(page.locator("#provider-dirty-dot")).not.toHaveAttribute("hidden");
  await page.locator('[data-key-id="k"] .edit-account').click();
  await secretInput.fill("fixture-new-secret");
  await page.getByRole("button", { name: "保存 API Key", exact: true }).click();
  await expect(page.locator("#account-dialog")).not.toBeVisible();
  expect(keys.openai[0].value).toBe("fixture-new-secret");
  await page.getByRole("button", { name: "添加 API Key", exact: true }).click();
  await page.getByLabel("上游", { exact: true }).selectOption("anthropic");
  await page.getByLabel("API Key 名称", { exact: true }).fill("New Claude account");
  await secretInput.fill("fixture-claude-secret");
  await page.getByRole("button", { name: "保存 API Key", exact: true }).click();
  await expect(page.locator("#account-dialog")).not.toBeVisible();
  await expect(page.getByLabel("所属账号组：New Claude account")).toHaveValue("");
  await page.locator('[data-key-id="k"] .delete-account').click();
  await page.locator("#confirm-delete-account").click();
  await expect(page.locator("#delete-account-dialog")).not.toBeVisible();
  await expect(page.locator('#key-assignments tr[data-key-id="k"]')).toHaveCount(0);
  expect(groups[0].key_ids).toEqual([]);
  await expect(page.getByLabel("账号组名称")).toHaveValue("Group draft");
  failDelete = true;
  await page.locator('[data-key-id="new-1"] .delete-account').click();
  await page.locator("#confirm-delete-account").click();
  await expect(page.locator("#delete-account-error")).toContainText("delete failed");
  expect(keys.anthropic).toHaveLength(1);
  await page.locator("#cancel-delete-account").click();
  await page.getByRole("tab", { name: "账号组", exact: true }).click();
  await page.locator("#save").click();
  await expect(page.locator("#dirty-dot")).toBeHidden();
  expect(prefs[0].cutoff).toBe("20:00");
  await expect(page.locator("#provider-dirty-dot")).not.toHaveAttribute("hidden");
  await page.getByRole("tab", { name: "上游配置", exact: true }).click();
  await page.locator("#save-provider").click();
  await expect(page.locator("#provider-dirty-dot")).toBeHidden();
  expect(prefs[0].cutoff).toBe("21:00");
});

test("LTSP API Key editor blocks writes until the existing secret loads and rejects empty edits", async ({
  page,
}) => {
  let fail = true,
    writes = 0;
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: {
        accounts: [],
        provider_schedules: schedules,
        keys: { openai: [{ id: "k", name: "Key", enabled: true }] },
      },
    }),
  );
  await page.route("**/api/ltsp/keys/openai/k/secret", (route) =>
    route.fulfill(
      fail
        ? { status: 503, json: { error: "read unavailable" } }
        : { json: { value: "fixture-current-secret" } },
    ),
  );
  await page.route("**/api/providers/**", (route) => {
    writes++;
    return route.fulfill({ json: {} });
  });
  await page.goto("/ltsp/accounts");
  await page.getByRole("button", { name: "编辑 API Key", exact: true }).click();
  await expect(page.locator("#account-error")).toContainText("read unavailable");
  await expect(page.getByRole("button", { name: "保存 API Key", exact: true })).toBeDisabled();
  await expect(page.locator("#account-secret")).toBeDisabled();
  await page.locator("#account-cancel").click();
  await expect(page.locator("#account-secret")).toHaveValue("");
  fail = false;
  await page.getByRole("button", { name: "编辑 API Key", exact: true }).click();
  await expect(page.locator("#account-secret")).toHaveValue("fixture-current-secret");
  await page.locator("#account-secret").fill("");
  await page.getByRole("button", { name: "保存 API Key", exact: true }).click();
  expect(
    await page
      .locator("#account-secret")
      .evaluate((el: HTMLInputElement) => el.validity.valueMissing),
  ).toBeTruthy();
  await expect(page.locator("#account-dialog")).toBeVisible();
  expect(writes).toBe(0);
  await page.locator("#account-cancel").click();
});

test("LTSP group layout fits split panes and controls follow light and dark themes", async ({
  page,
}) => {
  await page.route("**/api/ltsp/accounts", (route) =>
    route.fulfill({
      json: {
        accounts: [
          {
            id: "responsive-group",
            name: "Long account name for split pane",
            provider: "openai",
            key_ids: ["responsive-key"],
            refresh_date: "2026-10-30",
            cycle: "monthly",
            balance: "unknown",
          },
        ],
        keys: { openai: [{ id: "responsive-key", name: "Responsive account", enabled: true }] },
        provider_schedules: schedules.map((s) => ({ ...s, timezone: "America/Los_Angeles" })),
        access_key_name: "ltsp-lan-clients",
      },
    }),
  );
  await page.goto("/ltsp/groups");
  await expect(page.getByLabel("OpenAI 地区时区", { exact: true })).toHaveValue(
    "America/Los_Angeles",
  );
  await expect(page.locator(".refresh-zone").first()).toHaveText("假定最晚 20:00 扣费刷新");
  await expect(page.locator(".refresh-zone").first()).toHaveAttribute(
    "title",
    "按 America/Los_Angeles 时区判断",
  );
  await expect(page.locator(".gateway-title a")).toHaveAttribute(
    "href",
    "https://github.com/maximhq/bifrost",
  );
  const stable = await page.getByLabel("账号组名称").evaluateHandle((el) => el);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1280, 960, 820, 640, 390]) {
      await page.setViewportSize({ width, height: 900 });
      if (width === 1280 || width === 960) {
        const inline = await page
          .locator("#accounts tr[data-id]")
          .first()
          .evaluate((row) => {
            const date = row.querySelector('[name="refresh_date"]')!.getBoundingClientRect();
            const cycle = row.querySelector('[name="cycle"]')!.getBoundingClientRect();
            return Math.abs(date.top - cycle.top) < 2;
          });
        expect(inline).toBe(width === 1280);
      }
      const fits = await page
        .locator("#accounts")
        .evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
      expect(
        fits,
        `Account groups need no horizontal scroll at ${width}px in ${scheme} mode`,
      ).toBeTruthy();
      for (const selector of [
        "#accounts input",
        "#accounts select",
        "#accounts .probe",
        "#accounts .remove",
      ]) {
        const inside = await page
          .locator(selector)
          .first()
          .evaluate((el) => {
            const box = el.getBoundingClientRect(),
              table = document.querySelector("#accounts")!.getBoundingClientRect();
            return box.width > 20 && box.left >= table.left && box.right <= table.right;
          });
        expect(inside, `${selector} remains usable at ${width}px`).toBeTruthy();
      }
      expect(
        await stable.evaluate((el) => el === document.querySelector("#accounts input[name=name]")),
      ).toBeTruthy();
    }
    const colors = await page
      .locator("#provider-settings select[name=timezone_mode]")
      .first()
      .evaluate((el) => {
        const select = getComputedStyle(el),
          option = getComputedStyle(el.querySelector("option")!);
        return {
          background: select.backgroundColor,
          foreground: select.color,
          optionBackground: option.backgroundColor,
          optionForeground: option.color,
        };
      });
    expect(colors.background).not.toBe("rgba(0, 0, 0, 0)");
    expect(colors.optionBackground).toBe(colors.background);
    expect(colors.optionForeground).toBe(colors.foreground);
    if (scheme === "dark") {
      const channels = colors.background.match(/\d+/g)!.slice(0, 3).map(Number);
      expect(Math.max(...channels), "Dark select menu must have a dark background").toBeLessThan(
        100,
      );
    }
    await page.getByRole("tab", { name: "API Key", exact: true }).click();
    await page.getByRole("button", { name: "添加 API Key", exact: true }).click();
    const dialogColors = await page.locator("#account-provider").evaluate((el) => ({
      select: getComputedStyle(el).backgroundColor,
      option: getComputedStyle(el.querySelector("option")!).backgroundColor,
    }));
    expect(dialogColors.select).toBe(colors.background);
    expect(dialogColors.option).toBe(colors.background);
    await page.locator("#account-cancel").click();
    await page.getByRole("tab", { name: "账号组", exact: true }).click();
  }
});